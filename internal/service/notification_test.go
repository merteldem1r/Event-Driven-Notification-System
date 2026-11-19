package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// fakeStore stands in for the repository and records what it was asked to save.
type fakeStore struct {
	err   error
	saved []domain.Notification
}

func (f *fakeStore) Create(_ context.Context, n domain.Notification) (domain.Notification, error) {
	if f.err != nil {
		return domain.Notification{}, f.err
	}
	f.saved = append(f.saved, n)
	n.ID, n.Status = uuid.New(), domain.StatusPending
	return n, nil
}

func validNotification() domain.Notification {
	return domain.Notification{
		Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityNormal,
	}
}

func TestCreate_SavesValidNotification(t *testing.T) {
	store := &fakeStore{}

	created, err := NewNotifications(store).Create(t.Context(), validNotification())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d notifications, want 1", len(store.saved))
	}
	if created.ID == uuid.Nil || created.Status != domain.StatusPending {
		t.Errorf("Create returned %+v, want the stored notification", created)
	}
}

func TestCreate_RejectsInvalidWithoutSaving(t *testing.T) {
	store := &fakeStore{}
	n := validNotification()
	n.Recipient = ""

	_, err := NewNotifications(store).Create(t.Context(), n)

	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *domain.ValidationError", err)
	}
	if len(store.saved) != 0 {
		t.Errorf("saved %d notifications, want 0", len(store.saved))
	}
}

func TestCreate_WrapsStoreError(t *testing.T) {
	storeErr := errors.New("connection refused")

	_, err := NewNotifications(&fakeStore{err: storeErr}).Create(t.Context(), validNotification())
	if !errors.Is(err, storeErr) {
		t.Errorf("err = %v, want it to wrap %v", err, storeErr)
	}
}
