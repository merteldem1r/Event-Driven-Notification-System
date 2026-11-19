-- The notifications table is both the source of truth and the queue.
CREATE TABLE notifications (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id            UUID,
    idempotency_key     TEXT        UNIQUE,

    channel             TEXT        NOT NULL CHECK (channel IN ('sms', 'email', 'push')),
    recipient           TEXT        NOT NULL,
    content             TEXT        NOT NULL,
    priority            SMALLINT    NOT NULL CHECK (priority IN (1, 2, 3)), -- 1 high, 2 normal, 3 low

    status              TEXT        NOT NULL DEFAULT 'pending'
                                    CHECK (status IN ('pending', 'processing', 'sent', 'failed', 'cancelled')),
    attempts            INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(), -- not picked up before this time
    locked_until        TIMESTAMPTZ,                        -- worker lease while processing
    last_error          TEXT,
    provider_message_id TEXT,
    correlation_id      TEXT,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Workers claim the next pending job by priority, then by time.
-- Partial index: only pending rows are indexed, so it stays small.
CREATE INDEX notifications_queue_idx
    ON notifications (priority, next_attempt_at)
    WHERE status = 'pending';

-- Batch status lookups.
CREATE INDEX notifications_batch_id_idx
    ON notifications (batch_id)
    WHERE batch_id IS NOT NULL;
