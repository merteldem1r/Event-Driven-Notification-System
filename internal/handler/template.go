package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// TemplateService is what the template handlers need from the service layer.
type TemplateService interface {
	Create(ctx context.Context, t domain.Template) (domain.Template, error)
	Get(ctx context.Context, name string) (domain.Template, error)
	List(ctx context.Context) ([]domain.Template, error)
}

type createTemplateRequest struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// templateResponse is how a template looks in the API.
type templateResponse struct {
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	Variables []string  `json:"variables"` // the placeholders a notification must fill in
	CreatedAt time.Time `json:"created_at"`
}

type templateListResponse struct {
	Items []templateResponse `json:"items"`
}

func toTemplateResponse(t domain.Template) templateResponse {
	vars := t.Variables()
	if vars == nil {
		vars = []string{} // [] in JSON, not null
	}
	return templateResponse{Name: t.Name, Body: t.Body, Variables: vars, CreatedAt: t.CreatedAt.UTC()}
}

// CreateTemplate handles POST /api/v1/templates. Unlike notifications, a template is saved
// before answering, so the answer is 201 Created.
func CreateTemplate(svc TemplateService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createTemplateRequest
		if !decodeJSON(w, r, &req, maxBodyBytes) {
			return
		}
		created, err := svc.Create(r.Context(), domain.Template{Name: req.Name, Body: req.Body})
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		logger.InfoContext(r.Context(), "template created", "template", created.Name)
		w.Header().Set("Location", "/api/v1/templates/"+created.Name)
		writeJSON(w, http.StatusCreated, toTemplateResponse(created))
	}
}

// GetTemplate handles GET /api/v1/templates/{name}.
func GetTemplate(svc TemplateService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, err := svc.Get(r.Context(), chi.URLParam(r, "name"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, toTemplateResponse(t))
	}
}

// ListTemplates handles GET /api/v1/templates.
func ListTemplates(svc TemplateService, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ts, err := svc.List(r.Context())
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		items := make([]templateResponse, len(ts))
		for i, t := range ts {
			items[i] = toTemplateResponse(t)
		}
		writeJSON(w, http.StatusOK, templateListResponse{Items: items})
	}
}
