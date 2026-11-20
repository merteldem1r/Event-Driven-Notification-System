package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// fakeTemplateService knows one template, order_shipped, and returns err if set.
type fakeTemplateService struct {
	err error
	got domain.Template
}

var fakeTemplate = domain.Template{
	Name: "order_shipped", Body: "Hi {{first_name}}, order {{order_id}} has shipped.", CreatedAt: fakeCreatedAt,
}

func (f *fakeTemplateService) Create(_ context.Context, t domain.Template) (domain.Template, error) {
	f.got = t
	if f.err != nil {
		return domain.Template{}, f.err
	}
	t.CreatedAt = fakeCreatedAt
	return t, nil
}

func (f *fakeTemplateService) Get(_ context.Context, name string) (domain.Template, error) {
	if f.err != nil {
		return domain.Template{}, f.err
	}
	if name != fakeTemplate.Name {
		return domain.Template{}, fmt.Errorf("get template %q: %w", name, domain.ErrNotFound)
	}
	return fakeTemplate, nil
}

func (f *fakeTemplateService) List(context.Context) ([]domain.Template, error) {
	return []domain.Template{fakeTemplate, {Name: "welcome", Body: "Welcome!", CreatedAt: fakeCreatedAt}}, f.err
}

// templateRequest sends a request to the template endpoints through the full router.
func templateRequest(t *testing.T, svc TemplateService, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewRouter(slog.New(slog.DiscardHandler), fakePinger{}, nil, svc, nil).ServeHTTP(rec, req)
	return rec
}

func TestCreateTemplate(t *testing.T) {
	svc := &fakeTemplateService{}
	rec := templateRequest(t, svc, http.MethodPost, "/api/v1/templates",
		`{"name":"order_shipped","body":"Hi {{first_name}}, order {{order_id}} has shipped."}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/templates/order_shipped" {
		t.Errorf("Location = %q", loc)
	}
	if svc.got.Name != "order_shipped" || svc.got.Body != fakeTemplate.Body {
		t.Errorf("service got %+v", svc.got)
	}
	var body templateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body.Variables, []string{"first_name", "order_id"}) || body.CreatedAt.Location().String() != "UTC" {
		t.Errorf("response = %+v, want the variables and created_at in UTC", body)
	}
}

func TestTemplate_Errors(t *testing.T) {
	invalid := &domain.ValidationError{Fields: []domain.FieldError{{Field: "name", Message: "bad"}}}
	tests := []struct {
		name, method, path, body string
		svcErr                   error
		wantStatus               int
		wantCode                 string
	}{
		{"invalid", http.MethodPost, "/api/v1/templates", `{"name":"x","body":"y"}`, invalid,
			http.StatusBadRequest, codeValidationFailed},
		{"name taken", http.MethodPost, "/api/v1/templates", `{"name":"x","body":"y"}`, domain.ErrTemplateExists,
			http.StatusConflict, codeTemplateExists},
		{"unknown field", http.MethodPost, "/api/v1/templates", `{"name":"x","text":"y"}`, nil,
			http.StatusBadRequest, codeInvalidBody},
		{"not found", http.MethodGet, "/api/v1/templates/missing", "", nil, http.StatusNotFound, codeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := templateRequest(t, &fakeTemplateService{err: tt.svcErr}, tt.method, tt.path, tt.body)
			var got errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not the error format: %v\n%s", err, rec.Body)
			}
			if rec.Code != tt.wantStatus || got.Error.Code != tt.wantCode {
				t.Errorf("got %d %s, want %d %s", rec.Code, got.Error.Code, tt.wantStatus, tt.wantCode)
			}
		})
	}
}

func TestGetAndListTemplates(t *testing.T) {
	rec := templateRequest(t, &fakeTemplateService{}, http.MethodGet, "/api/v1/templates/order_shipped", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"variables":["first_name","order_id"]`) {
		t.Errorf("get = %d %s", rec.Code, rec.Body)
	}

	rec = templateRequest(t, &fakeTemplateService{}, http.MethodGet, "/api/v1/templates", "")
	var list templateListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if len(list.Items) != 2 || list.Items[1].Variables == nil {
		t.Errorf("list = %+v, want 2 items; no variables is [], not null", list.Items)
	}
}
