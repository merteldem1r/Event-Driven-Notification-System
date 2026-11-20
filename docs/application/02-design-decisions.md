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
| D5 | Batch validation: all-or-nothing vs partial | Phase 5 (batch API) | Decided: all-or-nothing |
| D6 | Pagination: offset vs cursor | Phase 5 (list API) | Decided: cursor |
| D7 | Priority strategy: strict vs weighted | Phase 6 (worker) | Decided: weighted 6:3:1 |
| D8 | Retry numbers: attempts, delays, lease time | Phase 8 (retries) | Decided: 5 attempts; 10 s doubling to 10 min, equal jitter; longer `Retry-After` wins, same cap; lease in 8.4 |
| D9 | Default provider in docker-compose: mock vs webhook.site | Phase 6 (provider), revised in Phase 7 | Decided: own fake provider by default, webhook.site via `PROVIDER_URL` |
| D10 | Loading a local `.env.local` for development | Phase 2 | Decided: godotenv |
| D11 | API error format, create response, unknown JSON fields | Phase 4 (create API) | Decided: own envelope, full notification, reject |
| D12 | Idempotency key behavior | Phase 4 (create API) | Decided: see D12 below |
| D13 | Correlation ID header and client IDs | Phase 4 (create API) | Decided: `X-Correlation-ID`, accept safe client IDs |
| D14 | Cancel API | Phase 5 (cancel) | Decided: `POST .../{id}/cancel`, `409` when too late, repeat is `200` |
| D15 | Rate limiter details: algorithm, burst, Redis down | Phase 7 (rate limiting) | Decided: GCRA via `redis_rate`, burst 5, fail closed |
| D16 | When a worker asks the limiter: after or before claiming | Phase 7 (rate limiting) | Decided: loops per channel; claim, then slot, then send (revised in 7.4) |
| D17 | What "max 100 per second" means, and the headroom | Phase 7 (load test) | Decided: at most 100 at the provider in any one second; send at 85/s, burst 5 |
| D18 | Which provider errors are worth retrying | Phase 8 (retries) | Decided: no response, 408, 429, 5xx, and 2xx without a receipt; not 3xx or other 4xx |

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

## D6 — Pagination

The list endpoint returns notifications newest first, a page at a time. The table is large and
grows constantly at the newest end, which is the case offset pagination handles worst:

- **Offset** (`?page=3`): Postgres reads and discards every row before the page, so deep pages get
  slower; and rows arriving between requests push older rows into the next page (duplicates) or
  past it (gaps).
- **Cursor** (`?cursor=...`): the client sends back the position of the last row it saw, and the
  query continues from there with `WHERE (created_at, id) < (cursor)` on an index over
  `(created_at, id)`. Every page costs the same; new rows are newer than any cursor, so they never
  shift later pages. There is no "jump to page N".

Measured on 100,000 rows at page 1,801 (limit 50): offset read 90,050 index entries (12.6 ms),
the cursor read 50 (0.06 ms).

Details: the cursor is base64url of `{created_at, id}` in UTC, opaque to clients; `id` breaks ties
(a batch shares one `created_at`); `limit` 1 to 100, default 50; one extra row is fetched to know
whether a next page exists; no total count, since `count(*)` over a large table on every request
is expensive and `GET /batches/{id}` already counts a batch. Filters (`status`, `channel`,
`batch_id`, `from` inclusive, `to` exclusive) are optional and combine with AND; one static query
switches each off with `$n IS NULL OR ...`.

---

## D7 — Priority strategy

Workers must prefer `high`, but `low` must not wait forever while `high` keeps arriving (IMP-6).

- **Strict:** always the highest priority waiting. Fastest for `high`, but `low` can starve.
- **Weighted:** of every 10 claims, 6 try `high` first, 3 `normal`, 1 `low`; an empty preferred
  queue falls back to the others, so no capacity is wasted.
- **Strict with aging:** strict, but long-waiting rows count as `high`. Starvation is time-limited,
  at the cost of another index and query.

Chosen: **weighted 6:3:1**. The pattern is interleaved (`H N H H L H N H N H`) and shared by all
loops of a worker process through an atomic counter. Each claim is one
`UPDATE ... WHERE id = (SELECT ... WHERE priority = $1 ... FOR UPDATE SKIP LOCKED)` on the queue
index, tried per priority in the claim's order. Measured with two worker processes on 100
notifications of each priority: the first 160 claims were 96 high, 48 normal, 16 low.

---

## D12 — Idempotency key

Decided in Phase 4. **Problem:** a client can't tell a lost request from a lost response, so it
retries, and a retry of `POST` creates a duplicate notification. **Idea:** the client names each
logical action with an `Idempotency-Key` header and reuses it on every retry; the server stores it
and never creates a second notification for the same key.

| Question | Options | Chosen |
|---|---|---|
| Repeated key, same request | Return the original / `409 Conflict` | **Return the original:** same `202` and body, plus `Idempotent-Replayed: true` |
| Repeated key, different request | Ignore and return the original / reject | **Reject with `422 idempotency_key_reused`**; "same" compares channel, recipient, content and priority (after the priority default) |
| Key rules and lifetime | Used while the row exists / expires after 24 h | **Optional header, 1 to 255 visible ASCII characters; used while its notification exists** |

**Concurrency.** "Check, then insert" races: two retries can both see "not found" and both insert.
Instead the `UNIQUE` index decides in one step: `INSERT ... ON CONFLICT (idempotency_key) DO NOTHING
RETURNING ...`. A returned row means this request won; no row means the key is taken, so the service
reads the stored row (in a separate statement, so it sees the winner's committed row) and replays it.

**Batches** (added in Phase 5) use the same header for the whole batch. Item `i` is stored with the
key `<key>#<i>`, so the existing unique index and `ON CONFLICT` cover batches without a schema
change. The insert runs in a transaction: if any item key is taken, everything is rolled back, so a
reused key can never leave a half-saved batch. A repeat counts as the same batch only if it has the
same number of items, each the same request; the lookup also checks one key past the end, so a
shorter request can't replay part of a longer batch.

What we give up: keys can't be reused after a period of time; there are no per-client key scopes
(the system has no client accounts). Duplicates between worker and provider are a separate problem,
handled in Phase 8.

---

## D15 — Rate limiter details

D2 put the limiter in Redis. Three things were left open:

- **Library or own script.** `go-redis/redis_rate` runs the GCRA algorithm as one Lua script,
  so the check and the update are atomic in Redis. Our own token-bucket script would do the same
  with more code to maintain and explain.
- **Burst.** How many sends may go at once after a quiet moment. A burst of 100 sends 100 at
  once, then 1 every 10 ms; a burst of 1 spaces every send exactly 10 ms apart, so loops wait
  even when the system is idle.
- **Redis down.** Fail closed (don't send until Redis answers) or fail open (send without a
  limit).

Chosen: **`redis_rate` (GCRA), fail closed**, burst first 10, then **5** after the load test
(D17). Redis stores one number per channel (key `rate:channel:<channel>`): the time at which
the channel will be "caught up". Each send pushes it one interval further (1 s / rate); a send
is allowed while it is at most burst intervals in the future. The time comes from Redis's own
clock, so worker clocks don't matter. In any one second at most rate + burst sends go out.
Measured with two limiters on separate clients (two processes), 20 loops each, over one
second at 100/s with burst 10: 109 to 110 sends in total.

Why burst 5 and not 1: a loop asks for its slot a millisecond or two late, and with burst 1 a
slot not taken at its moment is lost. At 90/s, burst 1 reached only 83/s; burst 5 reached 90/s.
Each call to Redis has a 1 s deadline: Redis normally answers in about a millisecond, and a
stopped Redis could otherwise keep a call hanging for 15 s or more.

---

## D16 — Where the worker waits for the rate limit

A claim used to take the next notification of any channel. With a per-channel limit, that
decides how one busy channel affects the others, and when the slot is asked for decides how
evenly messages reach the provider.

- **A: one set of loops; claim, then wait for a slot, then send.** A backlog on one channel
  fills every loop with notifications of that channel, all waiting for its slots: 10,000 SMS
  ahead of 10 emails keep the emails waiting ~100 s while the email limit is unused.
- **B: loops per channel; wait for a slot, then claim that channel, then send.** Channels are
  independent, nothing is held while waiting, and a Redis outage claims nothing.
- **C: loops per channel; claim that channel, then wait for a slot, then send.** Channels are
  independent too, and the slot is given right before the request.

Chosen first (7.2, 7.3): **B**. Revised after the load test (7.4): **C**. With B, the database
claim sits between the permission and the request; while PostgreSQL does its normal background
work (autovacuum, checkpoints), claims stall for a moment, the limiter keeps giving slots, and
the stalled loops then send together. At 90/s with burst 5 (at most 95 allowed per second) the
provider saw up to 116 in one second. With C, only the HTTP request sits between permission and
arrival: the same settings gave 95 to 96.

What C needs, and has:

- **Loops per channel** (kept from B): `WORKER_CONCURRENCY` loops per channel (default 10, so
  30 per process), each channel with its own dispatcher and 6:3:1 pattern (D7). The claim
  filters by channel on the index `(channel, priority, next_attempt_at)`.
- **No slot, no attempt:** if the slot can't be had (Redis down, 1 s deadline per call) or
  shutdown cuts the wait off, the notification goes back with `Unclaim`, which also takes the
  attempt off: nothing was sent. A Redis outage therefore uses up no attempts (measured: the
  notification stayed `pending` with 0 attempts, and was sent on attempt 1 once Redis was back).
- **Shutdown:** a claimed notification waiting for its slot is in-flight work, like a send, so
  it finishes within the shutdown timeout; at the deadline it is put back.
- **Idle loops take no slots:** a loop asks for a slot only once it holds a notification.

The cost: a claimed notification holds its lease while it waits for its slot, a fraction of a
second against a 60 s lease. The API's `/health` does not check Redis, since the API doesn't use
it; the worker's own health and metrics endpoint (Phase 9) does.

---

## D17 — What "max 100 messages per second" means

The limiter controls when a send is allowed; the provider counts when a message arrives. In
between are the HTTP request and the network, so arrivals are never spread quite as evenly as
the permissions: a few messages delayed by some milliseconds arrive together with later ones.

- **Average and per clock second:** the easiest to meet, but a strict reader of "max 100" could
  point at a one-second stretch with more.
- **Never more than 100 at the provider in any one-second window, starting at any moment:**
  the strict reading, and the only place the limit can really be judged.

Chosen: **the strict reading, at the provider.** The workers aim below the limit:
`RATE_LIMIT_PER_SECOND` 85 with burst 5, so at most 90 are allowed in any second, leaving room
for the timing spread on the way. The fake provider measures the busiest one-second window per
channel at arrival (`GET /_stats`), and the load test (`make loadtest`) fails if it is above
100. Two more changes came out of the measurements:

- **Connection reuse:** Go keeps only 2 idle connections per host by default, so most of the 30
  loops opened a new connection (and, for webhook.site, a TLS handshake) for every message:
  175 to 238 connections per 600 sends. The provider client now keeps up to 1,000: 36 to 38.
- **Order C of D16**, which removed the largest source of spread (database pauses).

Measured, 1,000 notifications per channel at once on all three channels (busiest second at the
provider):

| Settings (order) | Workers | Busiest second | Average |
|---|---|---|---|
| 100/s, burst 10 (B, 2 connections) | 3 | 119 to 122 | 100/s |
| 90/s, burst 5 (B) | 3 | 96 to 116 | 90/s |
| 90/s, burst 5 (C) | 3 | 95 to 96 | 90/s |
| 90/s, burst 5 (C) | 5 | 97 to 103 | 90/s |
| **85/s, burst 5 (C)** | **3** | **90 to 91** | **85/s** |
| **85/s, burst 5 (C)** | **5** | **91 to 93** | **85/s** |

The cost is about 15% of the top speed: 85 instead of 100 per second per channel. On a real
provider the margin may be tuned with `RATE_LIMIT_PER_SECOND` once its own counting is known.

---

## D8 — Retry numbers

A temporary failure (D18) goes back to `pending` with `next_attempt_at` moved into the future;
the claim already skips rows that aren't due, so the database holds it until then. Four
choices:

- **Waits (exponential backoff: each wait doubles).** Starting at 1 s (1, 2, 4, 8 s, giving up
  after ~15 s) covers blips but nothing longer. Starting at 10 s (10, 20, 40, 80 s, ~2.5 min)
  also covers a provider restart or deploy. Starting at 30 s with 8 attempts (~35 min) covers
  long outages, but a login code arriving half an hour late is useless.
- **Jitter.** Without it, everything that failed together retries together. Full jitter (0
  to the wait) spreads best but retries some almost at once; equal jitter (half to all of the
  wait) always pauses.
- **`Retry-After`.** Ignore it; always obey it (a provider saying "2 days" parks the
  notification for 2 days); or use the longer of it and our wait, capped.
- **Max attempts.** 3 (~30 s), 5 (~2.5 min) or 8 (~20 min) with the 10 s start.

Chosen: **5 attempts; waits start at 10 s and double, capped at 10 min; equal jitter; the
provider's `Retry-After` is used when longer, within the same cap.** So the waits are 5-10 s,
10-20 s, 20-40 s and 40-80 s. Longer outages are for the circuit breaker and the re-queue
endpoint (8.5), not for ever longer waits. A wait asked for by `Retry-After` still counts as an
attempt, so a provider that always answers `429` can't keep a notification forever. Settings:
`RETRY_BASE_DELAY` (10s), `RETRY_MAX_DELAY` (10m); the maximum attempts come with 8.3. The
delay is added to the database's clock, like the lease. The lease (60 s) is confirmed in 8.4.

---

## D18 — Which provider errors are worth retrying

RET-1 asks to retry temporary failures (timeout, network, 5xx, 429) and not permanent ones
(4xx). A few answers needed a choice:

| Answer | Temporary? | Why |
|---|---|---|
| No response (timeout, connection refused, connection reset) | Yes | The provider or the network had a moment; the message itself is fine |
| `5xx` | Yes | The provider's own problem |
| `429 Too Many Requests` | Yes | The provider asks us to slow down. Our limiter should prevent it (D17), so it also hints that someone else shares the account or the provider counts differently |
| `408 Request Timeout` | Yes | The provider says the request was too slow, not wrong |
| `2xx` without a readable `messageId` | Yes, chosen | Usually a cut-off or garbled answer, so a retry likely gets a proper receipt. Sending again could deliver twice, but each retry carries the same `Idempotency-Key`, so a provider that honours it ignores the repeat. A provider that always answers this way is stopped by max attempts (8.3). The alternative, permanent, avoids any duplicate but marks a probably delivered message as failed |
| Other `4xx` (`400`, `401`, `403`, `404`, `422`, ...) | No | The provider rejected the request; the same request fails the same way |
| `3xx` (a redirect Go didn't follow) | No | A wrong `PROVIDER_URL`: a retry can't fix configuration |
| Errors before sending (request can't be built) | No | Our own bug or configuration |

The provider client decides it (`provider.IsTemporary`), since only it knows HTTP. A cut-off by
shutdown is handled before this: it isn't a failure, and the notification goes back to the
queue (Phase 6).

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
| D5 | Batch create | **All-or-nothing:** any invalid item → `400` naming every invalid item by position (`notifications[3].recipient`), nothing saved; 1 to 1,000 items, 10 MB body cap; response `202 {batch_id, count, ids}` (ids in request order) + `Location`; one `INSERT ... SELECT FROM unnest(...)` statement; batch idempotency as its own step | A batch fully exists or not at all, so retries and batch status stay simple; validation errors are client bugs to fix, not skip; one statement is atomic and fast (1,000 rows in ~50 ms) | Partial acceptance of the valid items | 2026-11-20 |
| D6 | Pagination | **Cursor (keyset):** opaque `next_cursor` encoding `(created_at, id)`, newest first, `limit` 1 to 100 (default 50), no total count; index `(created_at, id)` built `CONCURRENTLY` | Every page costs the same at any depth (~200x less work than offset at page 1,801 of 100k rows); pages don't shift while new notifications arrive | Jumping to page N; a total count in the response | 2026-11-20 |
| D10 | Local `.env.local` loading | **godotenv**, via `config.LoadDotEnv` at startup; missing file ignored; never overrides existing variables | Works with `make run-api`, plain `go run` and the IDE debugger; real environment wins over the file; no effect in Docker/production (file not in the image) | One small dependency; one local-only line in startup code (alternative was loading it in the Makefile) | 2026-11-19 |
| D11 | API conventions | **Own error envelope** `{"error": {code, message, details}}` for every error; **202 returns the full notification** plus a `Location` header; **unknown JSON fields are rejected** with 400 | One simple, readable error shape with stable codes; create and get share one response shape; typos surface immediately | The RFC 9457 Problem Details standard; tolerance for clients that send extra fields | 2026-11-20 |
| D12 | Idempotency key | **Optional `Idempotency-Key` header** (1 to 255 visible ASCII); repeat with the same request → original `202` + `Idempotent-Replayed: true`; repeat with a different request → `422 idempotency_key_reused`; key used while its row exists; `INSERT ... ON CONFLICT DO NOTHING` | Retries become safe for clients; key reuse bugs surface; the unique index makes it race-free across API instances | Key expiry and per-client scopes | 2026-11-20 |
| D13 | Correlation ID | **`X-Correlation-ID` header**; a client ID of 1 to 128 characters `[A-Za-z0-9._:-]` is kept, anything else (missing, unsafe, too long) is replaced by a new UUID, never rejected; returned on every response, added to every log line through a slog handler wrapper, stored on the notification for the worker | One search finds every log line of a request, later across API and worker; the client's own ID links our logs to theirs; validation keeps log lines clean | W3C `traceparent` (comes with tracing, Phase 13) | 2026-11-20 |
| D14 | Cancel | **`POST /api/v1/notifications/{id}/cancel`** → `200` with the notification; one conditional `UPDATE ... WHERE id = $1 AND status = 'pending'`; past pending → **`409 not_cancellable`** naming the status; already cancelled → **`200`** (safe to retry); the row is kept as `cancelled`; **batch cancel** `POST /api/v1/batches/{id}/cancel` cancels what is still pending and returns `cancelled` plus the counts, `0` is not an error | The check and the change are one statement, so cancel and a worker claim can't both win (tested: 200 races, and 50 over HTTP); an explicit action URL, since nothing is deleted; clients learn why a cancel came too late | Cancelling a notification that is already with the provider | 2026-11-20 |
| D7 | Priority | **Weighted 6:3:1** with fallback: of every 10 claims 6 prefer `high`, 3 `normal`, 1 `low`; an empty preferred queue falls back to the others (high to low); claims use `FOR UPDATE SKIP LOCKED` per priority | `low` always moves (IMP-6); simple, predictable rule; full capacity is used whatever is waiting; weights are easy to tune | During a `high` burst, `high` gets ~60% of throughput instead of 100% | 2026-11-20 |
| D15 | Rate limiter details | **`go-redis/redis_rate` (GCRA in one Lua script), one key per channel, burst 5, fail closed**; `Wait` sleeps for the time Redis says the next slot opens, then asks again; each Redis call has a 1 s deadline; settings `RATE_LIMIT_PER_SECOND`, `RATE_LIMIT_BURST` | Atomic and correct across processes; Redis's clock, not the workers'; burst 5 absorbs loops being a little late for their slot (burst 1 lost ~10% of the slots); never exceeding the provider's limit matters more than sending during a Redis outage | Own algorithm code; sending while Redis is down; up to 5 extra sends in the first second after a quiet moment | 2026-11-20 |
| D16 | Worker and rate limit | **Loops per channel; claim that channel, then wait for a slot, then send** (first slot-before-claim, revised after the load test); `WORKER_CONCURRENCY` = loops per channel (default 10); claim filtered by channel on index `(channel, priority, next_attempt_at)`; no slot or shutdown during the wait → `Unclaim` (back to `pending`, attempt not counted); Redis health on the worker (Phase 9), not the API | A backlog on one channel never holds up the others; nothing slow (database pauses) between permission and request, so arrivals stay even; a Redis outage uses no attempts; idle loops take no slots | Priority across channels (D7 works within a channel); a claimed notification holds its lease while waiting for its slot (a fraction of a second); a new index | 2026-11-20 |
| D17 | Meaning of "max 100/s" | **At most 100 per channel at the provider in any one-second window**; workers send at **85/s with burst 5** (at most 90 allowed); provider client keeps up to 1,000 idle connections; the fake provider measures the busiest window at arrival (`GET /_stats`) and `make loadtest` fails above 100 | The strict reading, judged where it matters; measured 90 to 93 with 3 and 5 workers | About 15% of the top speed (85 instead of 100 per second per channel) | 2026-11-20 |
| D18 | Provider errors worth retrying | **Temporary: no response, `408`, `429`, `5xx`, `2xx` without a `messageId`; permanent: `3xx`, other `4xx`, errors before sending**; decided by `provider.IsTemporary` | Retries only where they can help; a garbled receipt is retried with the same `Idempotency-Key` | A possible duplicate from a provider that ignores `Idempotency-Key` and sends a broken receipt | 2026-11-20 |
| D9 | Provider | **webhook.site only, for now**: the worker sends to `PROVIDER_URL` (your own webhook.site URL, set to answer `202 {messageId, status, timestamp}`); an own local fake provider is noted in the plan as an optional later step | Exactly the integration the requirements ask for; less code now; every send is visible in the browser | Offline runs; load tests beyond webhook.site's free request limit; a provider that fails or slows down on purpose for retry tests (until the fake is added) | 2026-11-20 |
| D8 | Retry numbers | **5 attempts; waits start at 10 s and double, capped at 10 min; equal jitter (half to all of the wait); `Retry-After` used when longer, same cap**; `RETRY_BASE_DELAY`, `RETRY_MAX_DELAY` | Covers blips and short outages (~2.5 min in total) while messages are still useful; retries that failed together are spread out; never earlier than the provider asked, never parked for days | Survival of long outages by waiting alone (8.5 handles those) | 2026-11-20 |
\1 **Own fake provider by default** (`cmd/fakeprovider`, `internal/fakeprovider`): answers `POST` on any path like webhook.site with `202 {messageId, status, timestamp}`; `FAKE_PROVIDER_LATENCY` (default 50ms) and `FAKE_PROVIDER_FAILURE_RATE` (share answered `503`); logs the messages received per channel each second; a Compose service the worker uses unless `PROVIDER_URL` is set (e.g. to webhook.site in `.env`) | Load tests at 100/s per channel without webhook.site's free request limit; provider failures on demand for Phase 8; works offline; the per-second report measures the rate limit | A second, simpler provider than the real one (no `429`, no `Retry-After` yet; added when Phase 8 needs them) | 2026-11-20 |
