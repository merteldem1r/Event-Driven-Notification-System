# Plan

The build plan, phase by phase, in **small steps**. Each step ends with something that runs and
is tested. Then we stop, explain it, and the user commits it.

**Update this file after every step.** Requirement IDs (e.g. `API-1`) refer to
[`requirements.md`](requirements.md). Decisions (e.g. `D4`) refer to
[`application/02-design-decisions.md`](application/02-design-decisions.md).

Status: `[x]` done, `[ ]` not done. The next step is marked **(next)**.

> **Current position:** Phase 7, step **7.2** (worker waits for a token before each send).

---

## Phase 0 · Understand the product
- [x] 0.1 Read the requirements; explain the product from scratch (`docs/application/01-understanding-the-project.md`)
- [x] 0.2 Commit-message hook + CI check (Conventional Commits)

## Phase 1 · Key design decisions
- [x] 1.1 D1 queue = PostgreSQL, D2 rate limiter = Redis, D3 tooling (`docs/application/02-design-decisions.md`)

## Phase 2 · Project skeleton (done)
Goal: a running service in Docker with the basic tooling around it.
- [x] 2.1 API server: config from env, JSON logging, chi router (`/health`, `/api/v1` group), timeouts, graceful shutdown, first test
- [x] 2.2 Dockerfile for the API (multi-stage build, distroless non-root image, `.dockerignore`)
- [x] 2.3 Request-logging middleware: one JSON log line per request (method, path, status, bytes, duration)
- [x] 2.4 `docker-compose.yml`: api + PostgreSQL + Redis, with health checks and a data volume; Makefile shortcuts (`up`, `down`, `infra-up`, `logs`)
- [x] 2.5 Linting (golangci-lint in Docker, pinned version) + CI workflow running vet, lint and tests; `make lint`, `make check`

**Verify:** `docker compose up` starts everything; `GET /health` → 200; CI green.
**Summary:** `docs/application/03-project-skeleton.md`.

## Phase 3 · Database (done)
Goal: the schema exists and Go code can read/write it, tested against a real Postgres.
- [x] 3.1 pgx connection pool, `DATABASE_URL` config (required), API fails fast without a database, `/health` checks the DB (200/503)
- [x] 3.2 golang-migrate as a one-off compose service (API waits for it); first migration: `notifications` table with queue columns, constraints and indexes; `make migrate-up` / `migrate-down`
- [x] 3.3 Domain `Notification` type (channel, priority, status); repository: insert + get by ID; integration tests against a real PostgreSQL with testcontainers

**Verify:** migrations run on `docker compose up`; repository tests pass against a real database.
**Summary:** `docs/application/04-database.md`.

## Phase 4 · API: create and get one notification (done)
- [x] 4.1 Validation rules for notifications: required fields, allowed values, recipient format, content limits per channel (D4 decided); all field errors returned together
- [x] 4.2 Notification service; error envelope (D11); strict JSON body reading (1 MB cap, unknown fields rejected); `POST /api/v1/notifications` → `202` with the full notification and `Location`; priority defaults to `normal` (API-1, PROC-4)
- [x] 4.3 `GET /api/v1/notifications/{id}` → `200` with the notification; unknown ID → `404 not_found`, malformed ID → `400 invalid_id` (API-3)
- [x] 4.4 Idempotency key (D12): optional `Idempotency-Key` header; replay returns the original `202` + `Idempotent-Replayed: true`; different body → `422`; `INSERT ... ON CONFLICT DO NOTHING`; concurrency test against real Postgres (PROC-5, API level)
- [x] 4.5 JSON error envelope for unknown routes (`404 route_not_found`) and wrong methods (`405 method_not_allowed` with an `Allow` header), so every error follows D11
- [x] 4.6 Correlation ID (D13): `X-Correlation-ID` middleware (client ID kept if safe, else a new UUID), returned on every response, added to every log line by `internal/logging`, stored on the notification (OBS-3, API side)
- [x] 4.7 OpenAPI 3.0.3 spec for these endpoints, plus tests that it is valid, lists exactly the router's routes, and matches real responses (DEL-4)

**Verify:** create via Postman → 202; get → `pending`; same idempotency key → same notification; invalid input → 400.
**Summary:** `docs/application/05-api-create-and-get.md`.

## Phase 5 · API: batch, list, cancel (done)
- [x] 5.1 Batch create (D5): `POST /api/v1/notifications/batch`, 1 to 1,000 items, all-or-nothing validation with positional errors, one `unnest` insert statement, `202 {batch_id, count, ids}` (API-2, IMP-8)
- [x] 5.2 Batch idempotency: `Idempotency-Key` on batches, item keys `<key>#<i>`, insert in a transaction rolled back if any key is taken; replay only for the same items and count (PROC-5)
- [x] 5.3 `GET /api/v1/batches/{id}`: total and a count per status (zeros included), one `GROUP BY` on the batch index; unknown → `404`, malformed → `400` (API-4)
- [x] 5.4 `GET /api/v1/notifications`: filters `status`, `channel`, `batch_id`, `from`/`to`; cursor pagination (D6) on a new `(created_at, id)` index (API-6, API-7)
- [x] 5.5 `POST /api/v1/notifications/{id}/cancel` with one conditional `UPDATE`; too late → `409`, repeat → `200` (D14) (API-5, IMP-4)
- [x] 5.6 `POST /api/v1/batches/{id}/cancel`: one conditional `UPDATE` on the batch; returns `cancelled` and the counts afterwards; nothing left → `200` with `0` (API-5)

**Verify:** batch of 1,000 → `202`; batch status counts; list pages with filters; cancel → `cancelled`, too late → `409`.
**Summary:** `docs/application/06-api-batch-list-cancel.md`.

## Phase 6 · Worker: the end-to-end flow (done)
- [x] 6.1 `cmd/worker` + `internal/worker`: N loops (`WORKER_CONCURRENCY`), idle polling (`WORKER_POLL_INTERVAL`), graceful shutdown on SIGINT/SIGTERM: stop taking work, let in-flight work finish within `WORKER_SHUTDOWN_TIMEOUT`, then cancel it; `make run-worker`
- [x] 6.2 Claim with `FOR UPDATE SKIP LOCKED` (row becomes `processing`, attempts + 1, leased for `WORKER_LEASE_DURATION`); weighted priority 6:3:1 with fallback (D7); claim logs carry the API correlation ID (PROC-1, PROC-3, IMP-2, IMP-6)
- [x] 6.3 Provider HTTP client (`internal/provider`): `POST {to, channel, content}`, reads `202 {messageId, status, timestamp}`, notification ID as `Idempotency-Key`, `PROVIDER_TIMEOUT`; `PROVIDER_URL` = your webhook.site URL (D9: webhook.site only for now) (PRV-1, PRV-2)
- [x] 6.4 Send each claimed notification and record the result: `sent` + provider `messageId`, `failed` + `last_error` (retries come in Phase 8), or back to `pending` if shutdown cut the send off; recorded even during shutdown; only the claim's own attempt can record (fencing); worker requires `PROVIDER_URL` (RET-6, PRV-1, PRV-2)
- [x] 6.5 Worker service in docker-compose: one image with both programs (`entrypoint: /worker`), `PROVIDER_URL` from the shell or `.env`, `stop_grace_period: 30s`, scalable with `--scale worker=N`

**Verify:** create a notification → a worker sends it → status becomes `sent`; request visible in webhook.site or mock.
**Summary:** `docs/application/07-worker.md`.

## Phase 7 · Rate limiting
- [ ] 7.0 (Optional, D9) Own local fake provider: a small HTTP server answering like webhook.site, with settings for latency and a failure rate; used when `PROVIDER_URL` is unset. Decide before 7.3, whose load test may exceed webhook.site's free request limit, and before Phase 8's failure tests
- [x] 7.1 Redis client (`database.ConnectRedis`) + shared per-channel limiter (`internal/ratelimit`, D15: GCRA via `redis_rate`, 100/s, burst 10, fail closed); `REDIS_URL`, `RATE_LIMIT_PER_SECOND`, `RATE_LIMIT_BURST`; tests against a real Redis, including two limiters on separate clients staying at 100/s together (PROC-2, IMP-1)
- [ ] 7.2 Worker waits for a token before each send; worker requires `REDIS_URL`; Compose worker gets `REDIS_URL` and waits for Redis; Redis in `/health` (OBS-4) **(next)**
- [ ] 7.3 Test with several workers: total stays ≤ 100/s per channel (IMP-1)

## Phase 8 · Retries and failure handling
- [ ] 8.1 Classify provider errors (RET-1)
- [ ] 8.2 Backoff + jitter via `next_attempt_at`; respect `Retry-After` (decide D8) (RET-2, RET-4)
- [ ] 8.3 Max attempts → `failed` with last error (RET-3)
- [ ] 8.4 Lease expiry recovers stuck jobs (RET-5)
- [ ] 8.5 Re-queue endpoint for failed notifications; circuit breaker (OPT-1)

## Phase 9 · Observability
- [ ] 9.1 Prometheus metrics: queue depth, sent/failed counters, latency histograms (OBS-1)
- [ ] 9.2 `/metrics` on API and worker
- [x] 9.3 Correlation ID carried into worker logs (OBS-2, OBS-3); done early in 6.2

## Phase 10–14 · Optional features
- [ ] 10 Scheduled notifications (OPT-2)
- [ ] 11 Templates with variable substitution (OPT-3)
- [ ] 12 WebSocket status updates (OPT-4)
- [ ] 13 Distributed tracing with OpenTelemetry + Jaeger (OPT-5)
- [ ] 14 Full CI/CD: race detector, vulnerability check, image build (OPT-6)

## Phase 15 · Proof and polish
- [ ] 15.1 Load test: burst proves the 100/s cap holds (IMP-9)
- [ ] 15.2 Final README: setup, architecture, API examples (DEL-2)
- [ ] 15.3 OpenAPI complete + Swagger UI (DEL-4)
