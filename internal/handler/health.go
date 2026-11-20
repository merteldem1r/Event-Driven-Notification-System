package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Pinger checks a connection, e.g. the database pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Health returns 200 when every dependency in checks answers, 503 otherwise, with the result
// of each, e.g. {"database": "ok", "redis": "unreachable"}.
func Health(checks map[string]Pinger, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		resp := healthResponse{Status: "ok", Checks: map[string]string{}}
		code := http.StatusOK
		for name, dep := range checks {
			if err := dep.Ping(ctx); err != nil {
				logger.WarnContext(ctx, "health check failed", "check", name, "error", err)
				resp.Checks[name] = "unreachable"
				resp.Status, code = "unavailable", http.StatusServiceUnavailable
				continue
			}
			resp.Checks[name] = "ok"
		}
		writeJSON(w, code, resp)
	}
}
