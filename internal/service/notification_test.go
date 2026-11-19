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
	if n.IdempotencyKey != nil { // behaves like the unique index
		if _, err := f.GetByIdempotencyKey(context.Background(), *n.IdempotencyKey); err == nil {
			return domain.Notification{}, domain.ErrIdempotencyKeyExists
		}
	}
	n.ID, n.Status = uuid.New(), domain.StatusPending
	f.saved = append(f.saved, n)
	return n, nil
}

func (f *fakeStore) GetByID(_ context.Context, id uuid.UUID) (domain.Notification, error) {
	for _, n := range f.saved {
		if n.ID == id {
			return n, nil
		}
	}
	return domain.Notification{}, domain.ErrNotFound
}

func (f *fakeStore) GetByIdempotencyKey(_ context.Context, key string) (domain.Notification, error) {
	for _, n := range f.saved {
		if n.IdempotencyKey != nil && *n.IdempotencyKey == key {
			return n, nil
		}
	}
	return domain.Notification{}, domain.ErrNotFound
}

func validNotification() domain.Notification {
	return domain.Notification{
		Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityNormal,
	}
}

func TestCreate_SavesValidNotification(t *testing.T) {
	store := &fakeStore{}

	created, replayed, err := NewNotifications(store).Create(t.Context(), validNotification())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if replayed {
		t.Error("replayed = true for a new notification")
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

	_, _, err := NewNotifications(store).Create(t.Context(), n)

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

	_, _, err := NewNotifications(&fakeStore{err: storeErr}).Create(t.Context(), validNotification())
	if !errors.Is(err, storeErr) {
		t.Errorf("err = %v, want it to wrap %v", err, storeErr)
	}
}

func TestGet(t *testing.T) {
	svc := NewNotifications(&fakeStore{})
	created, _, err := svc.Create(t.Context(), validNotification())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(t.Context(), created.ID)
	if err != nil || got != created {
		t.Errorf("Get = %+v, %v; want %+v", got, err, created)
	}

	if _, err := svc.Get(t.Context(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(unknown) err = %v, want domain.ErrNotFound", err)
	}
}

func withKey(key string) domain.Notification {
	n := validNotification()
	n.IdempotencyKey = &key
	return n
}

func TestCreate_IdempotencyKeyReplay(t *testing.T) {
	store := &fakeStore{}
	svc := NewNotifications(store)

	first, replayed, err := svc.Create(t.Context(), withKey("order-1042"))
	if err != nil || replayed {
		t.Fatalf("first Create = replayed %v, err %v; want a new notification", replayed, err)
	}

	// The retry gets the original back, and nothing new is saved.
	again, replayed, err := svc.Create(t.Context(), withKey("order-1042"))
	if err != nil {
		t.Fatalf("retry Create: %v", err)
	}
	if !replayed || again.ID != first.ID {
		t.Errorf("retry = id %s, replayed %v; want id %s, replayed true", again.ID, replayed, first.ID)
	}
	if len(store.saved) != 1 {
		t.Errorf("saved %d notifications, want 1", len(store.saved))
	}
}

func TestCreate_IdempotencyKeyReusedForDifferentRequest(t *testing.T) {
	svc := NewNotifications(&fakeStore{})
	if _, _, err := svc.Create(t.Context(), withKey("order-1042")); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	different := withKey("order-1042")
	different.Content = "a different message"
	if _, _, err := svc.Create(t.Context(), different); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Errorf("err = %v, want domain.ErrIdempotencyKeyReused", err)
	}
}

func TestCreate_DifferentKeysAreDifferentNotifications(t *testing.T) {
	store := &fakeStore{}
	svc := NewNotifications(store)
	for _, key := range []string{"order-1042", "order-1043"} {
		if _, replayed, err := svc.Create(t.Context(), withKey(key)); err != nil || replayed {
			t.Fatalf("Create(%s) = replayed %v, err %v", key, replayed, err)
		}
	}
	if len(store.saved) != 2 {
		t.Errorf("saved %d notifications, want 2", len(store.saved))
	}
}
