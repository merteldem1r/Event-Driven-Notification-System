package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger returns a middleware that writes one log line per HTTP request,
// after the request has finished: method, path, status code, response size
// and how long it took. 5xx responses are logged at ERROR level, the rest at INFO.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// A plain ResponseWriter doesn't let us read back what was written.
			// The wrapper records the status code and byte count as the
			// handler writes them, so we can log them afterwards.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r) // run the rest of the chain (other middleware + handler)

			status := ww.Status()
			if status == 0 {
				// The handler wrote nothing; net/http sends 200 in that case.
				status = http.StatusOK
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
