package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// maxTemplates caps a template list; templates are configuration, so there are few.
const maxTemplates = 1000

// Templates stores message templates in the templates table.
type Templates struct {
	db *pgxpool.Pool
}

// NewTemplates returns a template repository that uses the given pool.
func NewTemplates(db *pgxpool.Pool) *Templates {
	return &Templates{db: db}
}

// Create inserts a new template and returns it as stored.
// If the name is taken, nothing is saved and domain.ErrTemplateExists is returned.
func (r *Templates) Create(ctx context.Context, t domain.Template) (domain.Template, error) {
	// ON CONFLICT lets the primary key decide atomically which of two same-name requests wins.
	err := r.db.QueryRow(ctx, `
		INSERT INTO templates (name, body) VALUES ($1, $2)
		ON CONFLICT (name) DO NOTHING
		RETURNING name, body, created_at`, t.Name, t.Body,
	).Scan(&t.Name, &t.Body, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Template{}, domain.ErrTemplateExists
	}
	if err != nil {
		return domain.Template{}, fmt.Errorf("insert template: %w", err)
	}
	return t, nil
}

// Get returns the template with the given name, or an error wrapping domain.ErrNotFound.
func (r *Templates) Get(ctx context.Context, name string) (domain.Template, error) {
	var t domain.Template
	err := r.db.QueryRow(ctx, `SELECT name, body, created_at FROM templates WHERE name = $1`, name).
		Scan(&t.Name, &t.Body, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Template{}, fmt.Errorf("get template %q: %w", name, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Template{}, fmt.Errorf("get template %q: %w", name, err)
	}
	return t, nil
}

// List returns the templates ordered by name, at most maxTemplates.
func (r *Templates) List(ctx context.Context) ([]domain.Template, error) {
	rows, err := r.db.Query(ctx, `SELECT name, body, created_at FROM templates ORDER BY name LIMIT $1`, maxTemplates)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	ts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Template, error) {
		var t domain.Template
		return t, row.Scan(&t.Name, &t.Body, &t.CreatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	return ts, nil
}
