# 02 — Design Decisions

> **Goal of this document:** choose the technologies that shape everything else, *before*
> writing code. Each decision lists the options, the trade-offs, and a recommendation.
> D1–D3 were decided on 2026-11-19. D4–D9 get decided in the phase that needs them.

---

## Decision status

| # | Decision | When | Status |
|---|---|---|---|
| D1 | Queue technology | **Now** | Decided: PostgreSQL |
| D2 | Where the shared rate limiter lives | **Now** | Decided: Redis |
| D3 | Tooling (router, DB library, migrations, logging, tests, …) | **Now** (quick confirm) | Decided: as recommended |
| D4 | Validation rules and content limits per channel | Phase 4 (create API) | Decided: see D4 below |
| D5 | Batch validation: all-or-nothing vs partial | Phase 5 (batch API) | Later |
| D6 | Pagination: offset vs cursor | Phase 5 (list API) | Later |
| D7 | Priority strategy: strict vs weighted | Phase 6 (worker) | Later |
| D8 | Retry numbers: attempts, delays, lease time | Phase 8 (retries) | Later |
| D9 | Default provider in docker-compose: mock vs webhook.site | Phase 2/6 | Later |
| D10 | Loading a local `.env.local` for development | Phase 2 | Decided: godotenv |
| D11 | API error format, create response, unknown JSON fields | Phase 4 (create API) | Decided: own envelope, full notification, reject |

**Why only D1–D3 now?** They decide *what the database schema looks like, how workers are
written, and what runs in docker-compose*. Changing them later means rewriting a lot.
D4–D9 are *policies* inside the code. They're easy to change, and much easier to decide when
we're looking at the actual code.

---

## D1 — Queue technology (the big one)

### What our queue must be able to do

Start from the needs, not the tools. From [01-understanding-the-project.md](01-understanding-the-project.md):

| # | Need | Why |
|---|---|---|
| 1 | **Priority:** high before normal before low | Requirement |
| 2 | **Delayed jobs:** "try again in 8 seconds" | Retry with backoff |
| 3 | **Scheduled jobs:** "send tomorrow at 09:00" | Optional feature |
| 4 | **Cancel** a job that's waiting | Requirement |
| 5 | **No double-claim:** two workers never take the same job | No duplicate sends |
| 6 | **Crash recovery:** a job held by a dead worker comes back | Reliability |
| 7 | **Queue depth** is easy to count | Metrics requirement |
| 8 | **Save + enqueue together:** never "saved but not queued" or the reverse | Reliability (see below) |

### The key concept: the "dual-write problem"

When a notification arrives, the API must do **two things**: save it in the database (so
status can be queried) and put it on the queue (so a worker sends it).

If the database and the queue are **two different systems**, those two writes can't happen
atomically (all-or-nothing):

```
API:  1. INSERT into Postgres     OK
      -- crash / network error here --
      2. PUSH to RabbitMQ/Redis  never happens → saved as "pending" forever, never sent
```

Swapping the order doesn't help either: push first, crash, and then a worker sends a message the
database has never heard of.

The standard fix is the **outbox pattern**: write the row *and* an "outbox" row in one DB
transaction, and a separate relay process copies outbox rows to the broker. It works, but
it's an extra moving part.

**If the database *is* the queue, the problem disappears.** Saving the row *is* enqueuing it,
because it's one `INSERT` in one transaction.

### Option A — PostgreSQL as the queue (`FOR UPDATE SKIP LOCKED`)

The `notifications` table itself is the queue. A job is "in the queue" when
`status = 'pending'` and its time has come. A worker claims one like this (sketch):

```sql
UPDATE notifications
SET    status = 'processing',
       locked_until = now() + interval '30 seconds',   -- the lease (crash recovery)
       attempts = attempts + 1
WHERE  id = (
    SELECT id FROM notifications
    WHERE  status = 'pending'
      AND  next_attempt_at <= now()                     -- retries + scheduling
    ORDER  BY priority, next_attempt_at                 -- priority
    FOR UPDATE SKIP LOCKED                              -- no double-claim
    LIMIT  1
)
RETURNING *;
```

**`SKIP LOCKED` in plain words:** think of a supermarket with several cashiers. A worker
grabs a row and locks it. Other workers *don't wait* for that row. They skip it and grab the
next unlocked one. So 10 workers claim 10 different jobs in parallel, and none of them ever gets
the same one.

How each need maps onto it:
- Priority → `ORDER BY priority`
- Delayed retry and scheduling → one column, `next_attempt_at`
- Cancel → `UPDATE … SET status='cancelled' WHERE id=$1 AND status='pending'` (atomic, so it can't race the worker)
- Crash recovery → a job whose `locked_until` has passed is claimable again
- Queue depth → `SELECT count(*) … WHERE status='pending'`
- Save + enqueue → the same `INSERT`

**Downsides:**
- Workers **poll** ("anything for me?") when idle, e.g. every 200 ms. That's cheap, and Postgres
  `LISTEN/NOTIFY` can wake workers instantly if we want.
- At very large scale (tens of thousands of jobs per second) a dedicated broker is better. Our cap is
  3 × 100/s = 300/s, far below what Postgres handles.
- It doesn't *look* like a classic message broker. We must be ready to explain why it's still
  event-driven: the API and workers are decoupled by the queue, and the queue just lives in Postgres.

### Option B — Redis (e.g. with the `asynq` library)

Redis is a very fast in-memory store. Libraries like `asynq` build a full job queue on it:
priority queues, retries with backoff, scheduled tasks and a dashboard, all built in.

- Pro: Very fast, with features ready-made.
- Con: **Dual-write problem** (Postgres for status + Redis for the queue), so we'd need an outbox.
- Con: Two sources of truth for status that can drift apart.
- Caveat: The interesting logic (priority, retry, claiming) lives *inside the library*, so there's less of
  our own design to show and explain.

### Option C — RabbitMQ

A real message broker. Producers publish, consumers receive and **acknowledge** messages, and
unacknowledged messages are redelivered.

- Pro: Built for this. It has priority queues, acks, and dead-letter exchanges for failed messages.
- Con: **Dual-write problem**, so we'd need an outbox.
- Con: **Cancel is awkward:** you can't pull one message out of the middle of a queue. The worker
  has to check the DB ("still pending?") before sending.
- Caveat: Delayed retries and scheduling need workarounds (TTL + dead-letter tricks, or a plugin).
- Caveat: One more service to run and monitor.

### Option D — Kafka

A distributed *log* built for enormous throughput and replaying history.

- Con: No per-message priority (you'd need separate topics), no per-message delay, and no
  per-message acknowledgment. It's a poor fit for "retry *this one* in 8 seconds".
- Con: Heavy to run. Overkill for 300 msg/s.
- Listed so we can explain **why not**, which is a fair question to expect.

### Scorecard

| Need | A · PostgreSQL | B · Redis (asynq) | C · RabbitMQ | D · Kafka |
|---|---|---|---|---|
| Priority | Yes, `ORDER BY` | Yes, built in | Yes, priority queues | No, separate topics |
| Delayed retry | Yes, one column | Yes, built in | Partly, workarounds | No, retry topics |
| Scheduled send | Yes, same column | Yes, built in | Partly, plugin | No |
| Cancel | Yes, atomic update | Partly, delete task + sync DB | No, check DB on consume | No, check DB on consume |
| Save + enqueue atomically | Yes, one transaction | No, needs outbox | No, needs outbox | No, needs outbox |
| No double-claim | Yes, `SKIP LOCKED` | Yes | Yes, acks | Yes, partitions |
| Crash recovery | Yes, lease | Yes | Yes, redelivery | Yes, offsets |
| Queue depth | Yes, `count(*)` | Yes | Yes, management API | Partly, consumer lag |
| Extra services | none | Redis | RabbitMQ | Kafka |
| Throughput ceiling | thousands/s (enough) | very high | very high | extreme |
| Logic visible in our code | Yes, all ours | Partly, in library | mixed | mixed |

### Recommendation: **A, PostgreSQL as the queue**, behind a `Queue` interface

- It meets every need, and the **dual-write problem simply doesn't exist**.
- Priority, retries, scheduling and cancel are each **one column or one `WHERE` clause**.
- Fewer moving parts, so `docker-compose up` is simpler and more reliable.
- All the queue logic is **our own code**, which we can explain line by line.
- **Keeping the door open:** workers talk to a Go `Queue` interface, not to SQL directly. If
  traffic ever outgrew Postgres, a RabbitMQ/Kafka implementation (plus an outbox) could replace it
  without touching the API or the worker logic.

**What we give up:** raw throughput far beyond our needs, and the "classic broker" look.

---

## D2 — Where the shared rate limiter lives

Reminder: the limit is **100/s per channel for all workers together**, so the counter must be
shared by every worker process.

| Option | How | Pros | Cons |
|---|---|---|---|
| **Redis** | Token bucket / GCRA in Redis (e.g. the `go-redis/redis_rate` library). Each worker asks Redis "may I send one SMS?" | Atomic, fast, the industry-standard tool for this. Redis can also carry the live updates for the WebSocket feature later. | One more service in docker-compose. |
| **PostgreSQL** | One row per channel holding tokens + last refill time, updated atomically | No extra service | An extra DB write per message, and every worker fights over 3 hot rows (lock contention). |
| **In-process** (`golang.org/x/time/rate`) | Each worker process keeps its own limiter | Simplest | **Wrong** as soon as there are 2+ worker processes (each allows 100/s). This is exactly the trap from section 5.2 of doc 01. |

**Recommendation: Redis.** The in-process limiter is the tempting wrong answer. It *looks* correct
with one worker and silently breaks when you scale. Redis is the standard shared limiter and
it's also useful for WebSockets later.

---

## D3 — Tooling (quick confirm)

These have well-known, sensible defaults in Go. Recommendation first; alternatives in brackets.

| Area | Recommendation | Why | (Alternatives) |
|---|---|---|---|
| HTTP router | **chi** | Small, built on the standard library, good middleware (request ID, panic recovery, timeouts) | (Go 1.22+ standard `net/http` mux, Gin, Echo) |
| DB driver | **pgx v5**, hand-written SQL | Fastest Postgres driver; queue queries stay visible and explainable | (sqlc code generation, GORM ORM: hides SQL, bad fit for `SKIP LOCKED`) |
| Migrations | **golang-migrate** | Numbered `up`/`down` SQL files, runs as a one-off container in docker-compose | (goose, Atlas) |
| Logging | **log/slog** (standard library) | Structured JSON logs built into Go; no dependency | (zap, zerolog) |
| Metrics | **prometheus/client_golang** | The standard `/metrics` format | (OpenTelemetry metrics) |
| Config | **Environment variables** | Twelve-factor style; works naturally with docker-compose | (config files, Viper) |
| Tests | **Go `testing`** + **testcontainers-go** | Integration tests start a real Postgres/Redis in Docker, so we test the real SQL | (mocks only: wouldn't catch SQL/locking bugs) |
| API docs | **Hand-written OpenAPI** (`docs/openapi.yaml`) + Swagger UI | The spec is a design document; we write it as we build each endpoint | (generate from code comments with swag) |
| Process layout | **Two programs:** `cmd/api` and `cmd/worker`, shared `internal/` | Scale API and workers separately; a crashing worker never takes down the API | (one binary with modes) |
| Commit style | **Conventional Commits** | Already enforced by the hook + CI | — |

---

## D4 — Validation rules and content limits

Decided at the start of Phase 4. The rules live in `domain.Notification.Validate`, so the API,
the batch endpoint and any later feature share them. The database `CHECK` constraints stay as a
second line of defense.

| Question | Options | Chosen |
|---|---|---|
| Content length | One SMS segment (160) / total cap / encoding-aware segment counting | **Total cap per channel:** SMS 1,600, push 500, email 100,000 characters. Blank content is rejected |
| How to count | Bytes / characters | **Characters** (`utf8.RuneCountInString`): `"ş"` is 2 bytes but 1 character |
| Recipient | Format checks / full libraries (libphonenumber) | **Format checks:** E.164 regex for SMS, `net/mail` exact match for email, push token of 1 to 512 characters without spaces |
| Priority | Required / optional with a default | **Optional, defaults to `normal`** when the request leaves it out; unknown values are rejected |
| Error reporting | First error / all errors | **All errors at once** as a list of `{field, message}` |

Why: 1,600 characters (about 10 segments) is the cap large SMS providers use, and the provider
splits long text. Push stays far below the ~4 KB payload limit even with 4-byte characters. Email
has no real limit, so the cap only protects storage. Format checks catch real mistakes with the
standard library only; whether a number or address exists, only the provider can know.

What we give up: clients aren't told how many SMS segments (and so what cost) a message has, and
a well-formed but non-existent number passes validation. Segment counting can be added later.

---

## Resulting architecture

```
                    docker-compose up
 ┌─────────┐   ┌──────────────┐     ┌──────────────────────────────┐
 │ client  │──►│  api (chi)   │────►│  PostgreSQL                   │
 └─────────┘   └──────┬───────┘     │  notifications table = queue │
                      │ /metrics    │  (SKIP LOCKED, leases)        │
                      │ /health     └──────────────▲───────────────┘
                                                   │ claim / update
                                    ┌──────────────┴───────────────┐    ┌─────────┐
                                    │  worker × N                   │───►│  Redis  │ rate limiter
                                    └──────────────┬───────────────┘    └─────────┘ (+ WebSocket later)
                                                   ▼
                                    ┌──────────────────────────────┐
                                    │ provider: webhook.site / mock │
                                    └──────────────────────────────┘
 + migrate (one-off container that applies migrations, then exits)
```

---

## Decision log

| # | Decision | Chosen | Why | What we give up | Date |
|---|---|---|---|---|---|
| D1 | Queue technology | **PostgreSQL** (`FOR UPDATE SKIP LOCKED`), behind a Go `Queue` interface | No dual-write problem; priority, retries, scheduling and cancel are each a column or a `WHERE`; one less service; all queue logic is our own code | Throughput far beyond 300/s; the "classic broker" look | 2026-11-19 |
| D2 | Rate limiter location | **Redis** (shared token bucket / GCRA) | Correct across any number of worker processes; standard tool; reused for WebSocket updates later | One more service in docker-compose | 2026-11-19 |
| D3 | Tooling | **chi, pgx v5 + hand-written SQL, golang-migrate, log/slog, prometheus/client_golang, env-var config, testing + testcontainers-go, hand-written OpenAPI, cmd/api + cmd/worker** | Standard, small, explainable Go choices | — | 2026-11-19 |
| D4 | Validation rules | **Per-channel character caps** (SMS 1,600, push 500, email 100,000); E.164 / `net/mail` / token-shape recipient checks; priority defaults to `normal`; all field errors returned at once | Simple, standard library only, matches real provider limits; one response lists every problem (useful for batches) | Segment and cost counting; checking that recipients really exist | 2026-11-20 |
| D10 | Local `.env.local` loading | **godotenv**, via `config.LoadDotEnv` at startup; missing file ignored; never overrides existing variables | Works with `make run-api`, plain `go run` and the IDE debugger; real environment wins over the file; no effect in Docker/production (file not in the image) | One small dependency; one local-only line in startup code (alternative was loading it in the Makefile) | 2026-11-19 |
| D11 | API conventions | **Own error envelope** `{"error": {code, message, details}}` for every error; **202 returns the full notification** plus a `Location` header; **unknown JSON fields are rejected** with 400 | One simple, readable error shape with stable codes; create and get share one response shape; typos surface immediately | The RFC 9457 Problem Details standard; tolerance for clients that send extra fields | 2026-11-20 |
