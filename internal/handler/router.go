// Package handler is the HTTP layer: routes, handlers and middleware.
package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/merteldem1r/Event-Driven-Notification-System/docs"
)

// NewRouter builds the HTTP router. With a registry, requests are measured and its metrics are
// served at /metrics; tests that don't need metrics pass nil.
func NewRouter(logger *slog.Logger, db Pinger, notifications NotificationService, reg *prometheus.Registry) http.Handler {
	r := chi.NewRouter()

	r.Use(CorrelationID)         // first, so every later log line has the ID
	r.Use(RequestLogger(logger)) // before Recoverer, so it also logs the 500s Recoverer produces
	if reg != nil {
		r.Use(RequestMetrics(reg)) // also before Recoverer, to count the 500s
	}
	r.Use(middleware.Recoverer) // panic -> 500 instead of a crash

	// Unknown URLs and wrong methods use the same JSON error format as everything else (D11).
	r.NotFound(routeNotFound)
	r.MethodNotAllowed(methodNotAllowed(r))

	// Operational endpoints stay outside the versioned API.
	r.Get("/health", Health(map[string]Pinger{"database": db}, logger))
	if reg != nil {
		r.Method(http.MethodGet, "/metrics", Metrics(reg, logger))
	}
	r.Get("/openapi.yaml", OpenAPISpec(docs.OpenAPI()))
	r.Get("/docs", SwaggerUI) // interactive documentation of everything above

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/notifications", ListNotifications(notifications, logger))
		r.Post("/notifications", CreateNotification(notifications, logger))
		r.Post("/notifications/batch", CreateNotificationBatch(notifications, logger))
		r.Get("/notifications/{id}", GetNotification(notifications, logger))
		r.Post("/notifications/{id}/cancel", CancelNotification(notifications, logger))
		r.Get("/batches/{id}", GetBatch(notifications, logger))
		r.Post("/batches/{id}/cancel", CancelBatch(notifications, logger))
	})

	return r
}

// NewOpsRouter builds the worker's HTTP server: /health (every dependency in checks) and
// /metrics only. Requests aren't logged: Docker and Prometheus call these every few seconds.
func NewOpsRouter(logger *slog.Logger, checks map[string]Pinger, reg *prometheus.Registry) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.NotFound(routeNotFound)
	r.MethodNotAllowed(methodNotAllowed(r))
	r.Get("/health", Health(checks, logger))
	r.Method(http.MethodGet, "/metrics", Metrics(reg, logger))
	return r
}

// routeNotFound answers requests for URLs that have no route.
func routeNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeRouteNotFound, "no endpoint at "+r.URL.Path)
}

// methodNotAllowed answers requests whose URL exists but not with this method.
// A custom handler loses chi's Allow header, so it asks routes which methods match.
func methodNotAllowed(routes chi.Routes) http.HandlerFunc {
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

	return func(w http.ResponseWriter, r *http.Request) {
		var allowed []string
		for _, m := range methods {
			if routes.Match(chi.NewRouteContext(), m, r.URL.Path) {
				allowed = append(allowed, m)
			}
		}
		list := strings.Join(allowed, ", ")

		w.Header().Set("Allow", list) // required on 405 by RFC 9110
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed,
			fmt.Sprintf("method %s is not allowed at %s; allowed: %s", r.Method, r.URL.Path, list))
	}
}
