# 06 — Phase 5: API, Batch, List and Cancel

> **Result:** clients can create up to 1,000 notifications in one request, follow a batch's
> progress, list notifications with filters page by page, and cancel single notifications or a
> whole batch before they are sent. Batch creates are idempotent, and cancel can't race the worker.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Batch create | `POST /api/v1/notifications/batch` | 1 to 1,000 items, all-or-nothing validation with positional errors, one `unnest` insert, `202 {batch_id, count, ids}` + `Location` (D5) |
| Batch idempotency | service, repository | `Idempotency-Key` for the whole batch; item `i` stored as `<key>#<i>`; insert in a transaction (D12) |
| Batch status | `GET /api/v1/batches/{id}` | Total and a count for every status, zeros included; one `GROUP BY` |
| List | `GET /api/v1/notifications` | Filters `status`, `channel`, `batch_id`, `from`/`to`; newest first; cursor pagination (D6) |
| List index | `migrations/000002_*` | `(created_at, id)`, built `CONCURRENTLY` so inserts aren't blocked |
| Cancel | `POST /api/v1/notifications/{id}/cancel` | One conditional `UPDATE`; too late `409 not_cancellable`, repeat `200` (D14) |
| Batch cancel | `POST /api/v1/batches/{id}/cancel` | Cancels what is still pending; returns `cancelled` plus the counts afterwards |
| API spec | `docs/openapi.yaml` | Every new endpoint, checked by the same spec tests as Phase 4 |

## How it works

**Batch create.** Every item is validated first; one invalid item rejects the request with a
`400` naming each problem by position (`notifications[3].recipient`). Valid batches are saved in
one `INSERT ... SELECT FROM unnest(...)` statement, so all rows exist or none do. IDs are made in
Go, so the response lists them in request order.

**Batch idempotency.** The batch key `K` becomes item keys `K#0 ... K#n-1`, so the existing unique
index protects batches too. If any item key is taken, the transaction rolls back and the service
looks up the stored batch: it is a replay only if it has the same number of items, each the same
request, under one batch ID (the lookup also checks `K#n`, so a shorter request can't replay part
of a longer batch). Anything else is `422`. Identical batches sent at the same time wait on the
unique index; one creates, the others replay.

**Cursor pagination.** A page ends with `next_cursor`, an opaque token for the last row's
`(created_at, id)`. The next query continues with `WHERE (created_at, id) < cursor` on the index,
so every page costs the same at any depth, and rows arriving meanwhile (always newer) never shift
later pages. One extra row is fetched to know whether a next page exists; there is no total count.
Every filter is a `$n IS NULL OR ...` condition in one static query.

**Cancel without a race.** Cancel and the worker's claim both change a row only
`WHERE status = 'pending'`, in one statement. The row lock makes the second one wait, re-check the
condition and change nothing, so exactly one wins. When nothing changed, the service reads the row
to answer `404`, `200` (already cancelled) or `409` (past pending). Scheduled and retry-waiting
notifications are `pending` too, so they can be cancelled. Batch cancel is the same `UPDATE` with
`WHERE batch_id = $1`. Tests race a cancel against simulated worker claims on real PostgreSQL.

## Trying it

```
make run-api                                   (with make infra-up running)
POST http://localhost:8080/api/v1/notifications/batch     {"notifications":[{...},{...}]}
GET  http://localhost:8080/api/v1/batches/<batch_id>
GET  http://localhost:8080/api/v1/notifications?batch_id=<batch_id>&limit=2   then &cursor=<next_cursor>
POST http://localhost:8080/api/v1/notifications/<id>/cancel
POST http://localhost:8080/api/v1/batches/<batch_id>/cancel
```

## Not done yet / decided later

- Notifications still stay `pending` until the worker exists (Phase 6); statuses were changed by
  hand in psql for testing.
- List has no `recipient` filter and no `prev_cursor`; both can be added when a UI needs them.
