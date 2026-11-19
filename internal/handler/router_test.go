package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouter_UnknownRoutesAndMethods(t *testing.T) {
	const notificationURL = "/api/v1/notifications/7c9e6679-7425-40de-944b-e07fc1f90ae7"

	tests := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantCode    string
		wantAllow   string
		wantMessage string
	}{
		{"unknown path", http.MethodGet, "/nope", http.StatusNotFound, codeRouteNotFound, "",
			"no endpoint at /nope"},
		{"unknown path under /api/v1", http.MethodGet, "/api/v1/nope", http.StatusNotFound, codeRouteNotFound, "",
			"no endpoint at /api/v1/nope"},
		{"GET on the create endpoint", http.MethodGet, "/api/v1/notifications", http.StatusMethodNotAllowed,
			codeMethodNotAllowed, "POST", "method GET is not allowed at /api/v1/notifications; allowed: POST"},
		{"DELETE on a notification", http.MethodDelete, notificationURL, http.StatusMethodNotAllowed,
			codeMethodNotAllowed, "GET", "method DELETE is not allowed at " + notificationURL + "; allowed: GET"},
		{"POST on health", http.MethodPost, "/health", http.StatusMethodNotAllowed,
			codeMethodNotAllowed, "GET", "method POST is not allowed at /health; allowed: GET"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, &fakeNotificationService{}).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if allow := rec.Header().Get("Allow"); allow != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", allow, tt.wantAllow)
			}
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not the JSON error format: %v\n%s", err, rec.Body)
			}
			if got.Error.Code != tt.wantCode || got.Error.Message != tt.wantMessage {
				t.Errorf("error = %+v, want code %q, message %q", got.Error, tt.wantCode, tt.wantMessage)
			}
		})
	}
}
