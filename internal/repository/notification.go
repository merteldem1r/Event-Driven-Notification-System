// Package repository reads and writes data in PostgreSQL. All SQL lives here.
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

// CreateBatch inserts all notifications in one statement, so either all are saved or none.
// It returns them as stored, in the same order as ns.
// If any idempotency key is already taken, nothing is saved and domain.ErrIdempotencyKeyExists is returned.
func (r *Notifications) CreateBatch(ctx context.Context, ns []domain.Notification) ([]domain.Notification, error) {
	// One array per column; unnest turns them back into rows inside the database.
	// IDs are generated here so results can be put back in input order: RETURNING promises no order.
	ids := make([]uuid.UUID, len(ns))
	keys := make([]pgtype.Text, len(ns))
	batchIDs := make([]pgtype.UUID, len(ns))
	correlationIDs := make([]pgtype.Text, len(ns))
	channels := make([]string, len(ns))
	recipients := make([]string, len(ns))
	contents := make([]string, len(ns))
	priorities := make([]int16, len(ns))
	for i, n := range ns {
		ids[i] = uuid.New()
		if n.IdempotencyKey != nil {
			keys[i] = pgtype.Text{String: *n.IdempotencyKey, Valid: true}
		}
		if n.BatchID != nil {
			batchIDs[i] = pgtype.UUID{Bytes: *n.BatchID, Valid: true}
		}
		if n.CorrelationID != nil {
			correlationIDs[i] = pgtype.Text{String: *n.CorrelationID, Valid: true}
		}
		channels[i], recipients[i], contents[i], priorities[i] = string(n.Channel), n.Recipient, n.Content, int16(n.Priority)
	}

	// A transaction, so a batch whose keys are only partly taken can be undone completely.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit; on failure nothing is kept

	// ON CONFLICT skips rows whose key is taken; a concurrent same-key batch waits here for ours.
	rows, err := tx.Query(ctx, `
		INSERT INTO notifications (id, idempotency_key, batch_id, correlation_id, channel, recipient, content, priority)
		SELECT * FROM unnest($1::uuid[], $2::text[], $3::uuid[], $4::text[], $5::text[], $6::text[], $7::text[], $8::smallint[])
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING `+notificationColumns,
		ids, keys, batchIDs, correlationIDs, channels, recipients, contents, priorities,
	)
	if err != nil {
		return nil, fmt.Errorf("insert batch: %w", err)
	}
	defer rows.Close()

	byID := make(map[uuid.UUID]domain.Notification, len(ns))
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan batch row: %w", err)
		}
		byID[n.ID] = n
	}
	if err := rows.Err(); err != nil { // e.g. a constraint failed: the whole statement is rolled back
		return nil, fmt.Errorf("insert batch: %w", err)
	}
	if len(byID) != len(ns) { // some rows were skipped: their keys were taken
		return nil, domain.ErrIdempotencyKeyExists
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit batch: %w", err)
	}

	created := make([]domain.Notification, len(ns))
	for i, id := range ids {
		created[i] = byID[id]
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

// GetByIdempotencyKeys returns the notifications stored under keys, in the order of keys.
// Keys with no notification are skipped, so a shorter result means some are missing.
func (r *Notifications) GetByIdempotencyKeys(ctx context.Context, keys []string) ([]domain.Notification, error) {
	rows, err := r.db.Query(ctx, `SELECT `+notificationColumns+` FROM notifications WHERE idempotency_key = ANY($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("get notifications by idempotency keys: %w", err)
	}
	defer rows.Close()

	byKey := make(map[string]domain.Notification, len(keys))
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		byKey[*n.IdempotencyKey] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get notifications by idempotency keys: %w", err)
	}

	found := make([]domain.Notification, 0, len(keys))
	for _, k := range keys {
		if n, ok := byKey[k]; ok {
			found = append(found, n)
		}
	}
	return found, nil
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
