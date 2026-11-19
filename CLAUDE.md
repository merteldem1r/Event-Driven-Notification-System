# CLAUDE.md

Starting point for every session. It describes what the project is and the rules we follow.
It is intentionally general and rarely changes. Progress, requirements and design details
live in `docs/`.

## The project

An **event-driven notification service** written in Go. Client systems call an HTTP API to send
**SMS, email and push** notifications. The API validates each request, stores it, and answers
`202 Accepted` immediately. Background **workers** take notifications from a queue by priority,
respect a **shared rate limit of 100 messages per second per channel**, send them to an external
provider over HTTP, retry failures with backoff, and record every status change. The service
exposes metrics, structured logs with correlation IDs, and health checks.

Core idea: **requests arrive in bursts, sending happens at a fixed rate, and the queue absorbs
the difference.**

## Architecture

```
client ──► api (cmd/api) ──► PostgreSQL ◄── workers (cmd/worker) ──► external provider
                              (data + queue)          │
                                                      ▼
                                                    Redis (shared rate limiter)
```

- **Two programs**, `cmd/api` and `cmd/worker`, share code in `internal/` and scale independently.
- **PostgreSQL** is both the source of truth and the queue (workers claim jobs with
  `FOR UPDATE SKIP LOCKED`). Saving a notification is enqueuing it.
- **Redis** holds the rate limiter shared by all workers.
- **The provider sits behind a Go interface**, so the real provider and test doubles are interchangeable.

**Stack:** Go, chi, pgx (hand-written SQL), golang-migrate, log/slog, Prometheus, Redis,
Docker Compose, GitHub Actions, OpenAPI.

## Where to look

| Path | Contents |
|---|---|
| `docs/application/` | Documents about the product and its design (numbered, read in order) |
| `docs/plan.md` | The phase-by-phase plan and **the current step**. Read this to know where we are |
| `docs/requirements.md` | Every requirement as a checklist with its status |
| `docs/openapi.yaml` | API specification |
| `Makefile` | Common commands (run, test, setup) |

The original requirements PDF in `docs/` is local only (gitignored); `docs/requirements.md` is its in-repo version.

## Folder structure

```
cmd/<program>/      entry points: wiring and startup only, no business logic
internal/
  config/           settings from environment variables (the only place that reads env)
  domain/           core types and rules; imports nothing else from internal/
  handler/          HTTP layer: routes, request/response JSON, error → status code mapping
  service/          use cases; orchestrates repositories, queue and other dependencies
  repository/       database access; all SQL lives here
  database/         database connection setup
  provider/         external provider interface and implementations
  ratelimit/        shared rate limiter
  worker/           worker loops and graceful shutdown
  logging/          request-scoped log fields (correlation ID)
migrations/         versioned SQL schema migrations
docs/               documentation
.githooks/          local Git hooks (enable with `make hooks`)
.github/workflows/  CI
```

New packages follow the same idea: one clear responsibility per package, named after what it does.

## Architecture rules

- **Dependencies point inward:** `handler → service → repository / provider / ratelimit → domain`.
  HTTP stays in `handler`, SQL stays in `repository`, business rules stay in `domain` and `service`.
- **Small interfaces, defined where they're used**, implemented by concrete packages.
- **Constructor injection, no globals.** Dependencies are passed into `New...` functions and wired
  in `cmd/*/main.go`.
- **`context.Context` is the first parameter** of anything that does I/O and is always passed down.
- **SOLID, pragmatically:** single responsibility, depend on abstractions at the boundaries
  (database, queue, provider, limiter), no abstraction without a real need.

## Coding conventions

- `make check` (vet, golangci-lint, tests with the race detector) passes before every commit; CI runs the same checks.
- **Errors:** return, don't panic; wrap with context (`fmt.Errorf("doing x: %w", err)`); only the
  HTTP layer maps errors to status codes; never ignore an error without a comment explaining why.
- **Logging:** `log/slog`, JSON to stdout, key-value fields; include correlation and notification IDs
  when known; never log secrets or message content.
- **Config:** environment variables with defaults, read only in `internal/config`, invalid values
  fail at startup, every variable documented in `.env.example`.
- **HTTP API:** public routes under `/api/v1`; operational endpoints (`/health`, `/metrics`) at the root;
  JSON bodies; asynchronous creates return `202 Accepted`; one consistent error format.
- **Tests:** table-driven where it fits; `httptest` for handlers; real PostgreSQL/Redis via
  testcontainers for data-access code; the whole suite runs with `make test`.
- **Comments** are short and simple: one line where possible, explain *why* only when it isn't obvious. Every package and exported identifier has a brief doc comment.
- **Files:** split a package into files only when a file gets large or holds a clearly separate concern; `x_test.go` tests `x.go`.
- **Docs:** plain language, no emojis.

## How we work

- The user is learning the system in depth and must be able to explain every line. **Explain the
  why before the how**, in plain language.
- **Small steps.** Each step is one small, working, tested change. After each step: explain what was
  built and why, show how to try it, give the **exact `git add` and `git commit` commands**
  (explicit file paths, no `git add .`), then **stop** for questions.
- **Never commit or push.** The user commits. Messages follow Conventional Commits
  (`feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`),
  enforced by the commit-msg hook and CI.
- **Design decisions belong to the user.** Present the options, trade-offs and a recommendation;
  record the decision in the decision log under `docs/application/`.
- **Keep the trackers current:** update `docs/plan.md` and `docs/requirements.md` after each step.
- **One document per big phase:** `docs/application/` holds one numbered document per completed
  phase, read in order. Each is a short summary of what was implemented and how it works in general;
  not a tutorial, and not updated for every small step.
- This is a **standalone project**. Docs don't refer to any company or describe it as an assessment.
- Prompts pasted from other tools are drafts, not ground truth. Check them against the docs and code.
