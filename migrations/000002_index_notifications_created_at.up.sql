-- Serves the list endpoint: newest first, cursor pagination on (created_at, id).
-- A B-tree can be read backwards, so DESC order needs no separate index.
-- CONCURRENTLY builds it without blocking inserts; it must be the only statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS notifications_created_at_id_idx
    ON notifications (created_at, id);
