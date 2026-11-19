// Package logging adds request-scoped fields, such as the correlation ID, to log lines.
package logging

import (
	"context"
	"log/slog"
)

// ctxKey is unexported, so no other package can overwrite the value by accident.
type ctxKey struct{}

// WithCorrelationID returns a copy of ctx that carries the correlation ID.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// CorrelationID returns the correlation ID in ctx, or "" if there is none.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string) // missing value: "" is the right answer
	return id
}

// NewHandler wraps next so every record logged with a context gets that context's correlation_id.
// Callers then only need logger.InfoContext(ctx, ...); nobody has to add the ID by hand.
func NewHandler(next slog.Handler) slog.Handler {
	return contextHandler{next}
}

type contextHandler struct {
	slog.Handler
}

// Handle adds the correlation ID, if any, then passes the record on.
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs and WithGroup keep the wrapper; otherwise logger.With(...) would silently drop it.

// WithAttrs returns a wrapped handler with the given attributes.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

// WithGroup returns a wrapped handler that puts later attributes in the named group.
func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
