# 08 — Phase 7: Rate Limiting

> **Result:** all workers together send at most the provider's limit per channel. A shared
> limiter in Redis hands out send slots per channel; each channel has its own worker loops, so a
> backlog on one never holds up the others. A local fake provider and a load test prove it: with
> 3 or 5 worker processes and 1,000 notifications per channel at once, the provider never
> received more than 93 per channel in any one second.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Redis connection | `database.ConnectRedis` | Fails fast at startup; the library's own log lines go through slog as JSON |
| Shared limiter | `internal/ratelimit` | GCRA via `redis_rate`, one key per channel, Redis's clock, 1 s deadline per call (D15) |
| Loops per channel | `internal/worker`, `cmd/worker` | `WORKER_CONCURRENCY` loops for each of sms, email, push (D16) |
| Claim per channel | `repository.ClaimNext`, migrations 000003-000004 | Index `(channel, priority, next_attempt_at)`; weighted priority within each channel (D7) |
| Slot before send | `service.Dispatcher` | Claim, then wait for a slot, then send; no slot means `Unclaim` (attempt not counted) |
| Outage logging | `internal/worker` | One error when an outage starts, one `working again` when a notification is delivered again |
| Connection reuse | `internal/provider` | Up to 1,000 idle connections kept instead of Go's default of 2 |
| Fake provider | `cmd/fakeprovider`, `internal/fakeprovider` | Answers like webhook.site; latency and failure rate; per-second report; busiest window at `GET /_stats` (D9 revised) |
| Load test | `cmd/loadtest`, `internal/loadtest`, `make loadtest` | Fails if the provider received more than 100 per channel in any one second (D17) |
| Settings | `internal/config`, `.env.example` | `REDIS_URL`, `RATE_LIMIT_PER_SECOND` (85), `RATE_LIMIT_BURST` (5), `FAKE_PROVIDER_*` |

## How it works

**The shared limiter.** Redis keeps one number per channel: the time at which the channel will
be "caught up". Each allowed send moves it one interval (1 s / rate) further; a send is allowed
while that time is at most `burst` intervals ahead of now. Checking and moving happen in one Lua
script, so two workers can never take the same slot, and "now" is Redis's clock, so the workers'
clocks don't matter. In any one second at most rate + burst sends are allowed. A denied caller
is told when the next slot opens and sleeps exactly that long.

**One worker loop.** Each loop belongs to one channel:

```
claim the next notification of my channel   (database, by priority)
wait for a slot of my channel               (Redis)
send it                                     (provider, right after the slot)
record the result
```

The slot comes right before the send, so nothing slow sits between the permission and the
request; when it came before the claim, database pauses bunched messages up at the provider.
Because each channel has its own loops and its own line in the database, 10,000 waiting SMS
don't delay a single email.

**When Redis is down.** No slot means no send (fail closed). The claimed notification goes back
with `Unclaim`, which also takes the attempt off, since nothing was sent. Each call to Redis has
a 1 s deadline, so a stopped Redis is noticed quickly instead of a call hanging for many
seconds. The worker logs the start of the outage once and the first successful delivery after
it once; the retries in between are at debug level. Sending resumes by itself when Redis is back.

**Why 85 and not 100.** The limiter controls when sends are allowed; the provider counts when
messages arrive. The HTTP request and the network in between spread arrivals a little, so the
workers aim below the limit: 85 per second with a burst of 5 allows at most 90 in any second,
and the busiest second measured at the provider was 90 to 93. The burst of 5 absorbs loops
being a millisecond or two late for their slot; with a burst of 1 about 10% of the slots were
lost.

**The fake provider.** A small HTTP server that answers `POST` on any path like webhook.site,
after `FAKE_PROVIDER_LATENCY` and with `FAKE_PROVIDER_FAILURE_RATE` of the requests answered
`503`. Once a second it logs how many messages per channel arrived. For every arrival it keeps
the arrivals of the last second per channel; the largest such count is the busiest one-second
window, readable at `GET /_stats` and reset with `DELETE /_stats`. Compose uses it unless
`PROVIDER_URL` is set.

**The load test.** `make loadtest` starts 3 workers (`WORKERS=5` for five) against the fake
provider, resets its measurements, sends 1,000 notifications per channel at once through the
API, waits until all are finished, and prints per channel the average rate, the busiest second
at the provider and the sends per second. It fails if any channel's busiest second is above 100
or any notification wasn't sent.

## Trying it

```
make loadtest                    3 workers, 1,000 per channel: busiest second ~90, average ~85/s
make loadtest WORKERS=5          the total stays the same with more workers
RATE_LIMIT_PER_SECOND=100 RATE_LIMIT_BURST=10 make loadtest    shows the limit being exceeded
make logs                        the fake provider's "received in the last second" lines

docker compose stop redis        a new notification stays pending with 0 attempts
docker compose start redis       it is sent on attempt 1; the worker logs "working again"
```

After a load test the workers still send to the fake provider;
`docker compose up -d --scale worker=1 worker` returns to the `.env` setting.

## Not done yet / decided later

- A failure is still final (`failed`); retries with backoff, `Retry-After` and lease recovery
  come in Phase 8. The fake provider's failure rate is there for those tests.
- The fake provider doesn't answer `429` or send `Retry-After` yet; added when Phase 8 needs them.
- Redis isn't in a health check yet: the worker gets its own health and metrics endpoint in Phase 9.
- 85/s is a margin chosen on one machine; on a real provider it may be tuned with
  `RATE_LIMIT_PER_SECOND` once its own way of counting is known.
