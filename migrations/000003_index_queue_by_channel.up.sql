-- Workers claim per channel (D16): the next pending job of one channel, by priority, then by time.
-- Partial index: only pending rows are indexed, so it stays small.
-- CONCURRENTLY builds it without blocking inserts; it must be the only statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS notifications_channel_queue_idx
    ON notifications (channel, priority, next_attempt_at)
    WHERE status = 'pending';
