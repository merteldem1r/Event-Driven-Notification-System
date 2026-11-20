// Command worker takes notifications from the queue and sends them to the provider.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/config"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/database"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/handler"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/metrics"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/provider"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/ratelimit"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/repository"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/retry"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/service"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped with error", "error", err)
		os.Exit(1)
	}
}

// run starts the worker and blocks until it has shut down.
func run() error {
	// Local development only; ignored when the file is missing.
	if err := config.LoadDotEnv(".env.local"); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.ProviderURL == "" { // optional for the API, which never sends
		return errors.New("PROVIDER_URL is required for the worker: the fake provider (http://localhost:9090/) or your webhook.site URL")
	}
	if cfg.RedisURL == "" { // optional for the API, which doesn't rate limit
		return errors.New("REDIS_URL is required for the worker: it holds the shared rate limit")
	}

	logger := slog.New(logging.NewHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}),
	))
	slog.SetDefault(logger)
	redis.SetLogger(database.RedisLogger{Logger: logger}) // the library's messages as JSON too

	// Fail fast if the database or Redis isn't reachable at startup.
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelConnect()
	db, err := database.Connect(connectCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close() // runs last, after in-flight work has finished
	slog.Info("connected to database")

	rdb, err := database.ConnectRedis(connectCtx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			slog.Warn("close redis", "error", err)
		}
	}()
	slog.Info("connected to redis")
	cancelConnect() // done connecting; the deferred call covers the early returns above

	// Cancelled on Ctrl+C (SIGINT) or docker stop (SIGTERM): stop taking new work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Wire the layers: repository (the queue) + provider client + shared rate limiter
	// -> one dispatcher per channel -> worker loops for each (D16).
	queue := repository.NewNotifications(db)
	sender := provider.NewClient(cfg.ProviderURL, cfg.ProviderTimeout)
	limiter := ratelimit.New(rdb, cfg.RateLimitPerSecond, cfg.RateLimitBurst)
	retries := retry.New(cfg.RetryMaxAttempts, cfg.RetryBaseDelay, cfg.RetryMaxDelay)
	reg := metrics.NewRegistry()
	sends := metrics.NewSends(reg) // every send's result and duration (D20)
	var dispatchers []worker.Processor
	for _, ch := range domain.Channels() {
		dispatchers = append(dispatchers,
			service.NewDispatcher(ch, queue, sender, limiter, retries, sends, cfg.WorkerLeaseDuration, logger))
	}
	slog.Info("rate limit per channel", "per_second", cfg.RateLimitPerSecond, "burst", cfg.RateLimitBurst)
	slog.Info("retries", "max_attempts", cfg.RetryMaxAttempts, "base_delay", cfg.RetryBaseDelay.String(), "max_delay", cfg.RetryMaxDelay.String())
	w := worker.New(dispatchers, sends.CountRecovered(queue), worker.Config{
		Concurrency:     cfg.WorkerConcurrency,
		PollInterval:    cfg.WorkerPollInterval,
		ShutdownTimeout: cfg.WorkerShutdownTimeout,
		RecoverInterval: cfg.WorkerRecoverInterval,
	}, logger)

	// /health checks both dependencies the worker needs; /metrics serves reg (OBS-4, D16).
	checks := map[string]handler.Pinger{"database": db, "redis": database.RedisPinger{Client: rdb}}
	srv, err := serveOps(ctx, cfg.WorkerHTTPPort, handler.NewOpsRouter(logger, checks, reg))
	if err != nil {
		return err
	}

	runErr := w.Run(ctx)

	// The loops have stopped; now stop answering health checks and scrapes too.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("stop health and metrics server", "error", err)
	}
	return runErr
}

// serveOps starts the worker's HTTP server for /health and /metrics. It listens before
// returning, so a port that is already in use stops the worker at startup.
func serveOps(ctx context.Context, port int, h http.Handler) (*http.Server, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen for health and metrics: %w", err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health and metrics server stopped", "error", err)
		}
	}()
	slog.Info("health and metrics listening", "addr", ln.Addr().String())
	return srv, nil
}
