# 01 — Understanding the Project

> **Goal of this document:** understand *what* we are building and *why*, in plain words,
> before writing any code. Each later step builds on this one.
> Source of truth: `docs/Software-Engineer-Assessment-Golang.pdf`.

---

## 1. The project in one paragraph

We're building the **backend of a notification service** in Go. Other
systems call our HTTP API and say *"send this SMS / email / push to this person"*.
The API doesn't send anything itself. It **checks the request, saves it, answers "accepted"
right away**, and puts the work in a **queue**. Background **workers** take items from
the queue and send them to an **external provider** (simulated with webhook.site). They
respect **priority** and a **rate limit of 100 messages/second per channel**, **retry**
failures sensibly, and **never send the same message twice by mistake**. You can check the
status of every notification at any time, and the system exposes **metrics, logs and
health checks** so people can see what it's doing.

There's no UI. The product is an **API plus background processing**.

---

## 2. What problem does this product solve?

Businesses like e-commerce sites, banks and media companies need to reach *their* users,
often with millions of notifications a day. Example:

> An online shop starts a **flash sale at 20:00** and wants to tell 500,000 users at once:
> 300,000 by push, 150,000 by email, 50,000 by SMS.

If the shop's system had to call the SMS company directly, one message at a time, waiting for
each answer, there would be several problems:

| Problem | Why it hurts |
|---|---|
| **Bursts** | 500k requests arrive in seconds, but providers only accept messages at a limited speed. |
| **Slow callers** | The shop's system would be blocked for minutes, waiting. |
| **Failures** | Providers sometimes time out or return errors. Who retries? When? How often? |
| **Duplicates** | If something retries blindly, the user gets the same SMS twice, which is annoying and costs money. |
| **No visibility** | "Did my campaign go out? How many failed?" Nobody knows. |

**Our service solves all of these in the middle:**

```
 Customer systems                  OUR SERVICE                       Providers
 (shop, bank, ...)                                                   (SMS / Email / Push)
                    ┌──────────────────────────────────────────┐
  "send these" ───► │  accept fast → queue → send at safe speed │ ───► webhook.site
  "status?"    ───► │  retry failures, track every message      │      (fake provider)
                    └──────────────────────────────────────────┘
```

### The core idea: "accept now, deliver later"

This is the main idea of the whole project, and you should be able to explain it in one sentence:

> **Requests arrive in bursts, but we're only allowed to send at a fixed rate. A queue sits
> between the two and absorbs the difference.**

A post office works the same way:

1. **Counter (API).** The clerk checks your letter, gives you a tracking number, says
   "accepted" and you leave. They don't walk the letter to its address while you wait.
2. **Sorting room (queue).** Letters wait in bins. Express letters (high priority) are taken first.
3. **Couriers (workers).** Several couriers work at the same time. Two couriers must never take
   the same letter, and all couriers *together* can only hand over 100 letters per second per
   delivery type.
4. **Delivery company (provider).** If it's closed, the courier tries again later, waiting
   longer each time. After too many failures the letter goes to the "failed" shelf.
5. **Tracking website (status API, metrics).** Anyone with the tracking number can check
   where the letter is.

### Why it's called "event-driven"

The parts don't call each other directly and wait. The API produces an event ("a new
notification needs sending") and moves on. Workers consume those events whenever they have
capacity. Producer and consumer are **decoupled**: the API stays fast even when sending is
slow, and we can add more workers without touching the API.

---

## 3. Who uses it?

| User | What they want |
|---|---|
| **API consumers** (customers' systems) | Create notifications (one or 1000 at a time), check status, cancel, list/filter. |
| **Internal teams** (engineers, support, operations) | See queue size, success/failure rates and latency, follow one request through the logs, know whether the service is healthy. |

---

## 4. The journey of one notification

Follow one SMS from start to finish. **Every later phase builds one part of this picture.**

```
 ① Client                ② API                   ③ Database / Queue
 POST /notifications ──► validate fields  ──────► save row: status = pending
 {to, channel,           check idempotency        (with priority, created_at,
  content, priority}     key                       correlation id)
        ◄─────────────── 202 Accepted + id
                                                         │
                                                         ▼
 ⑥ Status / metrics      ⑤ Provider              ④ Worker
 GET /notifications/{id} ◄─ webhook.site ◄─────── claim next job (by priority)
 → "sent"                  answers 202            wait for rate-limit slot
                           {messageId,...}        POST {to, channel, content}
                                                  success → status = sent
                                                  failure → retry later or failed
```

Step by step:

1. **Client sends a request.** Example: `{ "to": "+905551234567", "channel": "sms",
   "content": "Flash sale starts now!", "priority": "high" }`.
2. **API validates it.** Are the required fields there? Is the phone number valid for SMS? Is
   the content too long? Have we already seen this idempotency key? If something is wrong it
   returns `400` right away, and nothing is saved.
3. **API saves it** with status `pending` and answers **`202 Accepted`** with the new ID.
   `202` means "I accepted your request, but it isn't finished yet". That's the honest answer,
   because sending happens later.
4. **A worker claims it.** Several workers run in parallel. Each takes the next
   job (high priority first) in a way that guarantees **no two workers take the same job**.
   Before sending, it waits for a **rate-limit slot** for that channel.
5. **The worker calls the provider** (`POST https://webhook.site/{uuid}`, body
   `{to, channel, content}`). The provider answers `202 {messageId, status, timestamp}`.
6. **The worker updates the status.** On success it sets `sent` and saves the provider's
   `messageId`. On failure it decides: retry later (with a growing wait) or give up (`failed`).
   The client can see the status at any time through the API.

> 💡 **Worth knowing:** `sent` means *the provider accepted it*, not *the phone received
> it*. Real providers report final delivery later through callbacks (delivery receipts).
> webhook.site doesn't, so our final success state is "accepted by provider".

### The status lifecycle

Every notification moves through these states. The exact names are a decision we make later.

```
                  ┌──────────── cancel ─────────────┐
                  │                                 ▼
  (scheduled) ──► pending ──► processing ──► sent          cancelled
                    ▲              │
                    │   retryable  │  attempts used up /
                    └── failure ───┤  permanent error
                    (wait, then    ▼
                     try again)  failed
```

- **Cancel** is only allowed while it's still waiting (`pending`/`scheduled`). Once a worker has
  started sending, it's too late. *Tricky case:* what if a cancel and a worker arrive
  at the same moment? (Answer: both use the database's atomic update, so only one of them wins.)

---

## 5. The requirements, translated

Each requirement below comes with what it really means and the hidden question behind it,
which is where the real design work is.

### 5.1 Notification Management API

| Requirement | Plain meaning | Hidden question we must answer |
|---|---|---|
| Create with recipient, channel, content, priority | `POST` one notification | What's a valid recipient? Phone number for SMS (E.164 format, e.g. `+905551234567`), email address for email, device token for push. |
| Batch creation (up to 1000) | `POST` many in one request; they share a **batch ID** | If 3 out of 1000 are invalid, do we reject the whole batch or accept 997? Saving 1000 rows must be fast (one transaction, bulk insert). |
| Query status by ID or batch ID | `GET /notifications/{id}`, `GET /batches/{id}` | For a batch, return a summary (e.g. 990 sent, 7 pending, 3 failed) and/or the items. |
| Cancel pending | Stop a notification before it's sent | The cancel-vs-worker race described above. |
| List with filters + pagination | `GET /notifications?status=failed&channel=sms&from=...&to=...` | Offset pagination (`page=3`) is simple but slow on big tables and can skip rows. Cursor pagination is more robust. Which one? |

### 5.2 Processing Engine

| Requirement | Plain meaning | Hidden question |
|---|---|---|
| Process asynchronously via queue workers | The API never sends. Background workers do. | Which queue technology? How do several workers avoid taking the same job? What if a worker crashes halfway? |
| **Rate limit: max 100 msg/s per channel** | SMS ≤ 100/s, Email ≤ 100/s, Push ≤ 100/s, each limited separately | The limit is **global across all workers**, not 100 per worker. With 5 workers each allowed 100/s, we'd send 500/s, which is wrong. So the limiter must be **shared**. |
| Priority queue (high, normal, low) | Important messages go first | **Starvation:** if high-priority messages never stop coming, do low ones wait forever? |
| Content validation | Reject bad input early | Which limits per channel? (See below.) |
| Idempotency | Never send duplicates | Two levels, explained below. |

**Rate limit in numbers**, so you can feel it:
- 50,000 SMS at 100/s takes **at least 500 seconds (about 8.3 minutes)**. The queue holds the rest
  while they wait. That's exactly why the queue exists.
- Total capacity is 3 channels × 100/s = 300/s, which is about 26 million/day. "Millions daily" fits.

**Content validation: sensible limits (we decide):**
- **SMS:** one SMS segment is 160 characters (GSM-7 alphabet), but only 70 if the text uses
  characters outside that alphabet, like some Turkish letters (ş, ğ, ı). Longer messages are split
  into several segments. We pick a maximum and document it.
- **Push:** Apple/Google limit the whole payload to about 4 KB, so the text should be short.
- **Email:** no hard limit; we pick a reasonable maximum.
- Plus: required fields, a known channel, a known priority, and a recipient format that matches the channel.

**Idempotency happens at two levels:**
1. **API level: "the client retried".** The client sends `POST`, the network drops our
   answer, and the client sends it again. Without protection, that creates two notifications and two SMS.
   Fix: the client sends an **`Idempotency-Key`** header. If we've seen the key before, we return
   the *original* notification instead of creating a new one (enforced with a unique index in the DB).
2. **Worker level: "we crashed mid-send".** A worker sends to the provider, then crashes
   before writing `sent` to the database. Another worker picks the job up again and sends a second time.
   **Exactly-once delivery is impossible in a distributed system.** The honest answer
   is *at-least-once delivery + deduplication*: keep the window between "sent" and
   "recorded" tiny, and pass our notification ID to the provider as a dedup key so a real
   provider could drop the duplicate.

### 5.3 Delivery & Retry Logic: our own design

The requirements deliberately leave this open, so **this is where design judgment matters most.**
What a good design covers:

| Topic | Idea |
|---|---|
| **Classify errors** | Retry *temporary* errors (timeout, connection refused, `5xx`, `429 Too Many Requests`). Don't retry *permanent* ones (`400` bad request: retrying won't help). |
| **Backoff** | Wait longer after each failure: 1s → 2s → 4s → 8s … (exponential), up to a maximum. |
| **Jitter** | Add randomness to the wait. If the provider was down and 10,000 jobs all retry at exactly the same second, they knock it down again ("thundering herd"). |
| **Max attempts** | After N tries, give up and mark the job `failed` (a "dead letter"), keeping the last error for inspection. Optionally allow a manual re-queue. |
| **Respect `Retry-After`** | If the provider tells us when to come back, wait that long. |
| **Stuck jobs** | A worker crashed while a job was `processing`. Use a *lease*: if the job isn't finished within X seconds, it becomes available again. |
| **Circuit breaker** (optional) | If the provider is clearly down, stop hammering it for a while and fail fast, then test again carefully. |

### 5.4 Observability

| Requirement | Plain meaning |
|---|---|
| Real-time metrics endpoint | An endpoint (typically `/metrics`, Prometheus format) showing **queue depth** (how many are waiting), **success/failure rates**, and **latency** (how long a send takes, and how long a message waited in total). |
| Structured logging with correlation IDs | Logs as JSON (`{"level":"info","msg":"sent","notification_id":"…","correlation_id":"…"}`) instead of free text, so tools can search them. A **correlation ID** is created when the request arrives (or taken from the client's `X-Correlation-ID` header), saved with the notification, and printed by the worker too. With it you can follow one request from API → DB → worker → provider in the logs. |
| Health check endpoint | `/health`: "is the service alive, and can it reach its database/queue?" Docker and Kubernetes use this to restart broken instances. Often split into **liveness** (the process is running) and **readiness** (dependencies are OK). |

---

## 6. The external provider (webhook.site)

We don't send real SMS. **webhook.site** gives you a unique URL that records every request it
receives and can be configured to return a custom response. We configure it to return:

```
Status 202
{ "messageId": "uuid-here", "status": "accepted", "timestamp": "ISO8601" }
```

Things to know:
- The provider must sit **behind an interface** in our code, so we can swap webhook.site for a
  local fake provider in tests (one that can also simulate failures and slowness to test retries) or for a
  real provider later. Its URL comes from configuration (environment variable).
- Free webhook.site URLs have request and lifetime limits, so someone running our project in
  a week might hit an expired URL. We should handle this gracefully (e.g. a local mock provider in
  docker-compose by default, webhook.site switchable via an env var).

---

## 7. Optional features in plain words

| Feature | What it means |
|---|---|
| **Failure handling** | Not defined in the requirements; we define it. Dead-letter/failed state + re-queue, circuit breaker, graceful shutdown (finish in-flight jobs on stop), recovery of stuck jobs. |
| **Scheduled notifications** | "Send this tomorrow at 09:00." A `scheduled_at` field; workers ignore the job until then. |
| **Template system** | Save `"Hi {{name}}, your order {{order_id}} shipped"` once, then send with `{"name":"Mert","order_id":"123"}`. |
| **WebSocket updates** | Instead of the client asking "is it sent yet?" over and over, the server *pushes* status changes live. |
| **Distributed tracing** | Like the correlation ID, but richer: a timeline showing how long each step took (API → DB → worker → provider). Usually OpenTelemetry + Jaeger. |
| **GitHub Actions CI/CD** | On every push, GitHub automatically runs lint + tests (and maybe builds the Docker image). |

---

## 8. Deliverables: what "done" means

| # | Deliverable | Meaning |
|---|---|---|
| 1 | Clean commit history | Small, meaningful commits with clear messages (e.g. `feat: add rate limiter`), **not** one giant "done" commit. A readable history shows *how* the project evolved. |
| 2 | README.md | How to run it, architecture overview, example API calls. |
| 3 | Docker Compose | `docker-compose up` starts **everything** (API, workers, DB, …) with no other manual steps. |
| 4 | API documentation | An OpenAPI (Swagger) spec describing every endpoint. |
| 5 | Database migrations | Schema changes as numbered, versioned files (`001_create_notifications.up.sql`, …), not "run this SQL by hand". |
| 6 | Test suite | One command (e.g. `make test`) runs all tests. |

---

## 9. The building blocks

```
          ┌──────────────┐        ┌──────────────────────┐
 HTTP ───►│  API server  │───────►│  Database (+ queue)  │
          │  (cmd/api)   │◄───────│  notifications,      │
          └──────────────┘        │  batches, templates  │
                 │                └──────────▲───────────┘
          /metrics /health                   │ claim / update
                                  ┌──────────┴───────────┐     ┌───────────────┐
                                  │  Workers (cmd/worker)│────►│ Rate limiter  │
                                  │  N in parallel       │     │ (shared, per  │
                                  └──────────┬───────────┘     │  channel)     │
                                             │                 └───────────────┘
                                             ▼
                                  ┌──────────────────────┐
                                  │ Provider (interface) │──► webhook.site / mock
                                  └──────────────────────┘
```

| Block | Job |
|---|---|
| **API server** | HTTP endpoints, validation, idempotency, returns 202. |
| **Database** | Source of truth for every notification and its status. |
| **Queue** | Holds work waiting to be sent, in priority order. (Which technology is our first big decision.) |
| **Workers** | Claim jobs, respect the rate limit, call the provider, update status, retry. |
| **Rate limiter** | Shared counter per channel so *all workers together* stay ≤ 100/s. |
| **Provider client** | Talks HTTP to webhook.site; hidden behind an interface. |
| **Observability** | Metrics, structured logs, health checks, tracing. |

---

## 10. How we'll start: the step-by-step path

We work in **small phases**. Each phase ends with something that **runs, is tested, and is
committed** (only after you approve). Rough order:

| Step | What | Why in this order |
|---|---|---|
| **0** | **Understand the product** (this document) | Everything else builds on a clear picture of the product. |
| **1** | **Make the key design decisions** (queue tech, rate limiter, DB library, …) | Everything else depends on these, especially the queue. |
| 2 | Project skeleton: folders, config, logging, `/health`, Dockerfile, docker-compose, Makefile | A running "hello" service gives us a base to build on. |
| 3 | Database: migrations + schema + data-access code | Everything is stored here. |
| 4 | API: create + get one notification (validation, idempotency, correlation ID) | The smallest useful slice. |
| 5 | API: batch, list (filters + pagination), cancel | Completes the management API. |
| 6 | Worker: claim by priority → send to provider → update status | The smallest end-to-end flow. |
| 7 | Rate limiting (shared, 100/s per channel) | Correctness under many workers. |
| 8 | Retry & failure handling | The part that needs the most design thinking. |
| 9 | Metrics + structured logs polish | Visibility. |
| 10+ | Optional features: scheduling, templates, WebSocket, tracing, CI/CD | Built on a solid core. |
| Last | Load test (prove the 100/s cap holds), final README, OpenAPI check | Proof and presentation. |

### Decisions we'll make together in Step 1 (preview only, nothing decided yet)

1. **Queue technology.** Options: PostgreSQL as the queue (`SELECT … FOR UPDATE SKIP LOCKED`),
   Redis, RabbitMQ, Kafka. This is the biggest decision; priority, retries, scheduling and cancel
   all depend on it.
2. **Where the shared rate limiter lives**, e.g. Redis or the database.
3. **Priority strategy:** strict (always high first) vs weighted (mostly high, but low still moves).
4. **Batch validation:** all-or-nothing vs partial acceptance.
5. **Pagination:** offset vs cursor.
6. **Retry policy numbers:** max attempts, base delay, max delay.
7. **Content limits** per channel.
8. **Default provider in docker-compose:** local mock vs webhook.site.
9. **Tooling:** HTTP router (e.g. chi), DB library (e.g. pgx), migration tool (e.g. golang-migrate),
   OpenAPI approach (hand-written vs generated).

---

## 11. Things the requirements leave unclear (we decide and document)

- How retries work (explicitly left to us).
- What "failure handling" should include.
- Exact content limits, recipient formats and push token format.
- Whether batch creation is all-or-nothing.
- What "real-time" metrics means (a Prometheus endpoint that is always up to date is the usual reading).
- Go "1.2x" means any Go 1.20+ version. We use **Go 1.25** (`go.mod`).

For anything ambiguous we **make a reasonable choice and write down why** in the README.
Explicit assumptions are much better than silent ones.

---

## 12. Check your understanding

Try to answer these in your own words. If you can, you understand the core design.

<details><summary>1. Why does the API return 202 instead of 200 or 201?</summary>

Because the work (sending) isn't done yet. 202 = "accepted for processing". 201 would suggest
the final result exists; 200 would suggest the job is complete.
</details>

<details><summary>2. Why do we need a queue at all? Why not just send inside the API request?</summary>

Bursts come faster than the allowed sending rate (100/s/channel). The queue absorbs the
difference, keeps the API fast, survives provider outages, and lets workers scale separately.
</details>

<details><summary>3. We run 4 workers. How do we keep SMS at ≤ 100/s?</summary>

The limiter must be **shared** (one counter for all workers per channel, e.g. in Redis or the DB).
A per-worker limit of 100/s would allow 400/s.
</details>

<details><summary>4. How do two workers avoid sending the same notification?</summary>

They claim jobs atomically. For example, Postgres `FOR UPDATE SKIP LOCKED` lets each worker lock
a different row, and the others skip locked rows.
</details>

<details><summary>5. Can we guarantee each message is sent exactly once?</summary>

No. A crash between "provider accepted" and "we saved sent" can always cause a resend. We do
at-least-once + idempotency keys / deduplication to make duplicates very rare and harmless.
</details>

<details><summary>6. Which errors should be retried?</summary>

Temporary ones: timeouts, network errors, 5xx, 429. Not permanent ones like 400 (the request
itself is wrong; retrying gives the same result).
</details>

<details><summary>7. Why add jitter to retry delays?</summary>

So thousands of failed jobs don't all retry at the exact same moment and overload the
provider again (thundering herd).
</details>

<details><summary>8. What is a correlation ID for?</summary>

To connect all log lines belonging to one request across API and worker, so you can follow its whole
journey while debugging.
</details>

<details><summary>9. A flash sale queues 60,000 SMS. Minimum time to send them all?</summary>

60,000 / 100 per second = 600 seconds = 10 minutes. The rate limit sets the pace, not the number of
workers.
</details>
