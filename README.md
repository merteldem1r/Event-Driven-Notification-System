# Event-Driven Notification Service

A service that sends **SMS, email and push notifications** on behalf of other systems, written
in Go.

## The idea

Other systems (a shop, a bank, an app) need to notify their users: "your order has shipped",
"your login code is 4821". They often do it in bursts: a flash sale can create thousands of
messages in one second. The company that actually delivers the messages, the **provider**,
accepts only **100 messages per second per channel**.

This service sits in between. It accepts every request at once, keeps the messages in a queue,
and sends them out at a steady, safe rate.

> **Bursts come in, a steady stream goes out, and the queue absorbs the difference.**

## The building blocks

```mermaid
flowchart LR
    client([Client systems]) -->|send a notification| api[API]
    api -->|store it| pg[(PostgreSQL<br/>data and queue)]
    worker[Workers] -->|take the next one| pg
    worker -->|may I send?| redis[(Redis<br/>rate limit)]
    worker -->|deliver| provider[Provider]
```

| Block | Its job |
|---|---|
| **API** | Receives requests, checks them, stores them, and answers immediately with "accepted" |
| **PostgreSQL** | Holds every notification and its status. It is also the queue: storing a notification is queuing it |
| **Workers** | Take notifications from the queue, most important first, and deliver them to the provider |
| **Redis** | Keeps the rate limit, shared by all workers, so together they never send too fast |
| **Provider** | The external company that delivers the message. Locally, a built-in fake stands in for it |

The API and the workers are two separate programs. Each can run as many copies as needed.

---

## Getting started

### 1. Install the tools

| Tool | Needed for | Get it |
|---|---|---|
| Docker Desktop (with Docker Compose) | Running the service | [docker.com/get-started](https://www.docker.com/get-started/) |
| Git | Getting the code | [git-scm.com](https://git-scm.com/downloads) |
| Go 1.26 and `make` | Tests and running without Docker (optional) | [go.dev/dl](https://go.dev/dl/); `make` comes with macOS and Linux |

### 2. Get the code

```bash
git clone https://github.com/merteldem1r/Event-Driven-Notification-System.git
cd Event-Driven-Notification-System
```

### 3. Start everything

```bash
docker compose up -d --build --wait
```

This builds the service and starts six containers: PostgreSQL, Redis, the database migrations,
the API, one worker, and the fake provider. It takes about a minute the first time and a few
seconds after that. When the command returns, the service is ready.

Check that it is up:

```bash
curl localhost:8080/health
# {"status":"ok","checks":{"database":"ok"}}
```

### 4. Send your first notification

```bash
curl -X POST localhost:8080/api/v1/notifications \
  -H 'Content-Type: application/json' \
  -d '{"recipient":"+905551234567","channel":"sms","content":"Your order has shipped.","priority":"high"}'
```

The answer comes back immediately with status `pending` and an `id`. A moment later the worker
has delivered it:

```bash
curl localhost:8080/api/v1/notifications/<id>
# "status": "sent", "attempts": 1, "provider_message_id": "12d2f537-..."
```

### 5. Explore the API in your browser

Open **[localhost:8080/docs](http://localhost:8080/docs)**: interactive documentation of every
endpoint, with a "Try it out" button that sends real requests to the running service.

![Swagger UI listing every endpoint of the API](docs/assets/swagger-2.png)

### 6. Watch it work

```bash
make logs     # follow the API, worker and fake provider logs (Ctrl+C stops following)
```

Each program logs one JSON line per event. Below, one notification sent with
`X-Correlation-ID: shop-req-42`: the API stores it, the worker sends it about 0.4 s later, and
the same correlation ID appears on every line, so one request can be followed across both
programs.

![Logs of the API, worker and fake provider for one notification](docs/assets/terminal-logs.png)

### 7. Stop

```bash
docker compose down        # stop everything, keep the data
docker compose down -v     # stop everything and delete the data
```

<details>
<summary>Send to a real endpoint (webhook.site) instead of the fake provider</summary>

1. Open [webhook.site](https://webhook.site) and copy your unique URL.
2. Create a file named `.env` next to `docker-compose.yml` containing
   `PROVIDER_URL=https://webhook.site/<your-id>`.
3. Run `docker compose up -d --build --wait` again. Messages now appear on your webhook.site page.

</details>

<details>
<summary>Run the API and worker without Docker (for development)</summary>

PostgreSQL and Redis still run in Docker; the programs run on your machine with Go. Stop the
full Docker setup first (`docker compose down`), since it uses the same ports.

```bash
cp .env.example .env.local     # local settings, read by both programs
make infra-up                  # start PostgreSQL and Redis, apply the migrations
make run-fakeprovider          # terminal 1: the fake provider on port 9090
make run-api                   # terminal 2: the API on port 8080
make run-worker                # terminal 3: the worker
```

</details>

---

## How it works

Every notification takes the same path:

```mermaid
flowchart LR
    client([Client]) --> api[API<br/>answers at once]
    api --> queue[(Queue)]
    queue --> worker[Worker<br/>by priority]
    worker --> limit{{Rate limit}}
    limit --> provider[Provider]
    provider --> sent([Sent])
    provider -.-> retry([Failed: try again later])
    retry -.-> queue
```

### Step 1. Accept quickly

The API checks the request (a valid phone number for SMS, a valid address for email, and so
on), stores the notification and answers `202 Accepted` within milliseconds. The client never
waits for the actual delivery; it can check the status any time, or send a whole batch of up to
1,000 notifications in one request.

### Step 2. Most important first, but nobody waits forever

Each notification has a priority: `high`, `normal` or `low`. When all three kinds are waiting,
workers pick them in a fixed mix: out of every 10, 6 are high, 3 normal and 1 low. Urgent
messages go first, but low-priority ones still move. Each channel (SMS, email, push) has
its own line, so a pile-up of SMS never delays an email.

### Step 3. Never faster than the provider allows

Before each send, a worker asks Redis for a slot. Redis hands out slots per channel at a fixed
pace, no matter how many workers ask:

```mermaid
flowchart LR
    w1[Worker 1] --> redis
    w2[Worker 2] --> redis
    w3[Worker 3] --> redis
    redis[["Redis<br/>at most 85 per second<br/>per channel"]] --> provider[Provider<br/>limit: 100 per second]
```

The service aims at 85 per second, a little below the provider's 100, because messages can
bunch up slightly on their way over the network. Adding more workers makes the system more
resilient, but never faster than the limit.

### Step 4. Try again, but give the provider room

If the provider has a problem (it is down, too slow, or busy), the worker tries again later,
waiting a little longer each time. The waits are slightly randomized, so thousands of failed
messages don't all come back at the same moment.

```mermaid
flowchart LR
    a1[Attempt 1] --> a2[Attempt 2<br/>after ~10 s]
    a2 --> a3[Attempt 3<br/>after ~20 s]
    a3 --> a4[Attempt 4<br/>after ~40 s]
    a4 --> a5[Attempt 5<br/>after ~80 s]
    a5 --> failed([Failed, with the reason])
```

As soon as one attempt succeeds, the notification is `sent`. If the provider rejects a message
outright (for example an invalid number), retrying can't help, so it is marked `failed` at once.

### Step 5. Nothing gets lost

- **A worker crashes mid-send:** each notification a worker takes is reserved for 60 seconds.
  If the worker doesn't finish in time, the reservation expires and another worker sends it.
- **A worker is restarted for a deploy:** it stops taking new work and finishes what it started.
- **Two workers reach for the same notification:** the database gives each notification to only
  one of them.

---

## Key design decisions

The full reasoning, with the options compared and the measurements, is in the
[decision log](docs/application/02-design-decisions.md).

| Decision | Why | What it costs |
|---|---|---|
| **PostgreSQL is also the queue** (instead of Kafka or RabbitMQ) | One place for data and queue: a notification can't be stored without being queued. Workers claim safely in parallel with `FOR UPDATE SKIP LOCKED` | The database does the queue's work too. Plenty at this service's rates; at much higher volume, a dedicated message broker |
| **One shared rate limit in Redis** | The limit holds for all workers together, however many there are | One more component. If Redis is down, sending pauses rather than risking the limit |
| **Separate lines per channel; the slot is taken right before sending** | Channels don't block each other, and the provider sees an even rate | Priority applies within a channel, not across channels |
| **85 per second, below the limit of 100** | The limit is judged where it matters, at the provider, with room for network timing | About 15% of the top speed |
| **Retry only what a retry can fix, with growing waits, up to 5 attempts** | Temporary problems recover on their own; hopeless messages fail fast | A provider outage longer than about 2.5 minutes ends in `failed` |
| **Each message is delivered at least once** | A crash can never lose a message | In the rare crash case a message may be sent twice; each send carries the same ID so the provider can ignore the repeat |

## What happens when things go wrong

| Situation | What the service does |
|---|---|
| A sudden burst of requests | Accepts all of them at once; the queue absorbs the burst and drains at the rate limit |
| The provider is down or slow | Retries with growing waits; after 5 attempts marks the notification `failed` with the reason |
| The provider says "too many requests, come back later" | Waits at least as long as the provider asked |
| The provider rejects a message | Marks it `failed` right away |
| Redis is down | Pauses sending; nothing is lost or used up; resumes on its own when Redis is back |
| The database is down | The API reports the error so clients can retry; workers pause and resume on their own |
| A worker crashes | Its unfinished notifications are picked up by another worker after 60 seconds |

---

## Results

Measured with the built-in load test, which sends 1,000 notifications on each channel at the
same moment:

- **Every notification was delivered**, 3,000 in about 12 seconds.
- **The provider never received more than 89 messages in any one second** on a channel, against
  its limit of 100.
- **Running 5 workers instead of 3 didn't break the limit**: they share it.
- **A worker killed mid-send, and Redis switched off for 14 seconds,** lost nothing; everything
  was delivered once the system recovered.

SMS sent per second during the load test (bars), against the provider's limit (line):

```mermaid
xychart-beta
    title "SMS sent per second, against the limit of 100"
    x-axis "Second" [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]
    y-axis "Messages per second" 0 --> 110
    bar [90, 85, 85, 85, 85, 85, 85, 85, 85, 85, 85, 60]
    line [100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100]
```

<details>
<summary>Full load test output and how to run it</summary>

```bash
make loadtest            # 3 workers; WORKERS=5 make loadtest for five
```

```
        sent  failed   avg rate   busiest 1s at provider   recorded per second
sms     1000       0     85.4/s                  89 (ok)   [90 85 85 85 85 85 85 85 85 85 85 60]
email   1000       0     85.4/s                  89 (ok)   [90 85 85 85 85 85 85 85 85 85 85 60]
push    1000       0     85.4/s                  89 (ok)   [90 85 85 85 85 85 85 85 85 85 85 60]
```

The first second is 90: the steady rate of 85 plus a small burst allowance of 5. The last second
is the end of the run. The test fails if any channel goes over 100 in any one-second window.

</details>

---

## Reference

### API

All endpoints are under `/api/v1` and use JSON. Try them at
[localhost:8080/docs](http://localhost:8080/docs) (Swagger UI); the specification is
[`docs/openapi.yaml`](docs/openapi.yaml), also served at `localhost:8080/openapi.yaml`.

![The API documentation page with the API's description](docs/assets/swagger-1.png)

| Request | What it does |
|---|---|
| `POST /notifications` | Create a notification. An optional `Idempotency-Key` header makes retrying safe |
| `POST /notifications/batch` | Create up to 1,000 at once, all or nothing |
| `GET /notifications/{id}` | Status of one notification |
| `GET /notifications` | List, filtered by `status`, `channel`, `batch_id`, `from`, `to`, page by page |
| `POST /notifications/{id}/cancel` | Cancel a notification that hasn't been sent yet |
| `GET /batches/{id}` | Progress of a batch: how many are pending, sent, failed |
| `POST /batches/{id}/cancel` | Cancel what hasn't been sent yet in a batch |

Channels: `sms`, `email`, `push`. Priorities: `high`, `normal` (default), `low`.

<details>
<summary>Examples: batch, list, errors</summary>

```bash
# Create a batch
curl -X POST localhost:8080/api/v1/notifications/batch \
  -H 'Content-Type: application/json' \
  -d '{"notifications":[
        {"recipient":"+905551234567","channel":"sms","content":"Flash sale starts now"},
        {"recipient":"user@example.com","channel":"email","content":"Flash sale starts now"},
        {"recipient":"device-token-abc","channel":"push","content":"Flash sale starts now","priority":"low"}]}'
# 202 {"batch_id":"4a38a4e9-...","count":3,"ids":["90c75602-...","18952cd8-...","de1ab28f-..."]}

# Follow the batch
curl localhost:8080/api/v1/batches/4a38a4e9-...
# 200 {"total":3,"counts":{"pending":0,"processing":0,"sent":3,"failed":0,"cancelled":0},...}

# List sent SMS, two per page; pass next_cursor for the next page
curl 'localhost:8080/api/v1/notifications?channel=sms&status=sent&limit=2'
# 200 {"items":[...],"next_cursor":"eyJ0Ijoi..."}

# Every problem in a request is reported at once
curl -X POST localhost:8080/api/v1/notifications \
  -H 'Content-Type: application/json' -d '{"recipient":"+905551234567","channel":"fax","content":""}'
# 400 {"error":{"code":"validation_failed","message":"the request has invalid fields",
#      "details":[{"field":"channel","message":"must be sms, email or push"},
#                 {"field":"content","message":"is required"}]}}
```

</details>

### Monitoring

| Endpoint | Shows |
|---|---|
| `localhost:8080/metrics` | Queue size per channel, how long the oldest message has waited, API request counts and speed |
| `localhost:8081/metrics` | Every send by result (sent, retried, failed), provider speed, time from request to delivery |
| `localhost:8080/health` | Whether the API can reach the database |
| `localhost:8081/health` | Whether the worker can reach the database and Redis |

The metrics are in Prometheus format. With several workers, `docker compose port worker 8081`
shows each worker's port. Logs are JSON; every line carries the request's correlation ID and
never the message content.

### Tests

| Command | What it does |
|---|---|
| `make test` | All tests, including tests against real PostgreSQL and Redis started in Docker |
| `make check` | Code checks, linter and all tests; GitHub Actions runs the same on every push |
| `make loadtest` | The load test from the results above |

### Configuration

Every setting is an environment variable with a sensible default; invalid values stop the
program at startup. The most important ones, with their defaults (all of them are described in
[`.env.example`](.env.example)):

```bash
# Connections
DATABASE_URL=postgres://notification:notification@localhost:5433/notification?sslmode=disable
REDIS_URL=redis://localhost:6379/0          # the shared rate limiter
HTTP_PORT=8080                              # API port
WORKER_HTTP_PORT=8081                       # worker port for /health and /metrics
LOG_LEVEL=info                              # debug, info, warn or error

# Provider
PROVIDER_URL=http://localhost:9090/         # where messages are delivered (the fake provider by default)
PROVIDER_TIMEOUT=5s                         # time limit for one send

# Rate limit, per channel, for all workers together
RATE_LIMIT_PER_SECOND=85                    # steady rate, kept below the provider's 100
RATE_LIMIT_BURST=5                          # small extra allowance after a quiet moment

# Workers
WORKER_CONCURRENCY=10                       # messages each worker sends in parallel, per channel
WORKER_POLL_INTERVAL=500ms                  # how often an idle worker checks the queue
WORKER_LEASE_DURATION=60s                   # after this, a crashed worker's message is taken back
WORKER_SHUTDOWN_TIMEOUT=25s                 # on stop, time to finish sends already started

# Retries
RETRY_MAX_ATTEMPTS=5                        # attempts in total before a notification is failed
RETRY_BASE_DELAY=10s                        # first wait, doubled after each failed attempt
RETRY_MAX_DELAY=10m                         # the longest wait

# Fake provider (local testing only)
FAKE_PROVIDER_LATENCY=50ms                  # how long each answer takes
FAKE_PROVIDER_FAILURE_RATE=0                # share of requests answered 503, e.g. 0.2 = 20%
```

With Docker Compose, the connections already point at the containers. The provider, rate
limit, retry and fake provider settings can be changed in a `.env` file next to
`docker-compose.yml`; the rest are set in `docker-compose.yml` itself.

### Project layout

```
cmd/              the programs: api, worker, fakeprovider, loadtest
internal/         the shared code: handler (HTTP), service, repository (SQL), ratelimit,
                  retry, worker, metrics, ...
migrations/       database schema, versioned
docs/             design documents, decision log, plan, requirements, OpenAPI
```

### Further reading

1. [Understanding the project](docs/application/01-understanding-the-project.md): what the
   system must do.
2. [Design decisions](docs/application/02-design-decisions.md): every choice, its options and
   trade-offs.
3. One short document per build phase in [`docs/application/`](docs/application/), from the
   [database](docs/application/04-database.md) to
   [observability](docs/application/10-observability.md).

### With more time

- Prometheus and Grafana in Docker Compose with a ready-made dashboard.
- An endpoint to resend `failed` notifications after a provider outage.
- Archiving old notifications as the table grows.
- Scheduled notifications, message templates, live status updates, and tracing.
