// Package repository reads and writes data in PostgreSQL. All SQL lives here.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// notificationColumns is the column list every query returns, in scanNotification order.
const notificationColumns = `id, batch_id, idempotency_key, correlation_id,
	channel, recipient, content, priority,
	status, attempts, next_attempt_at, last_error, provider_message_id,
	created_at, updated_at`

// Notifications stores notifications in the notifications table.
type Notifications struct {
	db *pgxpool.Pool
}

// NewNotifications returns a repository that uses the given pool.
func NewNotifications(db *pgxpool.Pool) *Notifications {
	return &Notifications{db: db}
}

// Create inserts a new notification and returns it as stored.
// Saving it also enqueues it: new rows start as pending.
// If its idempotency key is already taken, nothing is saved and domain.ErrIdempotencyKeyExists is returned.
func (r *Notifications) Create(ctx context.Context, n domain.Notification) (domain.Notification, error) {
	// The database fills in id, status, attempts and the timestamps from column defaults.
	// ON CONFLICT lets the unique index decide atomically which of two same-key requests wins.
	// Unlike a failing INSERT, it also doesn't abort a surrounding transaction (needed for batches).
	row := r.db.QueryRow(ctx, `
		INSERT INTO notifications
			(batch_id, idempotency_key, correlation_id, channel, recipient, content, priority)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING `+notificationColumns,
		n.BatchID, n.IdempotencyKey, n.CorrelationID, n.Channel, n.Recipient, n.Content, n.Priority,
	)

	created, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) { // no row inserted: the key was taken
		return domain.Notification{}, domain.ErrIdempotencyKeyExists
	}
	if err != nil {
		return domain.Notification{}, fmt.Errorf("insert notification: %w", err)
	}
	return created, nil
}

// GetByID returns the notification with the given ID, or domain.ErrNotFound.
func (r *Notifications) GetByID(ctx context.Context, id uuid.UUID) (domain.Notification, error) {
	row := r.db.QueryRow(ctx, `SELECT `+notificationColumns+` FROM notifications WHERE id = $1`, id)

	n, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, fmt.Errorf("get notification %s: %w", id, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Notification{}, fmt.Errorf("get notification %s: %w", id, err)
	}
	return n, nil
}

// GetByIdempotencyKey returns the notification stored under key, or domain.ErrNotFound.
func (r *Notifications) GetByIdempotencyKey(ctx context.Context, key string) (domain.Notification, error) {
	row := r.db.QueryRow(ctx, `SELECT `+notificationColumns+` FROM notifications WHERE idempotency_key = $1`, key)

	n, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, fmt.Errorf("get notification by idempotency key: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Notification{}, fmt.Errorf("get notification by idempotency key: %w", err)
	}
	return n, nil
}

// scanNotification reads one row selected with notificationColumns.
func scanNotification(row pgx.Row) (domain.Notification, error) {
	var n domain.Notification
	err := row.Scan(
		&n.ID, &n.BatchID, &n.IdempotencyKey, &n.CorrelationID,
		&n.Channel, &n.Recipient, &n.Content, &n.Priority,
		&n.Status, &n.Attempts, &n.NextAttemptAt, &n.LastError, &n.ProviderMessageID,
		&n.CreatedAt, &n.UpdatedAt,
	)
	return n, err
}
