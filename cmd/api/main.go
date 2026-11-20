// Command api runs the HTTP API of the notification service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/config"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/database"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/handler"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/metrics"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/repository"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/service"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped with error", "error", err)
		os.Exit(1)
	}
}

// run starts the API and blocks until it shuts down.
func run() error {
	// Local development only; ignored when the file is missing.
	if err := config.LoadDotEnv(".env.local"); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// JSON to stdout; the wrapper adds correlation_id to every line logged with a request context.
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
	defer db.Close() // runs last, after the server has shut down
	slog.Info("connected to database")

	// Wire the layers: repository -> service -> HTTP handlers.
	repo := repository.NewNotifications(db)
	templateRepo := repository.NewTemplates(db)
	notifications := service.NewNotifications(repo, templateRepo)
	templates := service.NewTemplates(templateRepo)

	// Metrics at /metrics: HTTP requests, the queue (counted in the database), Go runtime (D20).
	reg := metrics.NewRegistry()
	reg.MustRegister(metrics.NewQueueCollector(repo))

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: handler.NewRouter(logger, db, notifications, templates, reg),
		// Protect against slow or stuck clients.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Cancelled on Ctrl+C (SIGINT) or docker stop (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Buffered so the goroutine can exit even after nobody is reading.
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("api starting", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	// Wait for a server error or a stop signal.
	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Stop accepting requests and let in-flight ones finish (max 10s).
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("api stopped")
	return nil
}
