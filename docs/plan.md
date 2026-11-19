# Plan

The build plan, phase by phase, in **small steps**. Each step ends with something that runs and
is tested. Then we stop, explain it, and the user commits it.

**Update this file after every step.** Requirement IDs (e.g. `API-1`) refer to
[`requirements.md`](requirements.md). Decisions (e.g. `D4`) refer to
[`application/02-design-decisions.md`](application/02-design-decisions.md).

Status: `[x]` done, `[ ]` not done. The next step is marked **(next)**.

> **Current position:** Phase 5, step **5.1** (batch create, decide D5).

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

## Phase 5 · API: batch, list, cancel
- [ ] 5.1 Batch create, up to 1000, efficient insert (decide D5) (API-2, IMP-8) **(next)**
- [ ] 5.2 Get batch status summary (API-4)
- [ ] 5.3 List with filters + pagination (decide D6) (API-6, API-7)
- [ ] 5.4 Cancel with a conditional update (API-5, IMP-4)

## Phase 6 · Worker: the end-to-end flow
- [ ] 6.1 `cmd/worker` skeleton with graceful shutdown
- [ ] 6.2 Claim query with `SKIP LOCKED` + priority (decide D7) (PROC-1, PROC-3, IMP-2)
- [ ] 6.3 Provider interface, webhook.site client, local mock provider (decide D9) (PRV-1..3)
- [ ] 6.4 Send + record result (`sent`, provider `messageId`) (RET-6)
- [ ] 6.5 Worker service in docker-compose

**Verify:** create a notification → a worker sends it → status becomes `sent`; request visible in webhook.site or mock.

## Phase 7 · Rate limiting
- [ ] 7.1 Redis client + shared per-channel limiter (PROC-2)
- [ ] 7.2 Worker waits for a token before each send
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
- [ ] 9.3 Correlation ID carried into worker logs (OBS-2, OBS-3)

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
