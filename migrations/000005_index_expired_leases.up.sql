-- Finds notifications whose worker stopped before recording a result (expired lease, 8.4).
-- Partial index: only rows being processed, so it stays tiny.
CREATE INDEX CONCURRENTLY IF NOT EXISTS notifications_lease_idx
    ON notifications (locked_until)
    WHERE status = 'processing';
