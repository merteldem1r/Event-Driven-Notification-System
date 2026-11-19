# 03 — Phase 2: Project Skeleton

> **Result:** a runnable API service with configuration, JSON logging, request logging, a health
> endpoint and graceful shutdown, packaged as a Docker image and started together with PostgreSQL
> and Redis by Docker Compose.
> **Status:** steps 2.1–2.4 done; 2.5 (linting + CI) open.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| API entry point | `cmd/api/main.go` | Load config → logger → HTTP server → wait for stop signal → graceful shutdown |
| Configuration | `internal/config` | `HTTP_PORT` (default 8080), `LOG_LEVEL` (default info); invalid values stop startup; `.env.local` loaded for local runs |
| Router | `internal/handler/router.go` | chi; `GET /health` at the root; empty `/api/v1` group for the public API |
| Middleware | `internal/handler/middleware.go` | `RequestLogger` (one JSON line per request), then chi `Recoverer` (panic → 500) |
| Health check | `internal/handler/health.go` | Liveness only: `{"status":"ok"}`. DB/Redis checks come in Phase 3 |
| Docker image | `Dockerfile`, `.dockerignore` | Multi-stage: Go build stage → distroless non-root image (~15 MB) |
| Local stack | `docker-compose.yml` | `api` (built from the Dockerfile), `postgres:17-alpine` (volume), `redis:7-alpine` |
| Commands | `Makefile` | `run-api`, `test`, `up`, `down`, `infra-up`, `logs`, `hooks` |
| Commit rules | `.githooks/commit-msg`, `.github/workflows/commit-messages.yml` | Conventional Commits, checked locally and in CI |

## How it works

**Startup and shutdown.** `ListenAndServe` runs in a goroutine; the main goroutine waits in a
`select` for either a server error (exit 1) or SIGINT/SIGTERM (graceful shutdown with a 10 s limit,
exit 0). Because the API handles SIGTERM itself, `docker stop` ends it cleanly even as PID 1.

**Request path.** `RequestLogger → Recoverer → handler`. The logger is outermost so it also logs
the 500 that Recoverer produces. 5xx responses are logged at ERROR, the rest at INFO.

**Two ways to run.**

```
make up                     everything in Docker:   api :8080, postgres :5433, redis :6379 on your Mac
make infra-up + run-api     postgres + redis in Docker, API runs on your Mac with go run
```

Inside the Compose network, services reach each other by name (`postgres:5432`, `redis:6379`).
PostgreSQL is published on **5433** on the host because a local PostgreSQL already uses 5432.
Database data lives in the `postgres-data` volume and survives `make down`.

**Where configuration comes from.**

| How the API runs | Source of its environment variables |
|---|---|
| `make run-api`, `go run`, IDE | The terminal's variables, plus `.env.local` (optional, gitignored) loaded at startup by `config.LoadDotEnv` (godotenv). Terminal values win over the file; anything unset falls back to the defaults in `internal/config` |
| `make up` | The `environment:` block of the `api` service in `docker-compose.yml`, stored in the container's configuration when Compose creates it |
| Production | The platform running the image (orchestrator + secret store) |

## Not done yet / decided later

- No Docker health check for the API container (distroless has no shell or curl).
- Linting and a CI job for vet, lint and tests (step 2.5).
