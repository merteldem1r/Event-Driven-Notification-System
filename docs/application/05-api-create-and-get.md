# 05 — Phase 4: API, Create and Get One Notification

> **Result:** clients can create a notification (`POST`, answered with `202 Accepted`) and read it
> back with its status (`GET`). Input is validated per channel, retries are safe with an
> idempotency key, every request carries a correlation ID through the logs, every error has one
> JSON format, and the OpenAPI spec is checked against the code by tests.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Validation rules | `internal/domain` | `Notification.Validate`: channel, recipient format, content limits, priority, key format; all errors at once (D4) |
| Use cases | `internal/service` | `Create` (validate, save, idempotent replay) and `Get`; depends on a small `NotificationStore` interface |
| Create endpoint | `POST /api/v1/notifications` | `202` + full notification + `Location`; priority defaults to `normal` |
| Get endpoint | `GET /api/v1/notifications/{id}` | `200`; malformed ID `400`, unknown ID `404` |
| Error format | `internal/handler/respond.go` | `{"error": {code, message, details}}` with stable codes; also for unknown routes (`404`) and wrong methods (`405` + `Allow`) (D11) |
| Strict body reading | `internal/handler/request.go` | 1 MB cap (`413`), unknown fields rejected, one JSON object only |
| Idempotency | handler, service, repository | `Idempotency-Key` header; `INSERT ... ON CONFLICT DO NOTHING` (D12) |
| Correlation ID | `internal/handler/middleware.go`, `internal/logging` | `X-Correlation-ID` in and out, added to every log line, stored on the row (D13) |
| API spec | `docs/openapi.yaml` | OpenAPI 3.0.3; tested by `internal/handler/openapi_test.go` |

## How it works

**The request path.**

```
CorrelationID → RequestLogger → Recoverer → handler → service → repository → PostgreSQL
 (ID in ctx)    (1 line/request) (panic→500)  (HTTP)    (rules)    (SQL)
```

The handler reads JSON strictly, applies the priority default, and passes a `domain.Notification`
to the service. The service validates it and saves it. Errors travel back as domain errors
(`*ValidationError`, `ErrNotFound`, `ErrIdempotencyKeyReused`), and only `writeServiceError` in the
handler turns them into status codes. Unexpected errors become a generic `500`; the cause goes
to the log under the request's correlation ID, never to the client.

**Idempotency.** A client names each logical action with an `Idempotency-Key` and reuses it on
retries. The `UNIQUE` index on `idempotency_key` decides atomically which request creates the row.
A repeat with the same body returns the stored notification (`202`, `Idempotent-Replayed: true`);
a repeat with a different body is `422`. A concurrency test with 20 simultaneous requests against
real PostgreSQL proves only one row is created.

**Correlation ID.** The middleware keeps a safe client ID or generates a UUID, puts it in the
request context and the response header. A slog handler wrapper adds it to every log line written
with that context, so no code adds it by hand. It's stored on the notification so the worker can
log with the same ID later.

**The spec can't drift.** Tests check that `docs/openapi.yaml` is valid, that it lists exactly the
routes the router has, and that real handler responses (success and every error) match the
documented schemas and status codes.

## Trying it

```
make run-api                                   (with make infra-up running)
POST http://localhost:8080/api/v1/notifications
     {"channel":"sms","recipient":"+905551234567","content":"hello","priority":"high"}
GET  http://localhost:8080/api/v1/notifications/<id>
```

Import `docs/openapi.yaml` into Postman to get every request ready to send.

## Not done yet / decided later

- Notifications stay `pending`: nothing sends them until the worker (Phase 6).
- Batch create, list with filters and cancel come in Phase 5.
- Swagger UI for the spec comes in Phase 15.
