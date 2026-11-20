package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// fakeNotificationService records the notification it receives and returns err or a stored copy.
type fakeNotificationService struct {
	err      error
	replayed bool // what Create reports
	calls    int
	got      domain.Notification
	gotBatch []domain.Notification
	gotKey   *string // batch idempotency key
	gotList  *domain.ListFilter
}

var (
	fakeID = uuid.MustParse("7c9e6679-7425-40de-944b-e07fc1f90ae7")
	// Istanbul time (UTC+3), to check that responses are converted to UTC.
	fakeCreatedAt = time.Date(2026, 11, 20, 13, 0, 0, 0, time.FixedZone("TRT", 3*60*60))
)

func (f *fakeNotificationService) Create(_ context.Context, n domain.Notification) (domain.Notification, bool, error) {
	f.calls++
	f.got = n
	if f.err != nil {
		return domain.Notification{}, false, f.err
	}
	n.ID, n.Status, n.CreatedAt, n.UpdatedAt = fakeID, domain.StatusPending, fakeCreatedAt, fakeCreatedAt
	return n, f.replayed, nil
}

var fakeBatchID = uuid.MustParse("3f2c9a1e-7b4d-4c1a-9e8f-1a2b3c4d5e6f")

// CreateBatch gives every notification a new ID under fakeBatchID.
func (f *fakeNotificationService) CreateBatch(
	_ context.Context, ns []domain.Notification, key *string,
) (domain.Batch, bool, error) {
	f.calls++
	f.gotBatch, f.gotKey = ns, key
	if f.err != nil {
		return domain.Batch{}, false, f.err
	}
	created := make([]domain.Notification, len(ns))
	for i, n := range ns {
		n.ID, n.BatchID, n.Status = uuid.New(), &fakeBatchID, domain.StatusPending
		created[i] = n
	}
	return domain.Batch{ID: fakeBatchID, Notifications: created}, f.replayed, nil
}

// GetBatch knows only fakeBatchID: 10 notifications, 3 still pending.
func (f *fakeNotificationService) GetBatch(_ context.Context, id uuid.UUID) (domain.BatchSummary, error) {
	f.calls++
	if f.err != nil {
		return domain.BatchSummary{}, f.err
	}
	if id != fakeBatchID {
		return domain.BatchSummary{}, fmt.Errorf("batch summary %s: %w", id, domain.ErrNotFound)
	}
	return domain.BatchSummary{
		ID: fakeBatchID, Total: 10, CreatedAt: fakeCreatedAt,
		Counts: map[domain.Status]int{domain.StatusPending: 3, domain.StatusSent: 6, domain.StatusFailed: 1},
	}, nil
}

// fakeCursor is the position List reports after its first page.
var fakeCursor = domain.Cursor{CreatedAt: fakeCreatedAt.UTC(), ID: fakeID} // cursors carry UTC

// List returns one notification; a first page (no cursor) also has a next page.
func (f *fakeNotificationService) List(_ context.Context, filter domain.ListFilter) (domain.Page, error) {
	f.calls++
	f.gotList = &filter
	if f.err != nil {
		return domain.Page{}, f.err
	}
	n, _ := f.Get(context.Background(), fakeID) // can't fail for fakeID
	page := domain.Page{Items: []domain.Notification{n}}
	if filter.After == nil {
		page.Next = &fakeCursor
	}
	return page, nil
}

// CancelBatch knows only fakeBatchID: it cancels its 3 pending notifications.
func (f *fakeNotificationService) CancelBatch(ctx context.Context, id uuid.UUID) (domain.BatchCancellation, error) {
	summary, err := f.GetBatch(ctx, id) // counts calls and handles err and unknown IDs
	if err != nil {
		return domain.BatchCancellation{}, err
	}
	summary.Counts[domain.StatusCancelled] += summary.Counts[domain.StatusPending]
	summary.Counts[domain.StatusPending] = 0
	return domain.BatchCancellation{Cancelled: 3, Summary: summary}, nil
}

// Cancel knows only fakeID and cancels it.
func (f *fakeNotificationService) Cancel(_ context.Context, id uuid.UUID) (domain.Notification, error) {
	f.calls++
	if f.err != nil {
		return domain.Notification{}, f.err
	}
	if id != fakeID {
		return domain.Notification{}, fmt.Errorf("get notification %s: %w", id, domain.ErrNotFound)
	}
	n, _ := f.Get(context.Background(), fakeID) // can't fail for fakeID
	n.Status = domain.StatusCancelled
	return n, nil
}

// Get knows only fakeID, as a sent notification.
func (f *fakeNotificationService) Get(_ context.Context, id uuid.UUID) (domain.Notification, error) {
	f.calls++
	if f.err != nil {
		return domain.Notification{}, f.err
	}
	if id != fakeID {
		return domain.Notification{}, fmt.Errorf("get notification %s: %w", id, domain.ErrNotFound)
	}
	return domain.Notification{
		ID: fakeID, Channel: domain.ChannelEmail, Recipient: "user@example.com", Content: "hi",
		Priority: domain.PriorityLow, Status: domain.StatusSent, Attempts: 1,
		ProviderMessageID: ptr("msg-42"), CreatedAt: fakeCreatedAt, UpdatedAt: fakeCreatedAt,
	}, nil
}

func ptr[T any](v T) *T { return &v }

// postNotification sends body to POST /api/v1/notifications through the full router.
// headers are optional name/value pairs.
func postNotification(t *testing.T, svc NotificationService, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/notifications", strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc, nil).ServeHTTP(rec, req)
	return rec
}

func TestCreateNotification_Accepted(t *testing.T) {
	svc := &fakeNotificationService{}
	rec := postNotification(t, svc,
		`{"channel":"sms","recipient":"+905551234567","content":"hello","priority":"high"}`,
		"X-Correlation-ID", "shop-req-42")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/notifications/"+fakeID.String() {
		t.Errorf("Location = %q", loc)
	}
	if h := rec.Header().Get(headerIdempotentReplayed); h != "" {
		t.Errorf("%s = %q on a first request, want no header", headerIdempotentReplayed, h)
	}

	want := domain.Notification{
		CorrelationID: ptr("shop-req-42"), // stored so the worker can log with it
		Channel:       domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityHigh,
	}
	if !reflect.DeepEqual(svc.got, want) { // DeepEqual compares what pointers point to
		t.Errorf("service got %+v, want %+v", svc.got, want)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	for field, wantValue := range map[string]any{
		"id":             fakeID.String(),
		"channel":        "sms",
		"recipient":      "+905551234567",
		"content":        "hello",
		"priority":       "high",
		"status":         "pending",
		"attempts":       float64(0), // JSON numbers decode as float64
		"correlation_id": "shop-req-42",
		"created_at":     "2026-11-20T10:00:00Z",
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

// scheduled_at reaches the service as the same instant, and comes back in UTC.
func TestCreateNotification_Scheduled(t *testing.T) {
	svc := &fakeNotificationService{}
	rec := postNotification(t, svc,
		`{"channel":"sms","recipient":"+905551234567","content":"hi","scheduled_at":"2026-11-21T12:00:00+03:00"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body)
	}
	want := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	if svc.got.ScheduledAt == nil || !svc.got.ScheduledAt.Equal(want) {
		t.Fatalf("service got scheduled_at %v, want %v", svc.got.ScheduledAt, want)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["scheduled_at"] != "2026-11-21T09:00:00Z" {
		t.Errorf("scheduled_at = %v, want 2026-11-21T09:00:00Z", body["scheduled_at"])
	}

	// Without it, the field is absent from the response.
	rec = postNotification(t, &fakeNotificationService{}, `{"channel":"sms","recipient":"+905551234567","content":"hi"}`)
	if strings.Contains(rec.Body.String(), "scheduled_at") {
		t.Errorf("unscheduled response contains scheduled_at: %s", rec.Body)
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

func TestCreateNotification_IdempotencyKey(t *testing.T) {
	const body = `{"channel":"sms","recipient":"+905551234567","content":"hi"}`

	tests := []struct {
		name    string
		headers []string
		want    *string // key the service receives
	}{
		{"no header", nil, nil},
		{"key is passed on", []string{"Idempotency-Key", "order-1042"}, ptr("order-1042")},
		{"empty header is kept for validation", []string{"Idempotency-Key", ""}, ptr("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{}
			postNotification(t, svc, body, tt.headers...)

			got := svc.got.IdempotencyKey
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("service got key %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateNotification_Replayed(t *testing.T) {
	svc := &fakeNotificationService{replayed: true}
	rec := postNotification(t, svc, `{"channel":"sms","recipient":"+905551234567","content":"hi"}`,
		"Idempotency-Key", "order-1042")

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202 (same as the first attempt)", rec.Code)
	}
	if h := rec.Header().Get(headerIdempotentReplayed); h != "true" {
		t.Errorf("%s = %q, want \"true\"", headerIdempotentReplayed, h)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/notifications/"+fakeID.String() {
		t.Errorf("Location = %q", loc)
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
		{"scheduled_at without a time zone", `{"scheduled_at":"2026-11-21T09:00:00"}`, nil,
			http.StatusBadRequest, codeInvalidBody, `field "scheduled_at" must be an RFC 3339 time`},
		{"scheduled_at as a number", `{"scheduled_at":1763715600}`, nil,
			http.StatusBadRequest, codeInvalidBody, `field "scheduled_at" must be an RFC 3339 time`},
		{"too large", `{"content":"` + strings.Repeat("a", maxBodyBytes) + `"}`, nil,
			http.StatusRequestEntityTooLarge, codeBodyTooLarge, "request body must be at most 1048576 bytes"},
		{"idempotency key reused", `{"channel":"sms"}`, domain.ErrIdempotencyKeyReused,
			http.StatusUnprocessableEntity, codeKeyReused, "this Idempotency-Key was already used"},
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

// getNotification sends GET /api/v1/notifications/{id} through the full router.
func getNotification(t *testing.T, svc NotificationService, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/notifications/"+id, nil)
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc, nil).ServeHTTP(rec, req)
	return rec
}

func TestGetNotification_Found(t *testing.T) {
	rec := getNotification(t, &fakeNotificationService{}, fakeID.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	want := `{"id":"` + fakeID.String() + `","channel":"email","recipient":"user@example.com","content":"hi",` +
		`"priority":"low","status":"sent","attempts":1,"provider_message_id":"msg-42",` +
		`"created_at":"2026-11-20T10:00:00Z","updated_at":"2026-11-20T10:00:00Z"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

// next_attempt_at is shown only while a retry is waiting, not for a new or finished notification.
func TestNotificationResponse_NextAttemptOnlyWhileRetrying(t *testing.T) {
	due := time.Date(2026, 11, 20, 10, 0, 10, 0, time.UTC)
	tests := []struct {
		name     string
		status   domain.Status
		attempts int
		want     bool
	}{
		{"new, never tried", domain.StatusPending, 0, false},
		{"waiting for a retry", domain.StatusPending, 1, true},
		{"sent", domain.StatusSent, 1, false},
		{"failed", domain.StatusFailed, 5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toNotificationResponse(domain.Notification{Status: tt.status, Attempts: tt.attempts, NextAttemptAt: due})
			if (got.NextAttemptAt != nil) != tt.want || (tt.want && !got.NextAttemptAt.Equal(due)) {
				t.Errorf("next_attempt_at = %v, want shown: %v", got.NextAttemptAt, tt.want)
			}
		})
	}
}

func TestGetNotification_Errors(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		svcErr     error
		wantStatus int
		wantCode   string
		wantCalled bool
	}{
		{"unknown id", uuid.NewString(), nil, http.StatusNotFound, codeNotFound, true},
		{"malformed id", "not-a-uuid", nil, http.StatusBadRequest, codeInvalidID, false},
		{"service failure is hidden", fakeID.String(), errors.New("connection refused"),
			http.StatusInternalServerError, codeInternal, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{err: tt.svcErr}
			rec := getNotification(t, svc, tt.id)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Error.Code != tt.wantCode {
				t.Errorf("body = %s, want code %q", rec.Body, tt.wantCode)
			}
			if called := svc.calls > 0; called != tt.wantCalled {
				t.Errorf("service called = %v, want %v", called, tt.wantCalled)
			}
		})
	}
}

// postBatch sends body to POST /api/v1/notifications/batch through the full router.
func postBatch(t *testing.T, svc NotificationService, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/notifications/batch", strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc, nil).ServeHTTP(rec, req)
	return rec
}

func TestCreateNotificationBatch_Accepted(t *testing.T) {
	svc := &fakeNotificationService{}
	rec := postBatch(t, svc, `{"notifications":[
		{"channel":"sms","recipient":"+905551234567","content":"one","priority":"high"},
		{"channel":"email","recipient":"user@example.com","content":"two"}
	]}`, "X-Correlation-ID", "campaign-7")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/batches/"+fakeBatchID.String() {
		t.Errorf("Location = %q", loc)
	}

	// Items reach the service in order, with the priority default and the correlation ID.
	want := []domain.Notification{
		{CorrelationID: ptr("campaign-7"), Channel: domain.ChannelSMS, Recipient: "+905551234567",
			Content: "one", Priority: domain.PriorityHigh},
		{CorrelationID: ptr("campaign-7"), Channel: domain.ChannelEmail, Recipient: "user@example.com",
			Content: "two", Priority: domain.PriorityNormal},
	}
	if !reflect.DeepEqual(svc.gotBatch, want) {
		t.Errorf("service got %+v\nwant %+v", svc.gotBatch, want)
	}

	var body batchCreatedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.BatchID != fakeBatchID || body.Count != 2 || len(body.IDs) != 2 || body.IDs[0] == body.IDs[1] {
		t.Errorf("body = %+v, want batch %s, count 2 and two different ids", body, fakeBatchID)
	}
}

func TestCreateNotificationBatch_Errors(t *testing.T) {
	invalid := &domain.ValidationError{Fields: []domain.FieldError{{Field: "notifications[1].recipient", Message: "is required"}}}

	tests := []struct {
		name       string
		body       string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{"invalid item", `{"notifications":[{}]}`, invalid, http.StatusBadRequest, codeValidationFailed},
		{"unknown field inside an item", `{"notifications":[{"recipent":"x"}]}`, nil, http.StatusBadRequest, codeInvalidBody},
		{"bare array instead of an object", `[{}]`, nil, http.StatusBadRequest, codeInvalidBody},
		{"larger than the batch limit", `{"notifications":[{"content":"` + strings.Repeat("a", maxBatchBodyBytes) + `"}]}`,
			nil, http.StatusRequestEntityTooLarge, codeBodyTooLarge},
		{"idempotency key reused", `{"notifications":[{}]}`, domain.ErrIdempotencyKeyReused,
			http.StatusUnprocessableEntity, codeKeyReused},
		{"service failure is hidden", `{"notifications":[{}]}`, errors.New("boom"), http.StatusInternalServerError, codeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{err: tt.svcErr}
			rec := postBatch(t, svc, tt.body)

			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not the error format: %v\n%s", err, rec.Body)
			}
			if rec.Code != tt.wantStatus || got.Error.Code != tt.wantCode {
				t.Errorf("got %d %q, want %d %q", rec.Code, got.Error.Code, tt.wantStatus, tt.wantCode)
			}
		})
	}
}

// A batch larger than the single-notification cap (1 MB) is still accepted.
func TestCreateNotificationBatch_AllowsBodiesOverOneMB(t *testing.T) {
	item := `{"channel":"email","recipient":"user@example.com","content":"` + strings.Repeat("a", 5000) + `"}`
	body := `{"notifications":[` + strings.TrimSuffix(strings.Repeat(item+",", 300), ",") + `]}` // about 1.5 MB

	svc := &fakeNotificationService{}
	if rec := postBatch(t, svc, body); rec.Code != http.StatusAccepted || len(svc.gotBatch) != 300 {
		t.Errorf("status %d with %d items, want 202 with 300", rec.Code, len(svc.gotBatch))
	}
}

func TestCreateNotificationBatch_IdempotencyKey(t *testing.T) {
	const body = `{"notifications":[{"channel":"sms","recipient":"+905551234567","content":"hi"}]}`

	svc := &fakeNotificationService{}
	rec := postBatch(t, svc, body)
	if svc.gotKey != nil || rec.Header().Get(headerIdempotentReplayed) != "" {
		t.Errorf("without header: key %v, replayed header %q; want neither", svc.gotKey, rec.Header().Get(headerIdempotentReplayed))
	}

	svc = &fakeNotificationService{replayed: true}
	rec = postBatch(t, svc, body, "Idempotency-Key", "campaign-7")
	if svc.gotKey == nil || *svc.gotKey != "campaign-7" {
		t.Errorf("service got key %v, want campaign-7", svc.gotKey)
	}
	if rec.Code != http.StatusAccepted || rec.Header().Get(headerIdempotentReplayed) != "true" {
		t.Errorf("replay: status %d, %s = %q; want 202 and true",
			rec.Code, headerIdempotentReplayed, rec.Header().Get(headerIdempotentReplayed))
	}
	// Items themselves carry no key from the handler; the service derives item keys.
	if svc.gotBatch[0].IdempotencyKey != nil {
		t.Errorf("item key = %v, want nil", svc.gotBatch[0].IdempotencyKey)
	}
}

func getBatch(t *testing.T, svc NotificationService, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/batches/"+id, nil)
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc, nil).ServeHTTP(rec, req)
	return rec
}

func TestGetBatch_Found(t *testing.T) {
	rec := getBatch(t, &fakeNotificationService{}, fakeBatchID.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	// Statuses with no notifications are still listed, as 0.
	want := `{"batch_id":"` + fakeBatchID.String() + `","total":10,` +
		`"counts":{"pending":3,"processing":0,"sent":6,"failed":1,"cancelled":0},` +
		`"created_at":"2026-11-20T10:00:00Z"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
}

func TestGetBatch_Errors(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		wantStatus int
		wantCode   string
	}{
		{"unknown batch", uuid.NewString(), http.StatusNotFound, codeNotFound},
		{"malformed id", "not-a-uuid", http.StatusBadRequest, codeInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := getBatch(t, &fakeNotificationService{}, tt.id)
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != tt.wantStatus || got.Error.Code != tt.wantCode {
				t.Errorf("got %d %s, want %d %q", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
			}
		})
	}
}

func cancelNotification(t *testing.T, svc NotificationService, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/notifications/"+id+"/cancel", nil)
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc, nil).ServeHTTP(rec, req)
	return rec
}

func TestCancelNotification(t *testing.T) {
	rec := cancelNotification(t, &fakeNotificationService{}, fakeID.String())

	var got notificationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v\n%s", err, rec.Body)
	}
	if rec.Code != http.StatusOK || got.ID != fakeID || got.Status != "cancelled" {
		t.Errorf("got %d, id %s, status %s; want 200, %s, cancelled", rec.Code, got.ID, got.Status, fakeID)
	}
}

func TestCancelNotification_Errors(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{"too late", fakeID.String(), &domain.NotCancellableError{Status: domain.StatusSent},
			http.StatusConflict, codeNotCancellable,
			"the notification is sent and can no longer be cancelled; only pending notifications can"},
		{"unknown", uuid.NewString(), nil, http.StatusNotFound, codeNotFound, "the requested item was not found"},
		{"malformed id", "abc", nil, http.StatusBadRequest, codeInvalidID, "id must be a UUID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := cancelNotification(t, &fakeNotificationService{err: tt.err}, tt.id)
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body: %v\n%s", err, rec.Body)
			}
			if rec.Code != tt.wantStatus || got.Error.Code != tt.wantCode || got.Error.Message != tt.wantMessage {
				t.Errorf("got %d %+v, want %d %q %q", rec.Code, got.Error, tt.wantStatus, tt.wantCode, tt.wantMessage)
			}
		})
	}
}

func TestCancelBatch(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/batches/"+fakeBatchID.String()+"/cancel", nil)
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, &fakeNotificationService{}, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
	want := `{"batch_id":"` + fakeBatchID.String() + `","total":10,` +
		`"counts":{"pending":0,"processing":0,"sent":6,"failed":1,"cancelled":3},` +
		`"created_at":"2026-11-20T10:00:00Z","cancelled":3}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}

	// Errors work as for batch status.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/batches/"+uuid.NewString()+"/cancel", nil)
	rec = httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, &fakeNotificationService{}, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown batch: status = %d, want 404", rec.Code)
	}
}
