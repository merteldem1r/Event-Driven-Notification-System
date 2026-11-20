# 14 — Phase 14: CI/CD

> **Result:** every push and pull request runs six jobs: lint, tests, a vulnerability scan, the
> migrations both ways, an end-to-end smoke test, and the Docker image. On `main`, once all the
> others pass, the image is published to GitHub Container Registry.
> **Status:** done.

## What was implemented

| Job | What it proves | Locally |
|---|---|---|
| `lint` | golangci-lint finds nothing | `make lint` |
| `test` | `go vet`, every test with the race detector, real PostgreSQL and Redis | `make test` |
| `vulncheck` | the code reaches no known vulnerability in Go or a dependency | `make vulncheck` |
| `migrations` | every migration applies, rolls back (`down -all`) and applies again | CI only (it drops everything) |
| `smoke` | the built image and Compose stack work: both `/health` are `200`, one notification reaches `sent` | `make smoke` |
| `image` | the Dockerfile builds; on `main`, pushes `ghcr.io/merteldem1r/event-driven-notification-system` | `docker build .` |

The separate `commit-messages` workflow still checks Conventional Commits.

## How it works

```
push / pull request
  lint ─┐
  test ─┤
  vulncheck ─┼─► image: build ──(main only)──► push sha-<commit>, latest
  migrations ┤
  smoke ─┘
```

**Parallel, then gated.** The five checks run side by side, so the run takes about as long as
the slowest. The image job waits for all of them: nothing is published from a commit that
failed a check.

**No secrets.** The push uses the run's own `GITHUB_TOKEN`, valid for that run only. Only the
image job is allowed to write packages; every other job can only read the code.

**The smoke test** is `scripts/smoke.sh`: plain `sh` and `curl`, so it runs the same on a laptop
and in CI. It checks the API's and the worker's `/health`, sends an SMS and waits up to 30 s
for `sent`; on failure CI prints the container logs. It was checked both ways: it passes on the
normal stack and fails (`still pending after 30 s`) when the fake provider rejects every request.

**The scan found a real problem.** `go.mod` said `go 1.26.0`, and CI installs exactly that, so
CI built with a standard library that has 31 known vulnerabilities our code reaches, e.g. in
`crypto/x509` through the provider's HTTPS client. `go.mod` now says `go 1.26.11` (Go downloads
it automatically where an older Go is installed); the Docker image already used the newest 1.26.

## Trying it

```
make vulncheck        No vulnerabilities found.
make smoke            ok ... health, ok created <id>, ok <id> sent
push to a branch      the Actions tab shows six jobs; the image is built, not pushed
push to main          the image appears under the repository's Packages
```

## Not done yet / decided later

- No load test in CI: shared runners have uneven timing (D23); `make loadtest` stays local.
- No Dependabot; dependencies are updated by hand (D23).
- No deployment target: the image is ready to run anywhere, but nothing deploys it.
