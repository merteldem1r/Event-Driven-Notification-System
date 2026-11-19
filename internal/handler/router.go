// Package handler contains the HTTP layer of the API: routes, handlers and
// JSON helpers. It translates HTTP into calls to the service layer.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter builds the HTTP router with every route of the API.
// Keeping it in one function lets tests exercise the exact same routing as main.
func NewRouter() http.Handler {
	r := chi.NewRouter()

	// Recoverer turns a panic in a handler into a 500 response
	// instead of crashing the whole process.
	r.Use(middleware.Recoverer)

	// Operational endpoints stay at the root: Docker, load balancers and
	// monitoring call them, and they must not move when the API version changes.
	r.Get("/health", Health)

	// Public API, versioned. A breaking change later would get /api/v2
	// next to this group, so existing clients keep working.
	r.Route("/api/v1", func(r chi.Router) {
		// Notification routes are added here in Phase 4, e.g.:
		// r.Post("/notifications", ...)
	})

	return r
}
