# 04 — Phase 3: Database

> **Result:** the API connects to PostgreSQL at startup and checks it in `/health`. The
> `notifications` table is created by a versioned migration, and Go code can insert and read
> notifications, tested against a real PostgreSQL.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Connection pool | `internal/database/postgres.go` | pgx pool; pings at startup so a missing database stops the API right away |
| Config | `internal/config` | `DATABASE_URL`, required |
| Health check | `internal/handler/health.go` | Pings the database: 200 `ok` or 503 `unavailable` |
| Schema | `migrations/000001_create_notifications.*.sql` | `notifications` table with constraints and two partial indexes |
| Migrations runner | `docker-compose.yml` (`migrate` service) | golang-migrate runs once on `docker compose up`; the API starts only after it succeeds |
| Domain type | `internal/domain/notification.go` | `Notification`, plus the `Channel`, `Priority` and `Status` values; `ErrNotFound` |
| Repository | `internal/repository/notification.go` | `Create` and `GetByID`, hand-written SQL |
| Integration tests | `internal/repository/notification_test.go` | testcontainers starts `postgres:17-alpine` with our migrations applied |
| Commands | `Makefile` | `migrate-up`, `migrate-down`; `infra-up` also applies migrations |

## How it works

**One table is the data and the queue.** A new row has `status = 'pending'`, which means it is
queued. Saving and enqueuing are one `INSERT`, so they can't get out of sync.

**The table has two kinds of columns.**

| Kind | Columns |
|---|---|
| The message | `channel`, `recipient`, `content`, `priority`, `batch_id`, `idempotency_key`, `correlation_id` |
| Queue state | `status`, `attempts`, `next_attempt_at`, `locked_until`, `last_error`, `provider_message_id` |

`priority` is stored as 1 (high), 2 (normal), 3 (low), so sorting ascending puts high first.
`CHECK` constraints reject unknown channels, priorities and statuses, a second line of defense
behind Go validation. `idempotency_key` is `UNIQUE`; several rows may have no key, because
PostgreSQL doesn't treat NULLs as equal.

**Indexes.** `(priority, next_attempt_at) WHERE status = 'pending'` serves the worker's claim
query and stays small because sent rows leave it. `batch_id` is indexed for batch status lookups.

**Repository.** `Create` inserts only the message columns; the database fills in `id`, `status`,
`attempts` and the timestamps from defaults, and `RETURNING` sends the stored row back. Both
methods read the same column list through one scan function. Optional fields are pointers in Go,
so `nil` becomes `NULL`. A missing row becomes `domain.ErrNotFound`, so callers don't depend on pgx.

**Tests use a real database.** Mocks can't catch wrong SQL, wrong column types or constraint
behavior. `TestMain` starts one PostgreSQL container per test run, loads the migration files as
init scripts, and removes the container afterwards. Each test creates its own rows with random
IDs, so tests don't interfere with each other. Docker must be running for `make test`.

**Looking at the database by hand.**

```
docker compose exec postgres psql -U notification -d notification
\dt                         list tables
\d notifications            columns, constraints, indexes
SELECT * FROM schema_migrations;   applied migration version
```

## Not done yet / decided later

- Validation rules and content limits (D4) come in Phase 4; the repository stores what it is given.
- Duplicate idempotency keys currently return a raw database error; Phase 4 turns this into a proper response.
- Tests load migrations as init scripts, not through golang-migrate; Compose exercises golang-migrate itself.
