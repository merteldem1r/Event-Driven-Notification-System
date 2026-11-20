-- Replaced by notifications_channel_queue_idx (000003): no query claims across channels any more.
DROP INDEX CONCURRENTLY IF EXISTS notifications_queue_idx;
