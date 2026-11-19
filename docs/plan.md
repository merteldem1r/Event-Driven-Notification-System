# Plan

The build plan, phase by phase, in **small steps**. Each step ends with something that runs and
is tested. Then we stop, explain it, and the user commits it.

**Update this file after every step.** Requirement IDs (e.g. `API-1`) refer to
[`requirements.md`](requirements.md). Decisions (e.g. `D4`) refer to
[`application/02-design-decisions.md`](application/02-design-decisions.md).

Status: `[x]` done, `[ ]` not done. The next step is marked **(next)**.

> **Current position:** Phase 2, step **2.3** (docker-compose with API, PostgreSQL and Redis).

---

## Phase 0 · Understand the product
- [x] 0.1 Read the requirements; explain the product from scratch (`docs/application/01-understanding-the-project.md`)
- [x] 0.2 Commit-message hook + CI check (Conventional Commits)

## Phase 1 · Key design decisions
- [x] 1.1 D1 queue = PostgreSQL, D2 rate limiter = Redis, D3 tooling (`docs/application/02-design-decisions.md`)

## Phase 2 · Project skeleton
Goal: a running service in Docker with the basic tooling around it.
- [x] 2.1 API server: config from env, JSON logging, chi router (`/health`, `/api/v1` group), timeouts, graceful shutdown, first test
- [x] 2.2 Dockerfile for the API (multi-stage build, distroless non-root image, `.dockerignore`)
- [ ] 2.3 `docker-compose.yml`: api + PostgreSQL + Redis, with health checks **(next)**
- [ ] 2.4 Linting (golangci-lint) + CI job running vet, lint and tests

**Verify:** `docker compose up` starts everything; `GET /health` → 200; CI green.

## Phase 3 · Database
Goal: the schema exists and Go code can read/write it, tested against a real Postgres.
- [ ] 3.1 pgx connection pool, `DATABASE_URL` config, `/health` checks the DB
- [ ] 3.2 golang-migrate as a one-off compose service; first migration (`notifications` table with queue columns)
- [ ] 3.3 Repository: insert + get by ID; integration tests with testcontainers

**Verify:** migrations run on `docker compose up`; repository tests pass against a real database.

## Phase 4 · API: create and get one notification
- [ ] 4.1 Domain types (Channel, Priority, Status) + validation rules (decide D4)
- [ ] 4.2 Standard JSON error format + `POST /api/v1/notifications` → `202` (API-1, PROC-4)
- [ ] 4.3 `GET /api/v1/notifications/{id}` (API-3)
- [ ] 4.4 Idempotency key (PROC-5, API level)
- [ ] 4.5 Correlation ID middleware + request logging (OBS-3)
- [ ] 4.6 OpenAPI spec for these endpoints (DEL-4)

**Verify:** create via Postman → 202; get → `pending`; same idempotency key → same notification; invalid input → 400.

## Phase 5 · API: batch, list, cancel
- [ ] 5.1 Batch create, up to 1000, efficient insert (decide D5) (API-2, IMP-8)
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
