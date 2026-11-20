# 11 — Phase 15: Proof and Polish

> **Result:** the service can be understood, started and explored without reading code. A
> README explains the idea, the building blocks and the design with diagrams and measured
> results; the API documents itself in the browser; a load test proves the rate limit.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Load test | `cmd/loadtest`, `internal/loadtest`, `make loadtest` | Done early, in Phase 7 (see 08). Fails if the provider gets more than 100 per channel in any one second |
| README | `README.md` | The idea, building blocks, getting started, how it works step by step, decisions, failure behavior, results, reference |
| Swagger UI | `GET /docs`, `handler.SwaggerUI` | Interactive docs with "Try it out" against the running API |
| Specification | `GET /openapi.yaml`, `docs` package | `docs/openapi.yaml`, built into the API binary |

## How it works

**README.** Written for a reader who has a few minutes: first the problem and the core idea
(bursts in, steady out), then the five building blocks, then how to run it, then how a
notification travels, one step and one small diagram at a time. Design decisions and failure
behavior are short tables; the measured results are in plain words with one chart, and the full
numbers are folded away. The diagrams are Mermaid, drawn by GitHub from plain text in the file.

**Swagger UI.** `GET /docs` returns a small HTML page that loads Swagger UI from the jsDelivr CDN
and points it at `/openapi.yaml`. The version is pinned and the files carry integrity hashes,
so the browser refuses them if the CDN ever serves anything else. Because the page comes from
the API itself, "Try it out" sends requests to the same address with no extra setup.

**The specification is built in.** The `docs` package embeds `docs/openapi.yaml` into the binary
with `go:embed`, so the API serves the exact spec it was built with. A test compares the served
bytes with the file, and the existing tests keep the file matched to the routes and the real
responses. The Docker build, which otherwise leaves `docs/` out, lets these two files in.

## Trying it

```
docker compose up -d --build --wait
open http://localhost:8080/docs           Swagger UI; "Try it out" on POST /api/v1/notifications
curl localhost:8080/openapi.yaml
make loadtest
```

## Not done yet / decided later

- Swagger UI needs internet access in the browser (the CDN); the spec itself works offline.
- Optional features (scheduled notifications, templates, WebSocket updates, tracing, full CI/CD)
  are chosen and built after this phase.
