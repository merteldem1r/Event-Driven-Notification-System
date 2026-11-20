# 13 — Phase 11: Templates

> **Result:** clients store message bodies with `{{name}}` placeholders once, then create
> notifications by naming the template and giving the values. The API fills them in before
> storing, so the stored content is the final text and workers didn't change.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Template rules | `domain.Template`: `Validate`, `Variables`, `Render` | Name `[a-z0-9][a-z0-9_.-]{0,63}`; body up to 100,000 characters; braces that aren't a placeholder rejected |
| Storage | migration 000007 (`templates`), `repository.Templates` | Name is the primary key; `ON CONFLICT` decides a taken name atomically |
| Endpoints | `POST /api/v1/templates`, `GET /api/v1/templates/{name}`, `GET /api/v1/templates` | `201` with `Location`; `409 template_exists`; responses list the variables |
| Use in notifications | `template` + `variables` on single and batch creates, `service.Notifications.render` | Rendered at creation, then validated like any content |
| Traceability | migration 000008, `notifications.template` | The template's name, with a foreign key; shown in responses |

## How it works

```
POST /api/v1/templates {"name":"order_shipped","body":"Hi {{first_name}}, order {{order_id}} has shipped."}
  -> 201, "variables":["first_name","order_id"]

POST /api/v1/notifications {"channel":"sms",...,"template":"order_shipped","variables":{"first_name":"Ayse","order_id":"1042"}}
  -> load the template (once per name in a batch)
  -> every placeholder has a value?    no: 400 "variables.order_id": "is required by template order_shipped"
  -> content = "Hi Ayse, order 1042 has shipped."
  -> the usual checks on the result (e.g. at most 1,600 characters for SMS), then stored and queued
```

**Rendered at creation.** The stored `content` is exactly what will be sent. Validation, length
limits, idempotent replays (same template, same values, same content) and the worker all work
on plain content as before.

**Safe by construction.** No logic in templates, only substitution. Values are inserted as they
are, so a value containing `{{...}}` is not filled in again. Templates never change, so a name
always means the same text; a new wording is a new name.

**Errors are the client's when they can be.** An unknown template, a missing variable, `content`
together with `template`, or `variables` without a template are `400 validation_failed` naming
the field (`notifications[2].variables.order_id` in a batch). Values must be strings; a number
is a `400 invalid_body`. Only a failed database lookup is a `500`.

## Trying it

```
POST /api/v1/templates with a body using {{first_name}} and {{order_id}}     201, variables listed
the same name again                                                         409 template_exists
"body":"Hi {{first name}}"                                                  400, malformed placeholder
POST /api/v1/notifications with "template" and both variables              202, content filled in; sent ~0.1 s later
the same with "order_id" left out                                          400 variables.order_id
a batch with an unknown template                                           400 notifications[1].template
```

## Not done yet / decided later

- No update or delete; no conditions, loops or formatting in templates (D22).
- No email subject: the service has one `content` field for every channel.
- The template list is not paginated (at most 1,000): templates are configuration, so there are few.
