package service

import (
	"context"
	"fmt"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// TemplateStore saves and loads templates.
type TemplateStore interface {
	Create(ctx context.Context, t domain.Template) (domain.Template, error)
	Get(ctx context.Context, name string) (domain.Template, error)
	List(ctx context.Context) ([]domain.Template, error)
}

// Templates implements the template use cases.
type Templates struct {
	store TemplateStore
}

// NewTemplates returns the template use cases backed by store.
func NewTemplates(store TemplateStore) *Templates {
	return &Templates{store: store}
}

// Create validates t and saves it. Invalid input returns a *domain.ValidationError;
// a taken name returns domain.ErrTemplateExists.
func (s *Templates) Create(ctx context.Context, t domain.Template) (domain.Template, error) {
	if err := t.Validate(); err != nil {
		return domain.Template{}, err
	}
	created, err := s.store.Create(ctx, t)
	if err != nil {
		return domain.Template{}, fmt.Errorf("create template: %w", err)
	}
	return created, nil
}

// Get returns the template with the given name, or an error wrapping domain.ErrNotFound.
func (s *Templates) Get(ctx context.Context, name string) (domain.Template, error) {
	return s.store.Get(ctx, name) // the repository already wraps the error with the name
}

// List returns every template, ordered by name.
func (s *Templates) List(ctx context.Context) ([]domain.Template, error) {
	return s.store.List(ctx)
}
