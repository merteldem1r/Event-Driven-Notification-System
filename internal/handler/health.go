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

// Health returns 200 when the database answers, 503 otherwise.
func Health(db Pinger, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(ctx, "health check failed", "check", "database", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{
				Status: "unavailable",
				Checks: map[string]string{"database": "unreachable"},
			})
			return
		}

		writeJSON(w, http.StatusOK, healthResponse{
			Status: "ok",
			Checks: map[string]string{"database": "ok"},
		})
	}
}
