# Plan

The build plan, phase by phase, in **small steps**. Each step ends with something that runs and
is tested. Then we stop, explain it, and the user commits it.

**Update this file after every step.** Requirement IDs (e.g. `API-1`) refer to
[`requirements.md`](requirements.md). Decisions (e.g. `D4`) refer to
[`application/02-design-decisions.md`](application/02-design-decisions.md).

Status: `[x]` done, `[ ]` not done. The next step is marked **(next)**.

> **Current position:** optional features, in this order: 10 scheduled notifications (done), 11 templates **(next)**, 14 full CI/CD; then decide on more (WebSocket, 12).

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

## Phase 7 · Rate limiting (done)
- [x] 7.0 Own local fake provider (D9 revised): `cmd/fakeprovider` + `internal/fakeprovider`, answers like webhook.site, `FAKE_PROVIDER_LATENCY` and `FAKE_PROVIDER_FAILURE_RATE`, logs messages per channel per second; Compose service used by the worker unless `PROVIDER_URL` is set; `make run-fakeprovider` (PRV-3)
- [x] 7.1 Redis client (`database.ConnectRedis`) + shared per-channel limiter (`internal/ratelimit`, D15: GCRA via `redis_rate`, 100/s, burst 10, fail closed); `REDIS_URL`, `RATE_LIMIT_PER_SECOND`, `RATE_LIMIT_BURST`; tests against a real Redis, including two limiters on separate clients staying at 100/s together (PROC-2, IMP-1)
- [x] 7.2 Loops per channel (D16): claim filtered by channel on a new index `(channel, priority, next_attempt_at)` (migrations 000003, 000004); one dispatcher per channel with its own 6:3:1 pattern; worker runs `WORKER_CONCURRENCY` loops per channel (default 10); Docker image built with Go 1.26 (needed by go-redis since 7.1)
- [x] 7.3 Each channel loop waits for a rate-limit slot before claiming (`Processor.Wait`, ended by the stop signal; error = take nothing, retry after the poll interval); worker requires `REDIS_URL` and connects at startup; Redis library logs as JSON; Compose worker gets `REDIS_URL` and waits for Redis (PROC-2)
- [x] 7.3.1 Outage logging: an outage (Redis or database down) logs one error when it starts and one `working again` (with `down_for`, `failed_tries`) when every loop works again; retries and the Redis library's own messages at debug level. Measured: a 17 s Redis outage logged 2 lines instead of ~1,400 (OBS-2)
- [x] 7.4 Load test (`make loadtest`, `cmd/loadtest`, `internal/loadtest`): 3 or 5 workers against the fake provider, 1,000 notifications per channel at once; the fake measures the busiest one-second window at arrival (`GET /_stats`), the test fails above 100 (D17). Findings and fixes: provider client reuses connections (36 instead of ~200 per 600 sends); order changed to claim, then slot, then send, with `Unclaim` when there is no slot (D16 revised); 1 s deadline per Redis call; an empty queue no longer ends an outage in the logs; defaults 85/s, burst 5; Compose passes `RATE_LIMIT_*` through. Measured: busiest second 90 to 93 at the provider (IMP-1, IMP-9)

**Verify:** `make loadtest` (also `make loadtest WORKERS=5`) passes: busiest second at the provider at most 100 per channel, every notification sent.
**Summary:** `docs/application/08-rate-limiting.md`.

## Phase 8 · Retries and failure handling (done)
- [x] 8.1 Classify provider errors (D18): `provider.IsTemporary` says temporary for no response, `408`, `429`, `5xx` and a `2xx` without a `messageId`, permanent for `3xx`, other `4xx` and errors before sending; the worker logs `temporary` with each failure (still final until 8.2) (RET-1)
- [x] 8.2 Temporary failures go back to `pending` with `next_attempt_at` moved ahead (`repository.Retry`); waits from `internal/retry` (D8: 10 s doubling to 10 min, equal jitter), provider's `Retry-After` (seconds or date) used when longer; `RETRY_BASE_DELAY`, `RETRY_MAX_DELAY`; API shows `next_attempt_at` while a retry waits. Until 8.3 there is no maximum: a temporary failure keeps retrying, at most every 10 min (RET-1, RET-2, RET-4)
- [x] 8.3 Max attempts (D8: `RETRY_MAX_ATTEMPTS`, default 5): `retry.Policy.Next` says wait or stop; after the last attempt a temporary failure is `failed` with its last error, logged with `reason: no attempts left` (a rejection logs `rejected by the provider`) (RET-3)
- [x] 8.4 Lease recovery (D19): every worker sweeps every `WORKER_RECOVER_INTERVAL` (10s) and puts `processing` rows with an expired lease (60 s, D8) back to `pending` (`repository.RecoverExpired`, partial index in migration 000005); attempt kept, `last_error` says the lease expired; sweep errors join the outage logging. Measured: a worker killed mid-send, its notification sent again as attempt 2 after ~64 s (RET-5)

**Verify:** kill a worker during a send (`docker compose kill worker`), start it again: the notification is `sent` on attempt 2 about a minute later.
**Summary:** `docs/application/09-retries.md`.

Dropped: a circuit breaker and a re-queue endpoint for `failed` notifications. Retries with
backoff and a maximum (8.2, 8.3) already handle a provider outage well enough for this service;
both would add state and edge cases without a requirement asking for them.

**Order after Phase 8:** Phase 9 (required metrics and worker health), then the README and Swagger UI
(15.2, 15.3), then at most one optional feature (scheduled notifications is the cheapest).

## Phase 9 · Observability (done)
- [x] 9.1 API metrics (D20): `GET /metrics` (`internal/metrics`, own registry): `notifications_queue_depth{channel,status}` and `notifications_queue_oldest_due_seconds{channel}` counted in the database on each scrape (`repository.QueueStats`); `http_requests_total` and `http_request_duration_seconds` per route pattern; Go runtime metrics; documented in OpenAPI (OBS-1)
- [x] 9.2 Worker HTTP server (`WORKER_HTTP_PORT`, 8081; Compose publishes 8081-8089 for scaled workers): `/metrics` with `notifications_send_total{channel,result}`, `notifications_provider_request_duration_seconds`, `notifications_delivery_duration_seconds`, `notifications_leases_recovered_total` (D20, `metrics.Sends`); `/health` checking PostgreSQL and Redis, 503 naming the one that is down (D16) (OBS-1, OBS-4)

**Verify:** `curl localhost:8080/metrics` (queue, requests) and `curl localhost:8081/metrics` (sends); `docker compose stop redis` turns `curl localhost:8081/health` into 503 with `"redis":"unreachable"`.
**Summary:** `docs/application/10-observability.md`.
- [x] 9.3 Correlation ID carried into worker logs (OBS-2, OBS-3); done early in 6.2

## Phase 10–14 · Optional features
Chosen: 10, 11 and 14, in that order (cheapest first, CI/CD last so it covers the new code); 12
(WebSocket) to be decided after them; 13 (tracing) not planned.

- [x] 10 Scheduled notifications (D21): optional `scheduled_at` on single and batch creates
  (RFC 3339 with a time zone), stored in a new column (migration 000006) with `next_attempt_at`
  starting there, so the queue holds it until then; future only, at most 30 days ahead, else
  `400`; shown in responses; delivery-time metric counts from it. Measured: scheduled 15 s
  ahead, sent 0.44 s after its time (OPT-2). Summary: `docs/application/12-scheduled-notifications.md`
- [ ] 11 Templates with variable substitution (OPT-3)
- [ ] 12 WebSocket status updates (OPT-4)
- [ ] 13 Distributed tracing with OpenTelemetry + Jaeger (OPT-5)
- [ ] 14 Full CI/CD: race detector, vulnerability check, image build (OPT-6)

## Phase 15 · Proof and polish (done)
- [x] 15.1 Load test: burst proves the 100/s cap holds (IMP-9); done early in 7.4
- [x] 15.2 README: the idea and building blocks, getting started (Docker and local), how it works step by step with diagrams (flow, priority, shared rate limit, retries), key decisions, failure behavior, measured results with charts, reference (API, monitoring, tests, configuration) (DEL-2)
- [x] 15.3 Swagger UI at `GET /docs` (pinned Swagger UI from jsDelivr with integrity hashes) and the spec at `GET /openapi.yaml`, built into the binary from `docs/openapi.yaml` (`docs` package); both documented in the spec; a test checks the served spec equals the file (DEL-4)

**Summary:** `docs/application/11-proof-and-polish.md`.
