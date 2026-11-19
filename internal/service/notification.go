// Package service holds the use cases of the notification service.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// NotificationStore saves and loads notifications.
type NotificationStore interface {
	Create(ctx context.Context, n domain.Notification) (domain.Notification, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Notification, error)
	GetByIdempotencyKey(ctx context.Context, key string) (domain.Notification, error)
}

// Notifications implements the notification use cases.
type Notifications struct {
	store NotificationStore
}

// NewNotifications returns the notification use cases backed by store.
func NewNotifications(store NotificationStore) *Notifications {
	return &Notifications{store: store}
}

// Create validates n and saves it. Saving also queues it for sending.
// If n's idempotency key was used before for the same request, it returns the stored
// notification with replayed = true instead of saving a duplicate.
// Invalid input returns a *domain.ValidationError; a key reused for a different request
// returns domain.ErrIdempotencyKeyReused.
func (s *Notifications) Create(ctx context.Context, n domain.Notification) (_ domain.Notification, replayed bool, _ error) {
	if err := n.Validate(); err != nil {
		return domain.Notification{}, false, err
	}

	created, err := s.store.Create(ctx, n)
	if errors.Is(err, domain.ErrIdempotencyKeyExists) {
		return s.replay(ctx, n)
	}
	if err != nil {
		return domain.Notification{}, false, fmt.Errorf("create notification: %w", err)
	}
	return created, false, nil
}

// replay returns the notification already stored under n's idempotency key,
// if it was created by the same request.
func (s *Notifications) replay(ctx context.Context, n domain.Notification) (domain.Notification, bool, error) {
	existing, err := s.store.GetByIdempotencyKey(ctx, *n.IdempotencyKey)
	if err != nil {
		return domain.Notification{}, false, fmt.Errorf("load notification for idempotency key: %w", err)
	}
	if !existing.SameRequest(n) {
		return domain.Notification{}, false, domain.ErrIdempotencyKeyReused
	}
	return existing, true, nil
}

// Get returns the notification with the given ID, or an error wrapping domain.ErrNotFound.
func (s *Notifications) Get(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	return s.store.GetByID(ctx, id) // the repository already wraps the error with the ID
}
