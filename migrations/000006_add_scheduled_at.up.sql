-- The time a client asked a notification to be sent at (OPT-2, D21); NULL means as soon as possible.
-- next_attempt_at starts at this time, so the queue holds the row until then.
ALTER TABLE notifications ADD COLUMN scheduled_at TIMESTAMPTZ;
