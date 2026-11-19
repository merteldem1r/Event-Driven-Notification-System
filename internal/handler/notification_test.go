package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// fakeNotificationService records the notification it receives and returns err or a stored copy.
type fakeNotificationService struct {
	err   error
	calls int
	got   domain.Notification
}

var (
	fakeID = uuid.MustParse("7c9e6679-7425-40de-944b-e07fc1f90ae7")
	// Istanbul time (UTC+3), to check that responses are converted to UTC.
	fakeCreatedAt = time.Date(2026, 11, 20, 13, 0, 0, 0, time.FixedZone("TRT", 3*60*60))
)

func (f *fakeNotificationService) Create(_ context.Context, n domain.Notification) (domain.Notification, error) {
	f.calls++
	f.got = n
	if f.err != nil {
		return domain.Notification{}, f.err
	}
	n.ID, n.Status, n.CreatedAt, n.UpdatedAt = fakeID, domain.StatusPending, fakeCreatedAt, fakeCreatedAt
	return n, nil
}

// postNotification sends body to POST /api/v1/notifications through the full router.
func postNotification(t *testing.T, svc NotificationService, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/notifications", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc).ServeHTTP(rec, req)
	return rec
}

func TestCreateNotification_Accepted(t *testing.T) {
	svc := &fakeNotificationService{}
	rec := postNotification(t, svc,
		`{"channel":"sms","recipient":"+905551234567","content":"hello","priority":"high"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/notifications/"+fakeID.String() {
		t.Errorf("Location = %q", loc)
	}

	want := domain.Notification{
		Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityHigh,
	}
	if svc.got != want {
		t.Errorf("service got %+v, want %+v", svc.got, want)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	for field, wantValue := range map[string]any{
		"id":         fakeID.String(),
		"channel":    "sms",
		"recipient":  "+905551234567",
		"content":    "hello",
		"priority":   "high",
		"status":     "pending",
		"attempts":   float64(0), // JSON numbers decode as float64
		"created_at": "2026-11-20T10:00:00Z",
	} {
		if body[field] != wantValue {
			t.Errorf("%s = %v, want %v", field, body[field], wantValue)
		}
	}
}

func TestCreateNotification_Priority(t *testing.T) {
	tests := []struct {
		name     string
		priority string
		want     domain.Priority
	}{
		{"missing defaults to normal", "", domain.PriorityNormal},
		{"high", "high", domain.PriorityHigh},
		{"low", "low", domain.PriorityLow},
		{"unknown becomes invalid", "urgent", 0}, // the service's validation rejects it
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{}
			body := `{"channel":"sms","recipient":"+905551234567","content":"hi"`
			if tt.priority != "" {
				body += `,"priority":"` + tt.priority + `"`
			}
			postNotification(t, svc, body+"}")

			if svc.got.Priority != tt.want {
				t.Errorf("priority = %d, want %d", svc.got.Priority, tt.want)
			}
		})
	}
}

func TestCreateNotification_ValidationError(t *testing.T) {
	svc := &fakeNotificationService{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "recipient", Message: "is required"},
		{Field: "priority", Message: "must be high, normal or low"},
	}}}
	rec := postNotification(t, svc, `{"channel":"sms","content":"hi","priority":"urgent"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	want := `{"error":{"code":"validation_failed","message":"the request has invalid fields","details":[` +
		`{"field":"recipient","message":"is required"},{"field":"priority","message":"must be high, normal or low"}]}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestCreateNotification_Errors(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		svcErr      error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{"empty body", "", nil, http.StatusBadRequest, codeInvalidBody, "request body is empty"},
		{"malformed JSON", `{"channel":`, nil, http.StatusBadRequest, codeInvalidBody, "malformed JSON"},
		{"not an object", `[1,2]`, nil, http.StatusBadRequest, codeInvalidBody, "request body must be a JSON object"},
		{"wrong type", `{"channel":5}`, nil, http.StatusBadRequest, codeInvalidBody, `field "channel" must be a string`},
		{"unknown field", `{"recipent":"+905551234567"}`, nil, http.StatusBadRequest, codeInvalidBody, `unknown field "recipent"`},
		{"two objects", `{}{}`, nil, http.StatusBadRequest, codeInvalidBody, "request body must contain a single JSON object"},
		{"too large", `{"content":"` + strings.Repeat("a", maxBodyBytes) + `"}`, nil,
			http.StatusRequestEntityTooLarge, codeBodyTooLarge, "request body must be at most 1048576 bytes"},
		{"service failure is hidden", `{"channel":"sms"}`, errors.New("connection refused"),
			http.StatusInternalServerError, codeInternal, "something went wrong"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{err: tt.svcErr}
			rec := postNotification(t, svc, tt.body)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not the error format: %v\n%s", err, rec.Body)
			}
			if got.Error.Code != tt.wantCode || !strings.HasPrefix(got.Error.Message, tt.wantMessage) {
				t.Errorf("error = %+v, want code %q and message %q", got.Error, tt.wantCode, tt.wantMessage)
			}
			if tt.svcErr == nil && svc.calls != 0 {
				t.Error("service was called for an unreadable body")
			}
		})
	}
}
