package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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

			NewRouter(slog.New(slog.DiscardHandler), tt.db, nil, nil).ServeHTTP(rec, req)

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

// The worker's server checks the database and Redis: one down is enough for 503, and the body
// says which (OBS-4, D16).
func TestOpsRouter_Health(t *testing.T) {
	down := fakePinger{err: errors.New("connection refused")}
	tests := []struct {
		name       string
		redis      Pinger
		wantStatus int
		wantBody   string
	}{
		{"both up", fakePinger{}, http.StatusOK, `{"status":"ok","checks":{"database":"ok","redis":"ok"}}`},
		{"redis down", down, http.StatusServiceUnavailable,
			`{"status":"unavailable","checks":{"database":"ok","redis":"unreachable"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewOpsRouter(slog.New(slog.DiscardHandler),
				map[string]Pinger{"database": fakePinger{}, "redis": tt.redis}, prometheus.NewRegistry())
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil))
			if rec.Code != tt.wantStatus || strings.TrimSpace(rec.Body.String()) != tt.wantBody {
				t.Errorf("GET /health = %d %s, want %d %s", rec.Code, rec.Body, tt.wantStatus, tt.wantBody)
			}
		})
	}
}

// The worker's server serves its registry, and nothing of the API.
func TestOpsRouter_Routes(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "test_total", Help: "A test counter."}))
	router := NewOpsRouter(slog.New(slog.DiscardHandler), map[string]Pinger{}, reg)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "test_total 0") {
		t.Errorf("GET /metrics = %d, body without test_total", rec.Code)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/notifications", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/notifications on the worker = %d, want 404", rec.Code)
	}
}
