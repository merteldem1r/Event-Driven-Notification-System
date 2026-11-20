package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

func listNotifications(t *testing.T, svc NotificationService, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/notifications"+query, nil)
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, svc, nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestListNotifications_ParsesFilter(t *testing.T) {
	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	batchID := uuid.MustParse("3f2c9a1e-7b4d-4c1a-9e8f-1a2b3c4d5e6f")

	tests := []struct {
		name  string
		query string
		want  domain.ListFilter
	}{
		{"no parameters: default limit", "", domain.ListFilter{Limit: domain.DefaultListLimit}},
		{"every parameter",
			"?status=failed&channel=sms&batch_id=" + batchID.String() +
				"&from=2026-11-20T00:00:00Z&to=2026-11-21T00:00:00Z&limit=10&cursor=" + encodeCursor(fakeCursor),
			domain.ListFilter{
				Status: ptr(domain.StatusFailed), Channel: ptr(domain.ChannelSMS), BatchID: &batchID,
				From: &from, To: &to, Limit: 10, After: &fakeCursor,
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{}
			if rec := listNotifications(t, svc, tt.query); rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", rec.Code, rec.Body)
			}
			if svc.gotList == nil || !reflect.DeepEqual(*svc.gotList, tt.want) {
				t.Errorf("service got %+v\nwant %+v", svc.gotList, tt.want)
			}
		})
	}
}

func TestListNotifications_Response(t *testing.T) {
	rec := listNotifications(t, &fakeNotificationService{}, "")

	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0]["id"] != fakeID.String() {
		t.Errorf("items = %v, want the one fake notification", body.Items)
	}
	if body.NextCursor == nil {
		t.Fatal("next_cursor missing on a page that has a next page")
	}
	// Sending the cursor back continues at the position it encodes.
	if got, err := decodeCursor(*body.NextCursor); err != nil || got != fakeCursor {
		t.Errorf("cursor decodes to %+v, %v; want %+v", got, err, fakeCursor)
	}

	// The last page has no next_cursor at all.
	rec = listNotifications(t, &fakeNotificationService{}, "?cursor="+*body.NextCursor)
	var last map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
		t.Fatalf("body: %v", err)
	}
	if _, ok := last["next_cursor"]; ok {
		t.Errorf("last page has next_cursor = %v, want none", last["next_cursor"])
	}
}

func TestListNotifications_BadParameters(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantFields []string
	}{
		{"bad batch id", "?batch_id=abc", []string{"batch_id"}},
		{"bad time", "?from=yesterday", []string{"from"}},
		{"limit not a number", "?limit=ten", []string{"limit"}},
		{"cursor not base64", "?cursor=%25%25", []string{"cursor"}},
		{"cursor not ours", "?cursor=e30", []string{"cursor"}}, // base64 of "{}"
		{"several at once", "?from=x&to=y&limit=z", []string{"from", "to", "limit"}},
		{"unparsable and invalid together", "?limit=500&status=lost&from=yesterday", []string{"from", "status", "limit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeNotificationService{}
			rec := listNotifications(t, svc, tt.query)

			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body: %v\n%s", err, rec.Body)
			}
			var fields []string
			for _, d := range got.Error.Details {
				fields = append(fields, d.Field)
			}
			if rec.Code != http.StatusBadRequest || got.Error.Code != codeValidationFailed || !slices.Equal(fields, tt.wantFields) {
				t.Errorf("got %d %s %v, want 400 validation_failed %v", rec.Code, got.Error.Code, fields, tt.wantFields)
			}
			if svc.calls != 0 {
				t.Error("service was called with unparsable parameters")
			}
		})
	}
}
