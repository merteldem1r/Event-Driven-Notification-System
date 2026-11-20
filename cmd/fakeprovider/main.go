// Command fakeprovider is a local stand-in for the external provider (D9): it answers like
// webhook.site, with a set delay and failure rate, for development and load tests.
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
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/fakeprovider"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fake provider stopped with error", "error", err)
		os.Exit(1)
	}
}

// run starts the fake provider and blocks until it shuts down.
func run() error {
	// Local development only; ignored when the file is missing.
	if err := config.LoadDotEnv(".env.local"); err != nil {
		return err
	}
	cfg, err := config.LoadFakeProvider()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	p := fakeprovider.New(fakeprovider.Config{Latency: cfg.Latency, FailureRate: cfg.FailureRate}, logger)
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           p,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.Latency + 10*time.Second, // every answer waits the latency first
		IdleTimeout:       60 * time.Second,
	}

	// Cancelled on Ctrl+C (SIGINT) or docker stop (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go p.Report(ctx)

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("fake provider starting", "addr", srv.Addr,
			"latency", cfg.Latency.String(), "failure_rate", cfg.FailureRate)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("fake provider stopped")
	return nil
}
