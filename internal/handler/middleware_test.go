package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newBufferLogger returns a JSON logger that writes into buf,
// so a test can read back exactly what was logged.
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
			router := NewRouter(newBufferLogger(&buf))

			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tt.method, tt.path, nil))

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
			// JSON numbers decode into float64.
			if line["status"] != float64(tt.wantStatus) {
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

	RequestLogger(newBufferLogger(&buf))(failing).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	line := decodeLogLine(t, &buf)
	if line["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", line["level"])
	}
	if line["status"] != float64(http.StatusServiceUnavailable) {
		t.Errorf("status = %v, want 503", line["status"])
	}
}
