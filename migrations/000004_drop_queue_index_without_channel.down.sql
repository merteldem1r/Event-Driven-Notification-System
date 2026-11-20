CREATE INDEX CONCURRENTLY IF NOT EXISTS notifications_queue_idx
    ON notifications (priority, next_attempt_at)
    WHERE status = 'pending';
