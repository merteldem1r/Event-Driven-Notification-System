# 07 — Phase 6: Worker, the End-to-End Flow

> **Result:** notifications now travel the whole way: the API saves them, workers take them from
> the queue by priority, send them to the provider (webhook.site) and record the result. Workers
> run as many parallel loops, never take the same notification twice, and stop gracefully.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Worker program | `cmd/worker` | Config, logger, database, signal handling; `make run-worker` |
| Loops and shutdown | `internal/worker` | `WORKER_CONCURRENCY` loops; idle wait `WORKER_POLL_INTERVAL`; graceful stop within `WORKER_SHUTDOWN_TIMEOUT` |
| Claim | `repository.ClaimNext` | `FOR UPDATE SKIP LOCKED`; row becomes `processing`, attempts + 1, leased for `WORKER_LEASE_DURATION` |
| Priority | `service.Dispatcher` | Weighted 6:3:1 with fallback (D7) |
| Provider client | `internal/provider` | `POST {to, channel, content}`, reads `202 {messageId, status, timestamp}`, `PROVIDER_TIMEOUT` (D9: webhook.site) |
| Send and record | `service.Dispatcher`, repository | `sent` + `messageId`, `failed` + error, or back to `pending` on shutdown |
| Compose | `docker-compose.yml`, `Dockerfile` | One image with both programs; worker service with a 30 s stop grace period |

## How it works

**The loop.** Each of the worker's loops asks the dispatcher to process one notification, again
and again. When the queue is empty (or the database fails) it waits one poll interval before asking
again; while there is work it doesn't wait at all.

**Taking a notification.** One statement picks the oldest due row of a priority and marks it
`processing`. `SKIP LOCKED` makes concurrent claims skip rows another worker is taking, so each row
goes to exactly one loop, across any number of processes. The dispatcher chooses which priority to
try first from a fixed pattern (6 high, 3 normal, 1 low in every 10) and falls back to the others
when that queue is empty, so `low` is never starved and no capacity is wasted.

**Sending and recording.** The provider client posts the message with the notification ID as
`Idempotency-Key` and the correlation ID as `X-Correlation-ID`, and gives up after the timeout.
Only a `2xx` with a `messageId` counts as sent. The result is saved with a context the shutdown
can't cancel, so a delivered message is never left looking unsent. Each claim's attempt number acts
as a fencing token: only that claim can record a result, which protects against a second worker
that took over an expired lease (Phase 8).

**Graceful shutdown.** On SIGINT or SIGTERM the loops stop taking work, but in-flight sends keep a
separate context that only the shutdown deadline cancels. If the deadline passes, sends are cut off
and their notifications go back to `pending`. Compose gives the container 30 s before killing it,
more than the 25 s the worker needs.

**Logs.** One line per result, with the notification ID, channel, priority, attempt, duration and
the provider's message ID, plus the correlation ID of the original API request. Never the
recipient or the content.

## Trying it

```
PROVIDER_URL=https://webhook.site/<your-id>    in .env.local (make run-worker) or .env (Compose)
make up                                        or: make run-api and make run-worker
POST http://localhost:8080/api/v1/notifications    → appears on webhook.site within a second
GET  http://localhost:8080/api/v1/notifications/<id>   → "status": "sent", "provider_message_id"
docker compose up -d --scale worker=3          several workers share the queue safely
```

## Not done yet / decided later

- No rate limit yet: workers send as fast as they can (Phase 7).
- A failure is final (`failed`); retries with backoff, `Retry-After` and lease recovery come in Phase 8.
- No local fake provider (D9); free webhook.site URLs allow 50 requests and expire after 7 days.
