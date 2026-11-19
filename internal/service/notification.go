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
	CreateBatch(ctx context.Context, ns []domain.Notification) ([]domain.Notification, error)
	GetByID(ctx context.Context, id uuid.UUID) (domain.Notification, error)
	GetByIdempotencyKey(ctx context.Context, key string) (domain.Notification, error)
	GetByIdempotencyKeys(ctx context.Context, keys []string) ([]domain.Notification, error)
	BatchSummary(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error)
	List(ctx context.Context, f domain.ListFilter) (domain.Page, error)
	CancelPending(ctx context.Context, id uuid.UUID) (_ domain.Notification, ok bool, _ error)
	CancelPendingInBatch(ctx context.Context, batchID uuid.UUID) (int, error)
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

// CreateBatch validates every notification and saves them together under a new batch ID.
// If any is invalid, nothing is saved (D5). Notifications are returned in the order of ns.
// With an idempotency key, a repeat of the same batch returns the stored batch with
// replayed = true; a key reused for a different batch returns domain.ErrIdempotencyKeyReused.
func (s *Notifications) CreateBatch(
	ctx context.Context, ns []domain.Notification, idempotencyKey *string,
) (_ domain.Batch, replayed bool, _ error) {
	if err := domain.ValidateBatch(ns, idempotencyKey); err != nil {
		return domain.Batch{}, false, err
	}

	batchID := uuid.New()
	items := make([]domain.Notification, len(ns)) // a copy, so the caller's slice isn't changed
	for i, n := range ns {
		n.BatchID = &batchID
		if idempotencyKey != nil {
			n.IdempotencyKey = ptr(itemKey(*idempotencyKey, i))
		}
		items[i] = n
	}

	created, err := s.store.CreateBatch(ctx, items)
	if errors.Is(err, domain.ErrIdempotencyKeyExists) {
		return s.replayBatch(ctx, *idempotencyKey, items)
	}
	if err != nil {
		return domain.Batch{}, false, fmt.Errorf("create batch: %w", err)
	}
	return domain.Batch{ID: batchID, Notifications: created}, false, nil
}

// itemKey derives one item's key from the batch key, reusing the unique index on single keys.
func itemKey(batchKey string, i int) string {
	return fmt.Sprintf("%s#%d", batchKey, i)
}

// replayBatch returns the batch already stored under batchKey, if it was created by the same
// request: same number of items, each the same request, all in one batch.
func (s *Notifications) replayBatch(
	ctx context.Context, batchKey string, items []domain.Notification,
) (domain.Batch, bool, error) {
	// Also ask for one key past the end: item keys run from #0 without gaps,
	// so if it exists, the stored batch is bigger than this request.
	keys := make([]string, len(items)+1)
	for i := range keys {
		keys[i] = itemKey(batchKey, i)
	}
	existing, err := s.store.GetByIdempotencyKeys(ctx, keys)
	if err != nil {
		return domain.Batch{}, false, fmt.Errorf("load batch for idempotency key: %w", err)
	}

	if len(existing) != len(items) || existing[0].BatchID == nil {
		return domain.Batch{}, false, domain.ErrIdempotencyKeyReused
	}
	batchID := *existing[0].BatchID
	for i, n := range existing {
		sameItem := *n.IdempotencyKey == keys[i] && n.SameRequest(items[i])
		if !sameItem || n.BatchID == nil || *n.BatchID != batchID {
			return domain.Batch{}, false, domain.ErrIdempotencyKeyReused
		}
	}
	return domain.Batch{ID: batchID, Notifications: existing}, true, nil
}

func ptr[T any](v T) *T { return &v }

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

// GetBatch returns how many of a batch's notifications are in each status,
// or an error wrapping domain.ErrNotFound.
func (s *Notifications) GetBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error) {
	return s.store.BatchSummary(ctx, batchID) // the repository already wraps the error with the ID
}

// List returns one page of notifications matching f, newest first.
// An invalid filter returns a *domain.ValidationError.
func (s *Notifications) List(ctx context.Context, f domain.ListFilter) (domain.Page, error) {
	if err := f.Validate(); err != nil {
		return domain.Page{}, err
	}
	page, err := s.store.List(ctx, f)
	if err != nil {
		return domain.Page{}, fmt.Errorf("list notifications: %w", err)
	}
	return page, nil
}

// cancelAttempts bounds Cancel's retries; see Cancel.
const cancelAttempts = 3

// Cancel stops a notification that hasn't been sent yet (D14). Cancelling an already
// cancelled notification returns it unchanged, so retrying is safe. Otherwise a notification
// past pending gives a *domain.NotCancellableError; an unknown ID wraps domain.ErrNotFound.
func (s *Notifications) Cancel(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	for range cancelAttempts {
		n, ok, err := s.store.CancelPending(ctx, id)
		if err != nil {
			return domain.Notification{}, fmt.Errorf("cancel: %w", err)
		}
		if ok {
			return n, nil
		}

		// Nothing was pending: find out why.
		current, err := s.store.GetByID(ctx, id)
		if err != nil {
			return domain.Notification{}, err // the repository already wraps it with the ID
		}
		switch current.Status {
		case domain.StatusCancelled:
			return current, nil
		case domain.StatusPending:
			// It was pending again by the time we looked (a worker released it in between),
			// so try the cancel again.
			continue
		default:
			return domain.Notification{}, &domain.NotCancellableError{Status: current.Status}
		}
	}
	return domain.Notification{}, fmt.Errorf("cancel notification %s: still changing after %d attempts", id, cancelAttempts)
}

// CancelBatch cancels every notification of the batch that is still pending (D14) and
// reports how many, with the batch's counts afterwards. Nothing left to cancel is not an
// error, so retrying is safe. An unknown batch wraps domain.ErrNotFound.
func (s *Notifications) CancelBatch(ctx context.Context, batchID uuid.UUID) (domain.BatchCancellation, error) {
	cancelled, err := s.store.CancelPendingInBatch(ctx, batchID)
	if err != nil {
		return domain.BatchCancellation{}, fmt.Errorf("cancel batch: %w", err)
	}
	// Also tells an unknown batch (no rows) apart from one with nothing left to cancel.
	summary, err := s.store.BatchSummary(ctx, batchID)
	if err != nil {
		return domain.BatchCancellation{}, err // the repository already wraps it with the ID
	}
	return domain.BatchCancellation{Cancelled: cancelled, Summary: summary}, nil
}
