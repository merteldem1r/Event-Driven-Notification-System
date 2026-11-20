# 09 — Phase 8: Retries and Failure Handling

> **Result:** a failed send is no longer simply final. Temporary failures are tried again with
> growing, randomized waits, respecting the provider's `Retry-After`, up to 5 attempts; failures
> a retry can't fix end at once. Notifications of a worker that crashed mid-send are taken back
> when its lease expires and sent by another worker.
> **Status:** done. The re-queue endpoint (8.5) is optional; the circuit breaker was dropped.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Error classification | `provider.IsTemporary`, `(*provider.Error).Temporary` | Temporary: no response, `408`, `429`, `5xx`, `2xx` without a `messageId`; permanent: `3xx`, other `4xx`, errors before sending (D18) |
| `Retry-After` | `internal/provider` | Seconds or HTTP date, carried on `provider.Error.RetryAfter` |
| Retry policy | `internal/retry` | `Next(attempt, retryAfter)`: wait and try again, or stop (D8) |
| Retry in the queue | `repository.Retry` | Back to `pending`, `next_attempt_at = now() + wait`, error kept, attempt counted |
| Decision per failure | `service.Dispatcher` | Retry, or `failed` with the last error; logged with `temporary` and `reason` |
| Lease recovery | `repository.RecoverExpired`, `worker` sweep, migration 000005 | Every 10 s, expired `processing` rows go back to `pending` (D19) |
| API | `GET /api/v1/notifications/{id}` | `next_attempt_at` while a retry is waiting |
| Settings | `internal/config`, `.env.example` | `RETRY_MAX_ATTEMPTS` (5), `RETRY_BASE_DELAY` (10s), `RETRY_MAX_DELAY` (10m), `WORKER_LEASE_DURATION` (60s), `WORKER_RECOVER_INTERVAL` (10s) |

## How it works

**After a failed send** the dispatcher decides in this order:

```
cut off by shutdown?        -> back to pending (not a failure, Phase 6)
permanent (e.g. 400)?       -> failed, last_error           reason "rejected by the provider"
temporary, attempts left?   -> pending, next_attempt_at = now() + wait
temporary, 5th attempt?     -> failed, last_error           reason "no attempts left"
```

**The wait.** It starts at 10 s and doubles with each failed attempt, up to 10 minutes. Then it is
picked at random between half of it and all of it (equal jitter), so notifications that failed
together don't come back together. If the provider's `Retry-After` asks for longer, that wins,
within the same 10-minute cap. With the defaults:

| After attempt | Wait |
|---|---|
| 1 | 5-10 s |
| 2 | 10-20 s |
| 3 | 20-40 s |
| 4 | 40-80 s |
| 5 | none: `failed` |

About 1.5 to 2.5 minutes pass from the first attempt to giving up.

**No new queue machinery.** A retry is the same row back in `pending` with a later
`next_attempt_at`. The claim already skips rows that aren't due, so the database holds it until
then. A retry goes through the rate limit like any send. Times come from the database's clock.

**A crashed worker.** A claim leases the notification for 60 s. If the worker dies before saving
a result, the row stays `processing`. Every worker runs a small sweep every 10 s that puts rows
with an expired lease back to `pending` with `last_error` "lease expired: the worker stopped
before recording a result"; the next claim takes it as the next attempt. If the old worker was
only slow and answers later, its result is refused: results are saved only while the row is
still `processing` with the same attempt number. If the crashed request had reached the
provider, the message goes out twice, with the same `Idempotency-Key` (at-least-once delivery).

**Logs.** One line per outcome: `notification failed, will retry` with `retry_in`, or
`notification failed` with `reason`; `took back notifications whose worker stopped without a
result` with a count. Sweep errors are part of the outage logging (Phase 7): logged once.

## Trying it

```
FAKE_PROVIDER_FAILURE_RATE=1 RETRY_BASE_DELAY=1s RETRY_MAX_DELAY=20s docker compose up -d --build fakeprovider worker
POST a notification, GET it a few times     attempts grow, next_attempt_at moves ahead;
                                            after ~15 s: failed, attempts 5, last_error 503
docker compose up -d fakeprovider worker     back to normal

FAKE_PROVIDER_LATENCY=3s docker compose up -d fakeprovider
POST a notification, then within 2 s:  docker compose kill worker && docker compose start worker
GET it: processing for ~60 s, then sent on attempt 2
```

## Not done yet / decided later

- Re-queueing `failed` notifications through the API (8.5) is optional, done only if time is
  left. No circuit breaker: retries with backoff and a maximum cover provider outages here.
- The fake provider answers only `202` or `503`, without `Retry-After`; `429`, other statuses
  and `Retry-After` are covered by tests with a test server.
- A notification that crashes its worker every time would be taken back again and again; in Go
  a crash is rare, and the attempt count shows it.
- Metrics for retries and failures come with Phase 9.
