# Requirements

Every requirement of the project as a checklist. This is the in-repo copy of the original
requirements document (`docs/Software-Engineer-Assessment-Golang.pdf`, kept locally and gitignored),
plus the requirements it implies but doesn't spell out.

**Update the status after every step.** The "Phase" column links to [`plan.md`](plan.md).

Status values: **Done**, **Partial**, **Todo**.

---

## 1. Notification Management API

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| API-1 | Create a notification with recipient, channel, content and priority | Done | 4 | `POST /api/v1/notifications` → `202` with the stored notification; priority optional (default `normal`) |
| API-2 | Batch creation, up to 1000 notifications per request | Done | 5 | `POST /api/v1/notifications/batch`; shared batch ID; all-or-nothing (D5); `Idempotency-Key` for the whole batch |
| API-3 | Query notification status by ID | Done | 4 | `GET /api/v1/notifications/{id}`; 404 if unknown, 400 if the ID is not a UUID |
| API-4 | Query status by batch ID | Done | 5 | `GET /api/v1/batches/{id}`: total and counts per status |
| API-5 | Cancel pending notifications | Done | 5 | Single and batch cancel (D14); scheduled and retry-waiting rows are `pending` too |
| API-6 | List notifications with filters (status, channel, date range) | Done | 5 | `GET /api/v1/notifications`; also `batch_id`; all errors at once |
| API-7 | Pagination on the list endpoint | Done | 5 | Cursor pagination (D6), stable under inserts |

## 2. Processing Engine

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| PROC-1 | Process notifications asynchronously via queue workers | Done | 6 | Worker loops claim with `FOR UPDATE SKIP LOCKED`, send, record the result |
| PROC-2 | Rate limiting: max 100 messages/second **per channel** | Done | 7 | Shared Redis limiter (D15); loops per channel claim, then wait for a slot, then send (D16); fail closed when Redis is down; at most 100 at the provider in any one second, sending at 85/s with burst 5 (D17) |
| PROC-3 | Priority queue support (high, normal, low) | Done | 6 | Weighted 6:3:1 with fallback (D7) |
| PROC-4 | Content validation (character limits, required fields) | Done | 4 | D4: SMS 1,600, push 500, email 100,000 characters; recipient format per channel; all field errors returned as `400 validation_failed` |
| PROC-5 | Idempotency support to prevent duplicate sends | Partial | 4, 8 | API level done for single and batch creates (D12: `Idempotency-Key` header, race-free via unique index); worker level (lease + dedup key) in Phase 8 |

## 3. Delivery & Retry Logic (our own design)

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| RET-1 | Classify errors: retry temporary ones (timeout, network, 5xx, 429), not permanent ones (4xx) | Done | 8 | `provider.IsTemporary` (D18); temporary ones go back to the queue, permanent ones are `failed` |
| RET-2 | Exponential backoff with jitter between attempts | Done | 8 | `internal/retry` (D8): 10 s doubling to 10 min, equal jitter |
| RET-3 | Max attempts, then mark `failed` and keep the last error | Done | 8 | `RETRY_MAX_ATTEMPTS` (5, D8); `last_error` keeps the last failure |
| RET-4 | Respect `Retry-After` from the provider | Done | 8 | Seconds or HTTP date; used when longer than our wait, capped at `RETRY_MAX_DELAY` (D8) |
| RET-5 | Recover jobs held by crashed workers (lease expiry) | Done | 8 | Sweep every 10 s in each worker puts expired leases (60 s) back in the queue (D19); a slow worker's late result is refused by the attempt check |
| RET-6 | Store the provider's `messageId` on success | Done | 6 | `provider_message_id`, set with status `sent` |

## 4. Observability

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| OBS-1 | Real-time metrics endpoint: queue depth, success/failure rates, latency | Todo | 9 | Prometheus `/metrics` |
| OBS-2 | Structured logging | Partial | 2, 9 | JSON logs via `slog` in the API and the worker (done); one log line per HTTP request (done); worker logs one line per send result (IDs, channel, attempt, duration; never content or recipient); an outage of Redis or the database logs one line when it starts and one when it ends, not every retry |
| OBS-3 | Correlation IDs in logs, carried from API to worker | Done | 4, 6 | API side (D13): `X-Correlation-ID`, on every API log line, stored on the notification; the worker puts it on its log lines for that notification (6.2) |
| OBS-4 | Health check endpoint | Partial | 2, 3, 9 | API `GET /health` checks PostgreSQL, 200/503 (done); the API doesn't use Redis, so the Redis check goes on the worker's health endpoint in Phase 9 (D16) |

## 5. External Provider Integration

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| PRV-1 | Send `POST {to, channel, content}` to the provider (webhook.site) | Done | 6 | `internal/provider`; verified against webhook.site |
| PRV-2 | Handle the expected `202 {messageId, status, timestamp}` response | Done | 6 | A 2xx without `messageId` is a failure; the `messageId` is stored |
| PRV-3 | Provider behind an interface; URL from config; local mock available | Done | 6, 7 | `Sender` interface and `PROVIDER_URL`; local fake provider (`cmd/fakeprovider`, D9 revised) with delay and failure rate, default in Compose; webhook.site via `PROVIDER_URL` |

## 6. Optional Features

| ID | Feature | Status | Phase | Notes |
|---|---|---|---|---|
| OPT-1 | Failure handling | Partial | 6, 8 | Graceful worker shutdown done (finish in-flight work within a timeout); retries with backoff and a maximum, then `failed` with the last error (8.2, 8.3); crashed workers' notifications recovered by lease expiry (8.4, done); re-queue endpoint optional (8.5); no circuit breaker (not needed with retries) |
| OPT-2 | Scheduled notifications (future delivery) | Todo | 10 | |
| OPT-3 | Template system with variable substitution | Todo | 11 | |
| OPT-4 | WebSocket real-time status updates | Todo | 12 | Redis pub/sub between worker and API |
| OPT-5 | Distributed tracing | Todo | 13 | OpenTelemetry + Jaeger |
| OPT-6 | GitHub Actions CI/CD: automated testing and linting | Partial | 2, 14 | Commit-message check, lint, vet, tests with race detector (done); image build and more checks in Phase 14 |

## 7. Deliverables

| ID | Deliverable | Status | Phase | Notes |
|---|---|---|---|---|
| DEL-1 | Repository with clean commit history | Partial | all | Conventional Commits enforced by hook + CI (done); ongoing |
| DEL-2 | README.md: setup, architecture overview, API examples | Todo | 15 | |
| DEL-3 | Docker Compose: one-command setup (`docker compose up`) | Done | 2, 6 | api, worker, fake provider, migrations, PostgreSQL, Redis; works without any settings (the worker uses the fake provider unless `PROVIDER_URL` is set) |
| DEL-4 | API documentation: Swagger/OpenAPI specification | Partial | 4–15 | `docs/openapi.yaml` (OpenAPI 3.0.3) covers every endpoint so far (create, batch, get, list, batch status, both cancels, health); tests keep it in sync with the router and real responses; grows with each endpoint; Swagger UI in Phase 15 |
| DEL-5 | Database migrations: versioned schema changes | Done | 3 | golang-migrate, numbered up/down SQL files, applied automatically by Compose; new migrations added as features need them |
| DEL-6 | Test suite runnable with a single command | Partial | all | `make test` (race detector) and `make check` (done); repository tests use testcontainers and need Docker; grows with each step |

## 8. Implied requirements (not written, but expected)

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| IMP-1 | The rate limit holds across **all** worker processes, not per worker | Done | 7 | One Redis key per channel; load test with 3 and 5 worker processes: 85/s per channel in total, busiest second 90 to 93 at the provider |
| IMP-2 | Two workers never claim the same notification | Done | 6 | `SKIP LOCKED`; tested with 20 concurrent claimers and with two worker processes |
| IMP-3 | Saving and enqueuing are atomic (no dual-write problem) | Done | 3, 5 | One table: inserting a row is enqueuing it; a batch is one statement, so all or none of it is saved |
| IMP-4 | Cancel and worker claim can't both win (no race) | Done | 5 | One conditional `UPDATE`; race tested against a simulated claim |
| IMP-5 | Graceful shutdown of API and workers (finish in-flight work on SIGTERM) | Done | 2, 6 | API (2.1); worker (6.1, 6.4): stops claiming, finishes in-flight sends within `WORKER_SHUTDOWN_TIMEOUT`, puts cut-off sends back in the queue |
| IMP-6 | Low-priority messages are not starved forever | Done | 6 | 1 in 10 claims prefers `low` (D7) |
| IMP-7 | Config only via environment variables; invalid config fails at startup | Done | 2 | `internal/config` |
| IMP-8 | Batch of 1000 is inserted efficiently (one transaction, bulk insert) | Done | 5 | One `INSERT ... SELECT FROM unnest(...)` statement; 1,000 rows in about 50 ms in tests |
| IMP-9 | Burst test proves the 100/s cap holds under load | Done | 7 | `make loadtest`: 1,000 per channel at once on all three channels; fails if the provider gets more than 100 in any one second (D17) |
