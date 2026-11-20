// Package repository reads and writes data in PostgreSQL. All SQL lives here.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// ClaimNext takes the next due notification of the given channel and priority off the queue:
// it becomes processing, its attempt count goes up, and it is leased to the caller for lease.
// ok is false when nothing of that channel and priority is due.
func (r *Notifications) ClaimNext(ctx context.Context, ch domain.Channel, p domain.Priority, lease time.Duration) (_ domain.Notification, ok bool, _ error) {
	// SKIP LOCKED: a row another worker is claiming right now is skipped, not waited for, so
	// concurrent claims get different rows. The inner query is served by the channel queue index.
	row := r.db.QueryRow(ctx, `
		UPDATE notifications
		SET status = 'processing', attempts = attempts + 1,
		    locked_until = now() + $3 * interval '1 millisecond', updated_at = now()
		WHERE id = (
			SELECT id FROM notifications
			WHERE status = 'pending' AND channel = $1 AND priority = $2 AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+notificationColumns, ch, p, lease.Milliseconds())

	n, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, false, nil
	}
	if err != nil {
		return domain.Notification{}, false, fmt.Errorf("claim %s %s notification: %w", p, ch, err)
	}
	return n, true, nil
}

// The result of a send is recorded only by the claim that made it: attempt is the claim's
// attempt number, and if the notification was claimed again since (an expired lease, Phase 8)
// nothing changes and ok is false.

// MarkSent records a successful send with the provider's message ID.
func (r *Notifications) MarkSent(ctx context.Context, id uuid.UUID, attempt int, providerMessageID string) (ok bool, _ error) {
	return r.finish(ctx, id, attempt, `status = 'sent', provider_message_id = $3, last_error = NULL`, providerMessageID)
}

// MarkFailed records a failed send with the error.
func (r *Notifications) MarkFailed(ctx context.Context, id uuid.UUID, attempt int, lastError string) (ok bool, _ error) {
	return r.finish(ctx, id, attempt, `status = 'failed', last_error = $3`, lastError)
}

// leaseExpiredError is the last_error of a notification taken back from a stopped worker.
const leaseExpiredError = "lease expired: the worker stopped before recording a result"

// RecoverExpired puts notifications whose lease has expired back in the queue, due at once,
// and returns how many. Their worker stopped (crashed, killed) without recording a result (RET-5).
// The attempt still counts: the provider may have received the message. If that worker is
// only slow, its result is not recorded later, since finish checks the status and attempt.
func (r *Notifications) RecoverExpired(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE notifications
		SET status = 'pending', locked_until = NULL, last_error = $1, updated_at = now()
		WHERE status = 'processing' AND locked_until < now()`, leaseExpiredError)
	if err != nil {
		return 0, fmt.Errorf("recover expired leases: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Retry puts a notification back in the queue to be tried again after delay, keeping the error.
// The attempt counts. The delay is added to the database's clock, like the lease.
func (r *Notifications) Retry(ctx context.Context, id uuid.UUID, attempt int, lastError string, delay time.Duration) (ok bool, _ error) {
	return r.finish(ctx, id, attempt,
		`status = 'pending', last_error = $3, next_attempt_at = now() + $4 * interval '1 millisecond'`,
		lastError, delay.Milliseconds())
}

// Release puts a notification back in the queue, e.g. when a send was interrupted by shutdown.
// The attempt still counts: the provider may have received the message.
func (r *Notifications) Release(ctx context.Context, id uuid.UUID, attempt int) (ok bool, _ error) {
	return r.finish(ctx, id, attempt, `status = 'pending'`)
}

// Unclaim puts a notification back in the queue as if it had not been claimed: nothing was
// sent (e.g. no rate-limit slot), so the attempt doesn't count.
func (r *Notifications) Unclaim(ctx context.Context, id uuid.UUID, attempt int) (ok bool, _ error) {
	return r.finish(ctx, id, attempt, `status = 'pending', attempts = attempts - 1`)
}

// finish applies set (with its extra parameters from $3) to the claimed notification and ends its lease.
func (r *Notifications) finish(ctx context.Context, id uuid.UUID, attempt int, set string, args ...any) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE notifications SET `+set+`, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'processing' AND attempts = $2`,
		append([]any{id, attempt}, args...)...)
	if err != nil {
		return false, fmt.Errorf("record result of %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// CancelPending cancels the notification only if it is still pending, in one statement,
// so a worker claiming it at the same moment can't also win (IMP-4). ok is false when no
// pending notification with this ID exists; the caller decides what that means.
func (r *Notifications) CancelPending(ctx context.Context, id uuid.UUID) (_ domain.Notification, ok bool, _ error) {
	// The row lock taken by UPDATE makes a concurrent claim wait; whichever goes second
	// re-checks status = 'pending', finds it changed and updates nothing.
	row := r.db.QueryRow(ctx, `
		UPDATE notifications SET status = 'cancelled', updated_at = now()
		WHERE id = $1 AND status = 'pending'
		RETURNING `+notificationColumns, id)

	n, err := scanNotification(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, false, nil
	}
	if err != nil {
		return domain.Notification{}, false, fmt.Errorf("cancel notification %s: %w", id, err)
	}
	return n, true, nil
}

// CancelPendingInBatch cancels every notification of the batch that is still pending and
// returns how many it cancelled. Like CancelPending, each row is checked and changed in
// one step, so a worker claim on any of them can't also win.
func (r *Notifications) CancelPendingInBatch(ctx context.Context, batchID uuid.UUID) (int, error) {
	tag, err := r.db.Exec(ctx, `
		UPDATE notifications SET status = 'cancelled', updated_at = now()
		WHERE batch_id = $1 AND status = 'pending'`, batchID)
	if err != nil {
		return 0, fmt.Errorf("cancel batch %s: %w", batchID, err)
	}
	return int(tag.RowsAffected()), nil
}

// List returns one page of notifications matching f, newest first (D6: cursor pagination).
func (r *Notifications) List(ctx context.Context, f domain.ListFilter) (domain.Page, error) {
	var afterTime *time.Time
	var afterID *uuid.UUID
	if f.After != nil {
		afterTime, afterID = &f.After.CreatedAt, &f.After.ID
	}

	// A NULL parameter switches its condition off, so one static query serves every filter mix.
	// "(created_at, id) < cursor" continues right after the previous page by walking the
	// (created_at, id) index, so every page costs the same, however deep.
	// One extra row is fetched to learn whether there is a next page.
	rows, err := r.db.Query(ctx, `
		SELECT `+notificationColumns+`
		FROM notifications
		WHERE ($1::text IS NULL OR status = $1)
		  AND ($2::text IS NULL OR channel = $2)
		  AND ($3::uuid IS NULL OR batch_id = $3)
		  AND ($4::timestamptz IS NULL OR created_at >= $4)
		  AND ($5::timestamptz IS NULL OR created_at < $5)
		  AND ($6::timestamptz IS NULL OR (created_at, id) < ($6, $7::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $8`,
		f.Status, f.Channel, f.BatchID, f.From, f.To, afterTime, afterID, f.Limit+1,
	)
	if err != nil {
		return domain.Page{}, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Notification, 0, f.Limit+1)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return domain.Page{}, fmt.Errorf("scan notification: %w", err)
		}
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		return domain.Page{}, fmt.Errorf("list notifications: %w", err)
	}

	page := domain.Page{Items: items}
	if len(items) > f.Limit { // the extra row exists: there is a next page
		page.Items = items[:f.Limit]
		last := page.Items[f.Limit-1]
		page.Next = &domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// BatchSummary counts the notifications of a batch per status, or returns domain.ErrNotFound.
func (r *Notifications) BatchSummary(ctx context.Context, batchID uuid.UUID) (domain.BatchSummary, error) {
	// One row per status present; served by the batch_id index.
	rows, err := r.db.Query(ctx, `
		SELECT status, count(*), min(created_at)
		FROM notifications WHERE batch_id = $1
		GROUP BY status`, batchID)
	if err != nil {
		return domain.BatchSummary{}, fmt.Errorf("batch summary %s: %w", batchID, err)
	}
	defer rows.Close()

	summary := domain.BatchSummary{ID: batchID, Counts: make(map[domain.Status]int, len(domain.Statuses))}
	for _, s := range domain.Statuses {
		summary.Counts[s] = 0
	}
	for rows.Next() {
		var (
			status  domain.Status
			count   int
			created time.Time
		)
		if err := rows.Scan(&status, &count, &created); err != nil {
			return domain.BatchSummary{}, fmt.Errorf("scan batch summary: %w", err)
		}
		summary.Counts[status] = count
		summary.Total += count
		if summary.CreatedAt.IsZero() || created.Before(summary.CreatedAt) {
			summary.CreatedAt = created
		}
	}
	if err := rows.Err(); err != nil {
		return domain.BatchSummary{}, fmt.Errorf("batch summary %s: %w", batchID, err)
	}
	if summary.Total == 0 { // no rows: no such batch
		return domain.BatchSummary{}, fmt.Errorf("batch summary %s: %w", batchID, domain.ErrNotFound)
	}
	return summary, nil
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
