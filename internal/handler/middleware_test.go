package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/logging"
)

// newBufferLogger returns a JSON logger that writes into buf.
func newBufferLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// decodeLogLine parses the single JSON log line in buf.
func decodeLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not one JSON line: %v\n%s", err, buf.String())
	}
	return line
}

func TestRequestLogger_LogsEveryRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"existing route", http.MethodGet, "/health", http.StatusOK},
		{"unknown route", http.MethodGet, "/nope", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/health", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			router := NewRouter(newBufferLogger(&buf), fakePinger{}, nil)

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			router.ServeHTTP(httptest.NewRecorder(), req)

			line := decodeLogLine(t, &buf)
			if line["msg"] != "http request" {
				t.Errorf("msg = %v, want \"http request\"", line["msg"])
			}
			if line["method"] != tt.method {
				t.Errorf("method = %v, want %s", line["method"], tt.method)
			}
			if line["path"] != tt.path {
				t.Errorf("path = %v, want %s", line["path"], tt.path)
			}
			if line["status"] != float64(tt.wantStatus) { // JSON numbers decode as float64
				t.Errorf("status = %v, want %d", line["status"], tt.wantStatus)
			}
			if _, ok := line["duration_ms"]; !ok {
				t.Error("duration_ms missing from log line")
			}
		})
	}
}

func TestRequestLogger_ServerErrorIsLoggedAsError(t *testing.T) {
	var buf bytes.Buffer
	failing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	RequestLogger(newBufferLogger(&buf))(failing).ServeHTTP(httptest.NewRecorder(), req)

	line := decodeLogLine(t, &buf)
	if line["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", line["level"])
	}
	if line["status"] != float64(http.StatusServiceUnavailable) {
		t.Errorf("status = %v, want 503", line["status"])
	}
}

func TestCorrelationID(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string // "" means a newly generated UUID
	}{
		{"client ID is kept", "shop-req-42", "shop-req-42"},
		{"UUID from client is kept", "7c9e6679-7425-40de-944b-e07fc1f90ae7", "7c9e6679-7425-40de-944b-e07fc1f90ae7"},
		{"missing: generated", "", ""},
		{"space: replaced", "shop req 42", ""},
		{"newline: replaced", "abc\ninjected", ""},
		{"too long: replaced", strings.Repeat("a", 129), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var inContext string
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				inContext = logging.CorrelationID(r.Context())
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set(headerCorrelationID, tt.header)
			}
			rec := httptest.NewRecorder()
			CorrelationID(next).ServeHTTP(rec, req)

			header := rec.Header().Get(headerCorrelationID)
			if header != inContext {
				t.Errorf("response header %q and context %q differ", header, inContext)
			}
			if tt.want != "" && header != tt.want {
				t.Errorf("ID = %q, want %q", header, tt.want)
			}
			if tt.want == "" && uuid.Validate(header) != nil {
				t.Errorf("ID = %q, want a generated UUID", header)
			}
		})
	}
}

// The request log line carries the same ID the client gets back.
func TestRouter_LogsCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(logging.NewHandler(slog.NewJSONHandler(&buf, nil)))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	req.Header.Set(headerCorrelationID, "shop-req-42")
	rec := httptest.NewRecorder()
	NewRouter(logger, fakePinger{}, nil).ServeHTTP(rec, req)

	if line := decodeLogLine(t, &buf); line["correlation_id"] != "shop-req-42" {
		t.Errorf("log correlation_id = %v, want shop-req-42", line["correlation_id"])
	}
	if h := rec.Header().Get(headerCorrelationID); h != "shop-req-42" {
		t.Errorf("response %s = %q, want shop-req-42", headerCorrelationID, h)
	}
}
