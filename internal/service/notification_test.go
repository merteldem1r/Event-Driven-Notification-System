package service

import (
	"context"
	"errors"
	"reflect"
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

func (f *fakeStore) CreateBatch(_ context.Context, ns []domain.Notification) ([]domain.Notification, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, n := range ns { // like the unique index plus rollback: one taken key, nothing saved
		if n.IdempotencyKey != nil {
			if _, err := f.GetByIdempotencyKey(context.Background(), *n.IdempotencyKey); err == nil {
				return nil, domain.ErrIdempotencyKeyExists
			}
		}
	}
	created := make([]domain.Notification, len(ns))
	for i, n := range ns {
		n.ID, n.Status = uuid.New(), domain.StatusPending
		f.saved = append(f.saved, n)
		created[i] = n
	}
	return created, nil
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

func (f *fakeStore) GetByIdempotencyKeys(ctx context.Context, keys []string) ([]domain.Notification, error) {
	var found []domain.Notification
	for _, k := range keys {
		if n, err := f.GetByIdempotencyKey(ctx, k); err == nil {
			found = append(found, n)
		}
	}
	return found, nil
}

func (f *fakeStore) BatchSummary(_ context.Context, batchID uuid.UUID) (domain.BatchSummary, error) {
	summary := domain.BatchSummary{ID: batchID, Counts: map[domain.Status]int{}}
	for _, n := range f.saved {
		if n.BatchID != nil && *n.BatchID == batchID {
			summary.Counts[n.Status]++
			summary.Total++
		}
	}
	if summary.Total == 0 {
		return domain.BatchSummary{}, domain.ErrNotFound
	}
	return summary, nil
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

func TestCreateBatch_SavesAllUnderOneBatchID(t *testing.T) {
	store := &fakeStore{}
	in := []domain.Notification{validNotification(), validNotification(), validNotification()}

	batch, replayed, err := NewNotifications(store).CreateBatch(t.Context(), in, nil)
	if err != nil || replayed {
		t.Fatalf("CreateBatch = replayed %v, err %v; want a new batch", replayed, err)
	}
	batchID, created := batch.ID, batch.Notifications
	if batchID == uuid.Nil || len(created) != 3 || len(store.saved) != 3 {
		t.Fatalf("batch %s, %d created, %d saved; want a batch ID and 3 of each", batchID, len(created), len(store.saved))
	}
	for i, n := range created {
		if n.BatchID == nil || *n.BatchID != batchID {
			t.Errorf("created[%d].BatchID = %v, want %s", i, n.BatchID, batchID)
		}
	}
	if in[0].BatchID != nil {
		t.Error("the caller's slice was modified")
	}
}

// D5: one invalid item, nothing saved.
func TestCreateBatch_AllOrNothing(t *testing.T) {
	store := &fakeStore{}
	bad := validNotification()
	bad.Recipient = ""

	_, _, err := NewNotifications(store).CreateBatch(t.Context(), []domain.Notification{validNotification(), bad}, nil)

	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Field != "notifications[1].recipient" {
		t.Fatalf("err = %v, want a validation error for notifications[1].recipient", err)
	}
	if len(store.saved) != 0 {
		t.Errorf("saved %d notifications, want 0", len(store.saved))
	}
}

func threeItems() []domain.Notification {
	return []domain.Notification{validNotification(), validNotification(), validNotification()}
}

func TestCreateBatch_IdempotencyKeyReplay(t *testing.T) {
	store := &fakeStore{}
	svc := NewNotifications(store)
	key := "campaign-7"

	first, replayed, err := svc.CreateBatch(t.Context(), threeItems(), &key)
	if err != nil || replayed {
		t.Fatalf("first CreateBatch = replayed %v, err %v", replayed, err)
	}
	if got := *first.Notifications[2].IdempotencyKey; got != "campaign-7#2" {
		t.Errorf("item key = %q, want campaign-7#2", got)
	}

	again, replayed, err := svc.CreateBatch(t.Context(), threeItems(), &key)
	if err != nil {
		t.Fatalf("retry CreateBatch: %v", err)
	}
	if !replayed || again.ID != first.ID || !reflect.DeepEqual(again.Notifications, first.Notifications) {
		t.Errorf("retry = batch %s, replayed %v; want batch %s replayed with the same items", again.ID, replayed, first.ID)
	}
	if len(store.saved) != 3 {
		t.Errorf("saved %d notifications, want 3", len(store.saved))
	}
}

func TestCreateBatch_IdempotencyKeyReused(t *testing.T) {
	changedItem := threeItems()
	changedItem[1].Content = "a different message"

	tests := []struct {
		name  string
		batch []domain.Notification
	}{
		{"different item", changedItem},
		{"fewer items", threeItems()[:2]},
		{"more items", append(threeItems(), validNotification())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewNotifications(&fakeStore{})
			key := "campaign-7"
			if _, _, err := svc.CreateBatch(t.Context(), threeItems(), &key); err != nil {
				t.Fatalf("first CreateBatch: %v", err)
			}
			if _, _, err := svc.CreateBatch(t.Context(), tt.batch, &key); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
				t.Errorf("err = %v, want domain.ErrIdempotencyKeyReused", err)
			}
		})
	}
}

func TestGetBatch(t *testing.T) {
	svc := NewNotifications(&fakeStore{})
	batch, _, err := svc.CreateBatch(t.Context(), threeItems(), nil)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	got, err := svc.GetBatch(t.Context(), batch.ID)
	if err != nil || got.Total != 3 || got.Counts[domain.StatusPending] != 3 {
		t.Errorf("GetBatch = %+v, %v; want 3 pending", got, err)
	}
	if _, err := svc.GetBatch(t.Context(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetBatch(unknown) err = %v, want domain.ErrNotFound", err)
	}
}
