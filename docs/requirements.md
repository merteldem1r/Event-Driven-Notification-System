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
| API-5 | Cancel pending notifications | Todo | 5 | Atomic: only if still `pending`/`scheduled` |
| API-6 | List notifications with filters (status, channel, date range) | Done | 5 | `GET /api/v1/notifications`; also `batch_id`; all errors at once |
| API-7 | Pagination on the list endpoint | Done | 5 | Cursor pagination (D6), stable under inserts |

## 2. Processing Engine

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| PROC-1 | Process notifications asynchronously via queue workers | Todo | 6 | PostgreSQL queue, `FOR UPDATE SKIP LOCKED` |
| PROC-2 | Rate limiting: max 100 messages/second **per channel** | Todo | 7 | Shared across all workers (Redis) |
| PROC-3 | Priority queue support (high, normal, low) | Todo | 6 | Strict vs weighted is decision D7 |
| PROC-4 | Content validation (character limits, required fields) | Done | 4 | D4: SMS 1,600, push 500, email 100,000 characters; recipient format per channel; all field errors returned as `400 validation_failed` |
| PROC-5 | Idempotency support to prevent duplicate sends | Partial | 4, 8 | API level done for single and batch creates (D12: `Idempotency-Key` header, race-free via unique index); worker level (lease + dedup key) in Phase 8 |

## 3. Delivery & Retry Logic (our own design)

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| RET-1 | Classify errors: retry temporary ones (timeout, network, 5xx, 429), not permanent ones (4xx) | Todo | 8 | |
| RET-2 | Exponential backoff with jitter between attempts | Todo | 8 | Numbers are decision D8 |
| RET-3 | Max attempts, then mark `failed` and keep the last error | Todo | 8 | |
| RET-4 | Respect `Retry-After` from the provider | Todo | 8 | |
| RET-5 | Recover jobs held by crashed workers (lease expiry) | Todo | 8 | |
| RET-6 | Store the provider's `messageId` on success | Todo | 6 | |

## 4. Observability

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| OBS-1 | Real-time metrics endpoint: queue depth, success/failure rates, latency | Todo | 9 | Prometheus `/metrics` |
| OBS-2 | Structured logging | Partial | 2, 9 | JSON logs via `slog` in the API (done); one log line per HTTP request (done); worker logs pending |
| OBS-3 | Correlation IDs in logs, carried from API to worker | Partial | 4, 9 | API side done (D13): `X-Correlation-ID`, on every API log line, stored on the notification; worker logs it from Phase 6/9 |
| OBS-4 | Health check endpoint | Partial | 2, 3 | `GET /health` checks PostgreSQL, 200/503 (done); Redis check in Phase 7 |

## 5. External Provider Integration

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| PRV-1 | Send `POST {to, channel, content}` to the provider (webhook.site) | Todo | 6 | |
| PRV-2 | Handle the expected `202 {messageId, status, timestamp}` response | Todo | 6 | |
| PRV-3 | Provider behind an interface; URL from config; local mock available | Todo | 6 | Default in docker-compose is decision D9 |

## 6. Optional Features

| ID | Feature | Status | Phase | Notes |
|---|---|---|---|---|
| OPT-1 | Failure handling | Todo | 8 | Failed state + re-queue, circuit breaker, graceful worker shutdown |
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
| DEL-3 | Docker Compose: one-command setup (`docker compose up`) | Partial | 2 | Dockerfile + compose with api, PostgreSQL, Redis (done); worker and migrations join in later phases |
| DEL-4 | API documentation: Swagger/OpenAPI specification | Partial | 4–15 | `docs/openapi.yaml` (OpenAPI 3.0.3) covers create, get and health; tests keep it in sync with the router and real responses; grows with each endpoint; Swagger UI in Phase 15 |
| DEL-5 | Database migrations: versioned schema changes | Done | 3 | golang-migrate, numbered up/down SQL files, applied automatically by Compose; new migrations added as features need them |
| DEL-6 | Test suite runnable with a single command | Partial | all | `make test` (race detector) and `make check` (done); repository tests use testcontainers and need Docker; grows with each step |

## 8. Implied requirements (not written, but expected)

| ID | Requirement | Status | Phase | Notes |
|---|---|---|---|---|
| IMP-1 | The rate limit holds across **all** worker processes, not per worker | Todo | 7 | |
| IMP-2 | Two workers never claim the same notification | Todo | 6 | |
| IMP-3 | Saving and enqueuing are atomic (no dual-write problem) | Done | 3, 5 | One table: inserting a row is enqueuing it; a batch is one statement, so all or none of it is saved |
| IMP-4 | Cancel and worker claim can't both win (no race) | Todo | 5 | Conditional `UPDATE` |
| IMP-5 | Graceful shutdown of API and workers (finish in-flight work on SIGTERM) | Partial | 2, 8 | API (done); worker pending |
| IMP-6 | Low-priority messages are not starved forever | Todo | 6 | Depends on D7 |
| IMP-7 | Config only via environment variables; invalid config fails at startup | Done | 2 | `internal/config` |
| IMP-8 | Batch of 1000 is inserted efficiently (one transaction, bulk insert) | Done | 5 | One `INSERT ... SELECT FROM unnest(...)` statement; 1,000 rows in about 50 ms in tests |
| IMP-9 | Burst test proves the 100/s cap holds under load | Todo | 15 | |
