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
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/handler"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped with error", "error", err)
		os.Exit(1)
	}
}

// run holds the real startup logic. Returning an error (instead of calling
// os.Exit deep inside) keeps deferred cleanup working and main trivial.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Structured JSON logs: one JSON object per line, easy for tools to search.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: handler.NewRouter(),
		// Timeouts protect the server from slow or stuck clients
		// holding connections open forever.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// ctx is cancelled when the process gets Ctrl+C (SIGINT)
	// or `docker stop` (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ListenAndServe blocks, so it runs in its own goroutine while
	// this one waits for either a server error or a shutdown signal.
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("api starting", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown: stop accepting new requests, let in-flight
	// requests finish, but give up after 10 seconds.
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("api stopped")
	return nil
}
