package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePinger stands in for the database: Ping returns err.
type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		db         Pinger
		wantStatus int
		wantBody   string
	}{
		{"database up", fakePinger{}, http.StatusOK,
			`{"status":"ok","checks":{"database":"ok"}}`},
		{"database down", fakePinger{err: errors.New("connection refused")}, http.StatusServiceUnavailable,
			`{"status":"unavailable","checks":{"database":"unreachable"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()

			NewRouter(slog.New(slog.DiscardHandler), tt.db).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != tt.wantBody {
				t.Errorf("body = %s, want %s", body, tt.wantBody)
			}
		})
	}
}
