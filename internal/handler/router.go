// Package handler is the HTTP layer: routes, handlers and middleware.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter builds the HTTP router.
func NewRouter(logger *slog.Logger, db Pinger, notifications NotificationService) http.Handler {
	r := chi.NewRouter()

	r.Use(RequestLogger(logger)) // outermost, so it also logs 500s from Recoverer
	r.Use(middleware.Recoverer)  // panic -> 500 instead of a crash

	// Operational endpoints stay outside the versioned API.
	r.Get("/health", Health(db, logger))

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/notifications", CreateNotification(notifications, logger))
	})

	return r
}
