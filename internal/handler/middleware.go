package handler

import (
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
)

// headerCorrelationID carries the correlation ID in requests and responses (D13).
const headerCorrelationID = "X-Correlation-ID"

// safeCorrelationID limits client-supplied IDs to short, harmless log labels.
var safeCorrelationID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// CorrelationID gives every request a correlation ID: the client's if it sent a safe one,
// otherwise a new UUID. The ID goes into the request context and the response header.
func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerCorrelationID)
		if !safeCorrelationID.MatchString(id) {
			id = uuid.NewString() // missing or unsafe: replace it; a bad label shouldn't fail the request
		}

		w.Header().Set(headerCorrelationID, id)
		next.ServeHTTP(w, r.WithContext(logging.WithCorrelationID(r.Context(), id)))
	})
}

// RequestLogger logs one line per request: method, path, status, size and duration.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor) // captures status and size

			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // nothing written: net/http sends 200
			}

			level := slog.LevelInfo
			if status >= http.StatusInternalServerError {
				level = slog.LevelError
			}

			logger.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", ww.BytesWritten(),
				"duration_ms", float64(time.Since(start).Microseconds())/1000,
			)
		})
	}
}
