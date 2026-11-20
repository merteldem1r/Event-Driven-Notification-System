# Web simulation

A page that shows the notification service working: every building block, the paths a
notification takes between them, and what happens to each one. It runs a model of the service in
the browser, with the same rules and defaults as the Go code, so it needs no backend and can be
opened from a link.

It is a **simulation**, not the running service. To run the real system, see the
[main README](../README.md) (`make up`).

## What you see

```
 Client systems ──POST──► API ──INSERT──► PostgreSQL ──claim──► worker-N ──send──► Provider
                                          (the queue)  ◄─result──    │
                                                                     └──slot?──► Redis
```

- **Diagram.** One block per part of the service. Dots move along the edges: color is the
  channel, size is the priority, and the dots going back to PostgreSQL show the result (green
  sent, yellow retry later, red failed).
  - **PostgreSQL** shows the rows that are due per channel and priority, the scheduled ones,
    the ones waiting out a retry, and the ones claimed by a worker.
  - **Each worker** shows its loops, one square per loop and channel: filled while sending,
    hollow while holding a claimed row and waiting for a rate-limit slot, gray when idle.
  - **Redis** shows the free rate-limit slots per channel (the burst).
  - **Provider** shows what arrived in the last second against its limit of 100.
- **Scenarios** (side panel): send one notification and follow it, schedule one 15 s ahead, a
  burst of 1,000 per channel, a batch of 200 and its cancel, the provider answering 503 or 400,
  Redis down, killing or adding a worker, and steady background traffic.
- **Settings**: the service's own settings, named after the environment variables
  (`WORKER_CONCURRENCY`, `RATE_LIMIT_PER_SECOND`, `RETRY_BASE_DELAY`, ...). Changing them shows
  why the defaults were chosen.
- **Inspect**: one notification's fields and its history, step by step.
- **Charts**: arrivals at the provider per second (the rate-limit proof) and rows waiting in the
  queue (the burst being absorbed).
- **Logs**: shaped like the service's JSON logs: level, message, IDs. Click a line to inspect
  its notification.
- **Speed**: from 0.1x (to watch single messages) to 5x (to see a 60 s lease expire quickly).

Scenarios can be linked: `/?scenario=burst`, `/?scenario=retries`, `/?scenario=redis`,
`/?scenario=crash`.

## Running it

```sh
cd web
npm install
npm run dev        # http://localhost:3000
npm test           # the simulation's tests (Vitest)
npm run lint       # ESLint
npm run typecheck  # TypeScript
npm run build      # production build: one static page
```

Needs Node.js 22 or newer.

## How it works

Three layers, each in its own place:

| Layer | Files | What it does |
|---|---|---|
| Model | `lib/sim/rules.ts`, `lib/sim/limiter.ts`, `lib/sim/heap.ts`, `lib/sim/engine.ts` | The service's rules and a deterministic simulation of it. Plain TypeScript, no React, fully tested. |
| State | `lib/sim/store.ts`, `lib/sim/tokens.ts` | Runs the model on animation frames, publishes snapshots to the page about 10 times a second and the log 4 times a second, and turns events into moving dots. |
| Page | `app/`, `components/sim/`, `components/ui/` | The diagram, side panel, charts and logs. |

**The model is event-driven.** Every worker loop has a wake-up time: after an empty queue it
sleeps one poll interval, while waiting for a slot it sleeps until Redis says the next slot
opens, while sending it sleeps for the provider's latency. `advance(ms)` always runs whichever
wake-up comes next, so timing is exact rather than rounded to animation frames. The same loop
is used by the tests, without a browser.

**The model stays fast with thousands of rows.** Wake-ups wait in a binary heap, and each
channel and priority has its own heap of due rows ordered by `next_attempt_at`, so claiming the
next row, like the `ORDER BY` with an index in PostgreSQL, costs a few steps instead of a scan.
Counters are kept as rows change, so a snapshot does not walk the queue. Finished rows beyond
the last 10,000 are forgotten, so memory stays flat on a long run.

**The dots are drawn outside React.** Hundreds of messages per second would mean hundreds of
re-renders. Instead, each edge registers its SVG path with `tokenBus`, which moves circles along
it on every frame. Each edge starts at most one new dot every 28 ms, so on a busy edge one dot
stands for several messages. Points along each path are sampled once, when its shape changes,
not on every frame.

**React renders only what changed.** Components read narrow slices of the store, the charts
re-render once per simulated second when a new point arrives, and log lines are memoized, so a
new batch of lines renders only the new ones. Measured in Chrome at 5x speed with 4 workers, 20
loops each and 300 requests per second, the page holds 60 frames per second with no long tasks.

**The page is static.** `app/page.tsx` loads the simulator in the browser only (`next/dynamic`
with `ssr: false`), because it runs on timers and random numbers. `next build` prerenders one
static page, which is what Vercel serves.

## How the model maps to the Go service

Each rule is ported, not invented. The comments in `lib/sim/rules.ts` name the decision
(`docs/application/02-design-decisions.md`) and the Go file.

| Rule | Go | Web |
|---|---|---|
| Priority 6:3:1 with fallback (D7) | `internal/service/dispatcher.go` `claimPattern`, `claimOrder` | `rules.ts` `CLAIM_PATTERN`, `claimOrder` |
| Claim the earliest due row, `FOR UPDATE SKIP LOCKED`, lease it | `internal/repository/notification.go` `ClaimNext` | `engine.ts` `claimNext` |
| Claim, then slot, then send; one dispatcher per channel (D16) | `internal/service/dispatcher.go` `deliver` | `engine.ts` `step`, `askSlot`, `startSend` |
| Shared limit: GCRA, 85/s, burst 5, fail closed (D15, D17) | `internal/ratelimit/ratelimit.go` (`redis_rate`) | `limiter.ts` `Gcra` |
| No slot (Redis down): put back, attempt not counted | `Unclaim` | `engine.ts` `unclaim` |
| Temporary vs permanent errors (D18) | `internal/provider/provider.go` `IsTemporary` | `rules.ts` `isTemporary` |
| Backoff 10 s doubling to 10 min, equal jitter, 5 attempts (D8) | `internal/retry/retry.go` | `rules.ts` `retryDelay`, `retryNext` |
| Lease sweep every 10 s, lease 60 s (D19) | `internal/worker/worker.go` `sweep`, `RecoverExpired` | `engine.ts` `sweep` |
| Only the claim that owns a row may record its result | `finish` (status and attempt check) | `engine.ts` `owns` |
| One error per outage, then "working again" (OBS-2) | `internal/worker/worker.go` `trouble` | `engine.ts` `trouble`, `recoveredFrom` |
| Scheduled notifications (D21) | `next_attempt_at` starts at `scheduled_at` | `engine.ts` `submit` |
| Cancel only while pending (D14) | conditional `UPDATE` | `engine.ts` `cancel`, `cancelBatch` |
| Busiest one-second window at the provider | `internal/fakeprovider` | `engine.ts` `arrive` |

The tests in `lib/sim/*.test.ts` check the same behavior as the Go tests and the load test: a
burst of 1,000 per channel with 3 workers never goes above 90 in any second and sends
everything; high goes first without starving low; temporary failures stop after 5 attempts; a
killed worker's row comes back after its lease and is sent on attempt 2; nothing is sent while
Redis is down, nothing is lost, and the outage is logged once. Others check the bookkeeping:
random operations keep every counter equal to a full count, old rows are forgotten, and the
worker count stays in sync.

## What is simplified

- **No HTTP.** Every request is valid and answered `202`. Validation, idempotency keys,
  templates and error responses are not modeled; the request line in the logs stands for them.
- **The database never fails** and answers at once. Only Redis and the provider can break.
- **The provider** answers `202`, `503` or `400`, with a latency. Timeouts and `Retry-After` are
  not modeled.
- **Workers are killed, not stopped.** Graceful shutdown is not shown.
- **Loops start at a random point of the poll interval**, like goroutines that are not in step.
- **IDs are random**; time is simulated and starts at 0.

## Stack

Next.js (App Router, static), React, TypeScript, Tailwind CSS, shadcn/ui (Radix), React Flow
(`@xyflow/react`) for the diagram, Recharts for the charts, Zustand for state, Lucide icons,
Geist fonts, Vitest for tests. The channel colors are a validated categorical palette, safe for
color-blind readers, and every color also has a label.

## Deploying on Vercel

Import the repository in Vercel and set **Root Directory** to `web`. Vercel detects Next.js; no
environment variables are needed.
