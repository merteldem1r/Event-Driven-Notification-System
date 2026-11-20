package handler

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// Requests are counted per route pattern, not per URL, and /metrics serves them (OBS-1).
func TestRequestMetrics(t *testing.T) {
	router := NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, &fakeNotificationService{}, nil, prometheus.NewRegistry())
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return rec
	}
	get("/api/v1/notifications/" + fakeID.String())                   // 200
	get("/api/v1/notifications/" + fakeID.String())                   // 200
	get("/api/v1/notifications/9b2f0c3e-0000-4000-8000-000000000000") // 404 from the handler: unknown ID
	get("/api/v1/nothing-here")                                       // 404: no route under /api/v1
	get("/random-scanner-path")                                       // 404: no route at all

	rec := get("/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`http_requests_total{method="GET",route="/api/v1/notifications/{id}",status="200"} 2`,
		`http_requests_total{method="GET",route="/api/v1/notifications/{id}",status="404"} 1`,
		`http_requests_total{method="GET",route="unmatched",status="404"} 2`,
		`http_request_duration_seconds_count{method="GET",route="/api/v1/notifications/{id}"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics is missing %s", want)
		}
	}
	for _, leak := range []string{fakeID.String(), "nothing-here", "random-scanner-path"} {
		if strings.Contains(body, leak) {
			t.Errorf("/metrics contains %q: URLs must not become series", leak)
		}
	}
}

// Without a registry (tests, or metrics not wanted) there is no /metrics route.
func TestNoMetricsWithoutRegistry(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, nil, nil, nil).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /metrics without a registry = %d, want 404", rec.Code)
	}
}
