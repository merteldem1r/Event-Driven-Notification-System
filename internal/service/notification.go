// Package service holds the use cases of the notification service.
package service

import (
	"context"
	"fmt"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// NotificationStore saves and loads notifications.
type NotificationStore interface {
	Create(ctx context.Context, n domain.Notification) (domain.Notification, error)
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
// Invalid input returns a *domain.ValidationError.
func (s *Notifications) Create(ctx context.Context, n domain.Notification) (domain.Notification, error) {
	if err := n.Validate(); err != nil {
		return domain.Notification{}, err
	}

	created, err := s.store.Create(ctx, n)
	if err != nil {
		return domain.Notification{}, fmt.Errorf("create notification: %w", err)
	}
	return created, nil
}
