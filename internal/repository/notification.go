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
func (r *Notifications) Create(ctx context.Context, n domain.Notification) (domain.Notification, error) {
	// The database fills in id, status, attempts and the timestamps from column defaults.
	row := r.db.QueryRow(ctx, `
		INSERT INTO notifications
			(batch_id, idempotency_key, correlation_id, channel, recipient, content, priority)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+notificationColumns,
		n.BatchID, n.IdempotencyKey, n.CorrelationID, n.Channel, n.Recipient, n.Content, n.Priority,
	)

	created, err := scanNotification(row)
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
