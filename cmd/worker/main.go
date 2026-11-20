// Command worker takes notifications from the queue and sends them to the provider.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/config"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/database"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/provider"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/repository"
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
		return errors.New("PROVIDER_URL is required for the worker: set it to your webhook.site URL")
	}

	logger := slog.New(logging.NewHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}),
	))
	slog.SetDefault(logger)

	// Fail fast if the database isn't reachable at startup.
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Connect(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		return err
	}
	defer db.Close() // runs last, after in-flight work has finished
	slog.Info("connected to database")

	// Cancelled on Ctrl+C (SIGINT) or docker stop (SIGTERM): stop taking new work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Wire the layers: repository (the queue) + provider client -> one dispatcher per channel
	// -> worker loops for each (D16).
	queue := repository.NewNotifications(db)
	sender := provider.NewClient(cfg.ProviderURL, cfg.ProviderTimeout)
	var dispatchers []worker.Processor
	for _, ch := range domain.Channels() {
		dispatchers = append(dispatchers, service.NewDispatcher(ch, queue, sender, cfg.WorkerLeaseDuration, logger))
	}
	w := worker.New(dispatchers, worker.Config{
		Concurrency:     cfg.WorkerConcurrency,
		PollInterval:    cfg.WorkerPollInterval,
		ShutdownTimeout: cfg.WorkerShutdownTimeout,
	}, logger)
	return w.Run(ctx)
}
