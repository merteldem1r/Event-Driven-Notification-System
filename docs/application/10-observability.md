# 10 — Phase 9: Observability

> **Result:** both programs serve Prometheus metrics and a health check. The API shows the
> queue (how much is waiting, and how long the oldest due notification has waited) and its own
> requests; the worker shows how every send ended and how long sends take. Together with the
> structured logs and correlation IDs from earlier phases, the service can be watched and
> debugged without reading the database.
> **Status:** done.

## What was implemented

| Piece | Where | Notes |
|---|---|---|
| Registry | `metrics.NewRegistry` | One per program, with Go runtime and process metrics; no global registry |
| Queue metrics | `metrics.QueueCollector`, `repository.QueueStats` | Counted in the database on each scrape (D20) |
| API request metrics | `handler.RequestMetrics` | Per method, chi route pattern and status |
| Send metrics | `metrics.Sends`, `service.Recorder` | Reported by the dispatcher for every send |
| Recovered leases | `metrics.Sends.CountRecovered` | Wraps the repository's `RecoverExpired` |
| Worker HTTP server | `cmd/worker`, `handler.NewOpsRouter` | `WORKER_HTTP_PORT` (8081): `/health` and `/metrics` only, requests not logged |
| Health | `handler.Health` | Checks every dependency given; 503 names the one that is down |

## The metrics

| Metric | Type | Where | Answers |
|---|---|---|---|
| `notifications_queue_depth{channel,status}` | gauge | API | How many are `pending` or `processing` right now |
| `notifications_queue_oldest_due_seconds{channel}` | gauge | API | Are we keeping up? Grows when sending falls behind |
| `http_requests_total{method,route,status}` | counter | API | Requests and errors per endpoint |
| `http_request_duration_seconds{method,route}` | histogram | API | API latency |
| `notifications_send_total{channel,result}` | counter | worker | Success and failure rates: `sent`, `retry`, `failed`, `interrupted` |
| `notifications_provider_request_duration_seconds{channel}` | histogram | worker | Is the provider slow? |
| `notifications_delivery_duration_seconds{channel}` | histogram | worker | How long a customer waits, including queue time and retries |
| `notifications_leases_recovered_total` | counter | worker | Did workers crash mid-send? |

Prometheus turns them into rates and percentiles, for example:

```
sum by (channel) (rate(notifications_send_total{result="sent"}[5m]))
  / sum by (channel) (rate(notifications_send_total[5m]))                       success rate
histogram_quantile(0.95, sum by (le, channel) (rate(notifications_delivery_duration_seconds_bucket[5m])))
max by (channel) (notifications_queue_depth{status="pending"})                 backlog
```

## How it works

**The queue is counted, not tracked.** Many processes change the queue, so none of them could
keep a correct running count. Each read of the API's `/metrics` runs one query with two halves,
`pending` and `processing`, each using its partial index. It has a 2 s limit; if it fails, the
rest of `/metrics` is still served and the error is logged. Several API instances all report
the same numbers, hence `max` in queries.

**Sends are reported where they end.** The dispatcher depends only on a small `Recorder`
interface; `metrics.Sends` implements it with Prometheus. Each send is counted once with how it
ended; a delivery also records the time since `created_at`. A send cut off by shutdown counts as
`interrupted` but not as a provider time. All series start at 0 for every channel and result.

**Bounded labels.** Labels are a fixed set: channels, statuses, results, and route patterns
(`/api/v1/notifications/{id}`, not the URL). Unknown URLs share one `unmatched` series, so
scanners or typos can't create new series.

**Health.** The API checks PostgreSQL; the worker checks PostgreSQL and Redis, the two things it
can't send without (D16). Both answer `200 {"status":"ok","checks":{...}}` or `503` with the
unreachable dependency named.

## Trying it

```
curl localhost:8080/metrics | grep ^notifications_      queue depth and oldest due age
curl localhost:8080/health
docker compose port worker 8081                          the worker's port on your Mac (usually 8081)
curl localhost:8081/metrics | grep ^notifications_      send results and durations
docker compose stop redis && curl localhost:8081/health  503, "redis":"unreachable"
docker compose start redis
```

## Not done yet / decided later

- No Prometheus server or dashboard in Compose: the endpoints are ready to be scraped, and the
  queries above show how to use them.
- Docker can't run the health checks itself: the distroless image has no shell or `curl`. They
  are meant for a load balancer or an orchestrator such as Kubernetes.
- Distributed tracing (OPT-5) is an optional feature.
