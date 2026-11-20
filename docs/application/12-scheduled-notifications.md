# 12 — Phase 10: Scheduled Notifications

> **Result:** a client can ask for a notification to be sent later, up to 30 days ahead. The
> queue already waits for rows whose time hasn't come, so the feature is one field, one column
> and its rules; workers didn't change.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Field | `scheduled_at` on `POST /notifications` and each batch item | RFC 3339 with a time zone; shown in responses in UTC |
| Rules | `domain.Notification.Validate(now)` | Future only, at most 30 days ahead (`MaxScheduleAhead`), else `400 validation_failed` (D21) |
| Malformed time | `handler.scheduleTime` | `400 invalid_body` naming the field and showing the format |
| Storage | migration 000006, `repository.Create`, `CreateBatch` | `scheduled_at` column; `next_attempt_at` starts at it |
| Metric | `domain.Notification.DueAt`, `service.Dispatcher` | Delivery time counts from the scheduled time |

## How it works

```
POST {"scheduled_at":"2026-11-21T09:00:00+03:00", ...}
  -> validated against the API's clock: future, at most 30 days ahead
  -> INSERT scheduled_at = 06:00Z, next_attempt_at = 06:00Z, status pending
  -> the claim skips it until next_attempt_at <= now()    (unchanged since Phase 6)
  -> sent like any other notification: priority, rate limit, retries
```

**Why a column and not only `next_attempt_at`.** Retries move `next_attempt_at`, so the requested
time would be lost after the first failure. The column keeps it, the API shows it, and the
delivery-time metric measures from it instead of from creation, so a day-ahead schedule isn't
reported as a day of delay.

**Why a past time is rejected.** It is usually a time zone mistake: 15:00 meant locally, sent
as 15:00 UTC. Sending at once would hide the mistake and deliver hours early. Clients wanting
"now" leave the field out.

**Unchanged.** A scheduled notification is `pending` until its time, so cancel works on it.
An idempotent replay must repeat the same time (compared to the microsecond, as PostgreSQL
stores it), otherwise the key counts as reused.

## Trying it

```
POST /api/v1/notifications with "scheduled_at" 15 s ahead     202, pending, scheduled_at in UTC
GET it after 5 s and 12 s                                     still pending, attempts 0
GET it after 20 s                                             sent, attempts 1
Measured: sent 0.44 s after its scheduled time.

"scheduled_at":"2020-01-01T09:00:00Z"   400 scheduled_at: must be in the future
"scheduled_at":"2030-01-01T09:00:00Z"   400 scheduled_at: must be at most 30 days ahead
"scheduled_at":"2026-11-21T09:00:00"    400 invalid_body: must be an RFC 3339 time with a time zone
```

## Not done yet / decided later

- The queue-depth metric counts scheduled notifications as `pending`; the oldest-due age
  ignores them until they are due, so it still shows whether sending keeps up.
- A large campaign scheduled for one moment is sent at the rate limit from then on, not all at
  that second.
- No changing the time of a scheduled notification: cancel it and create a new one.
