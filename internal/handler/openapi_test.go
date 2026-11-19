package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// These tests keep docs/openapi.yaml honest: it must be valid, list exactly the routes the
// router has, and describe the responses the handlers really send.

const specPath = "../../docs/openapi.yaml"

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load %s: %v", specPath, err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatalf("%s is not valid OpenAPI: %v", specPath, err)
	}
	return doc
}

func TestOpenAPI_DocumentsExactlyTheRoutes(t *testing.T) {
	doc := loadSpec(t)

	inSpec := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			inSpec[method+" "+path] = true
		}
	}

	inRouter := map[string]bool{}
	router := NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, &fakeNotificationService{})
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		inRouter[method+" "+route] = true
		return nil
	}
	if err := chi.Walk(router.(chi.Routes), walk); err != nil {
		t.Fatalf("walk routes: %v", err)
	}

	for route := range inRouter {
		if !inSpec[route] {
			t.Errorf("route %s is missing from %s", route, specPath)
		}
	}
	for op := range inSpec {
		if !inRouter[op] {
			t.Errorf("%s documents %s, but the router has no such route", specPath, op)
		}
	}
}

func TestOpenAPI_ResponsesMatchSpec(t *testing.T) {
	doc := loadSpec(t)
	specRouter, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("spec router: %v", err)
	}

	const validBody = `{"channel":"sms","recipient":"+905551234567","content":"hi","priority":"high"}`
	invalid := &domain.ValidationError{Fields: []domain.FieldError{{Field: "recipient", Message: "is required"}}}

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		svc     *fakeNotificationService
		headers []string
	}{
		{"create: accepted", http.MethodPost, "/api/v1/notifications", validBody, &fakeNotificationService{}, nil},
		{"create: replayed", http.MethodPost, "/api/v1/notifications", validBody,
			&fakeNotificationService{replayed: true}, []string{"Idempotency-Key", "order-1042"}},
		{"create: validation failed", http.MethodPost, "/api/v1/notifications", validBody,
			&fakeNotificationService{err: invalid}, nil},
		{"create: invalid body", http.MethodPost, "/api/v1/notifications", `{"channel":`, &fakeNotificationService{}, nil},
		{"create: key reused", http.MethodPost, "/api/v1/notifications", validBody,
			&fakeNotificationService{err: domain.ErrIdempotencyKeyReused}, nil},
		{"create: internal error", http.MethodPost, "/api/v1/notifications", validBody,
			&fakeNotificationService{err: errors.New("boom")}, nil},
		{"batch: accepted", http.MethodPost, "/api/v1/notifications/batch",
			`{"notifications":[` + validBody + `,` + validBody + `]}`, &fakeNotificationService{}, nil},
		{"batch: replayed", http.MethodPost, "/api/v1/notifications/batch", `{"notifications":[` + validBody + `]}`,
			&fakeNotificationService{replayed: true}, []string{"Idempotency-Key", "campaign-7"}},
		{"batch: key reused", http.MethodPost, "/api/v1/notifications/batch", `{"notifications":[` + validBody + `]}`,
			&fakeNotificationService{err: domain.ErrIdempotencyKeyReused}, nil},
		{"batch: validation failed", http.MethodPost, "/api/v1/notifications/batch",
			`{"notifications":[` + validBody + `]}`, &fakeNotificationService{err: invalid}, nil},
		{"list: first page", http.MethodGet, "/api/v1/notifications?status=failed&limit=10", "", &fakeNotificationService{}, nil},
		{"list: last page", http.MethodGet, "/api/v1/notifications?cursor=" + encodeCursor(fakeCursor), "",
			&fakeNotificationService{}, nil},
		{"list: bad parameter", http.MethodGet, "/api/v1/notifications?limit=ten", "", &fakeNotificationService{}, nil},
		{"list: invalid filter", http.MethodGet, "/api/v1/notifications", "", &fakeNotificationService{err: invalid}, nil},
		{"get: found", http.MethodGet, "/api/v1/notifications/" + fakeID.String(), "", &fakeNotificationService{}, nil},
		{"get: not found", http.MethodGet, "/api/v1/notifications/" + uuid.NewString(), "", &fakeNotificationService{}, nil},
		{"get: invalid id", http.MethodGet, "/api/v1/notifications/abc", "", &fakeNotificationService{}, nil},
		{"cancel: cancelled", http.MethodPost, "/api/v1/notifications/" + fakeID.String() + "/cancel", "",
			&fakeNotificationService{}, nil},
		{"cancel: too late", http.MethodPost, "/api/v1/notifications/" + fakeID.String() + "/cancel", "",
			&fakeNotificationService{err: &domain.NotCancellableError{Status: domain.StatusSent}}, nil},
		{"cancel: not found", http.MethodPost, "/api/v1/notifications/" + uuid.NewString() + "/cancel", "",
			&fakeNotificationService{}, nil},
		{"cancel: invalid id", http.MethodPost, "/api/v1/notifications/abc/cancel", "", &fakeNotificationService{}, nil},
		{"batch status: found", http.MethodGet, "/api/v1/batches/" + fakeBatchID.String(), "", &fakeNotificationService{}, nil},
		{"batch status: not found", http.MethodGet, "/api/v1/batches/" + uuid.NewString(), "", &fakeNotificationService{}, nil},
		{"batch status: invalid id", http.MethodGet, "/api/v1/batches/abc", "", &fakeNotificationService{}, nil},
		{"batch cancel: cancelled", http.MethodPost, "/api/v1/batches/" + fakeBatchID.String() + "/cancel", "",
			&fakeNotificationService{}, nil},
		{"batch cancel: not found", http.MethodPost, "/api/v1/batches/" + uuid.NewString() + "/cancel", "",
			&fakeNotificationService{}, nil},
		{"batch cancel: invalid id", http.MethodPost, "/api/v1/batches/abc/cancel", "", &fakeNotificationService{}, nil},
		{"health: ok", http.MethodGet, "/health", "", &fakeNotificationService{}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The spec's server is http://localhost:8080, so requests use that host.
			req := httptest.NewRequestWithContext(t.Context(), tt.method, "http://localhost:8080"+tt.path,
				strings.NewReader(tt.body))
			for i := 0; i+1 < len(tt.headers); i += 2 {
				req.Header.Set(tt.headers[i], tt.headers[i+1])
			}
			rec := httptest.NewRecorder()
			NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, tt.svc).ServeHTTP(rec, req)

			route, params, err := specRouter.FindRoute(req)
			if err != nil {
				t.Fatalf("request not in the spec: %v", err)
			}
			input := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
				Status:                 rec.Code,
				Header:                 rec.Header(),
				Options:                &openapi3filter.Options{IncludeResponseStatus: true}, // undocumented status = failure
			}
			input.SetBodyBytes(rec.Body.Bytes())
			if err := openapi3filter.ValidateResponse(t.Context(), input); err != nil {
				t.Errorf("status %d does not match the spec: %v\nbody: %s", rec.Code, err, rec.Body)
			}
		})
	}
}
