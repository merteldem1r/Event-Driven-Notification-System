.PHONY: hooks run-api run-worker run-fakeprovider loadtest smoke vulncheck test lint check up down infra-up logs migrate-up migrate-down

# Same versions as CI (.github/workflows/ci.yml). Change both together.
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION := v1.1.4

## hooks: use the repo's Git hooks in .githooks (run once after cloning)
hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks enabled from .githooks/"

## run-api: start the API locally (Ctrl+C to stop)
run-api:
	go run ./cmd/api

## run-worker: start the worker locally (Ctrl+C stops it gracefully)
run-worker:
	go run ./cmd/worker

## run-fakeprovider: start the local fake provider on port 9090 (Ctrl+C to stop)
run-fakeprovider:
	go run ./cmd/fakeprovider

## test: run all tests with the race detector (repository tests need Docker running)
test:
	go test -race ./...

## lint: run golangci-lint in Docker (nothing to install; the cache volume keeps reruns fast)
lint:
	docker run --rm -v "$(CURDIR)":/app -w /app -v golangci-lint-cache:/root/.cache \
		golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run ./...

## vulncheck: fail if the code calls a function with a known vulnerability (Go or a dependency)
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

## smoke: start the stack against the fake provider and check one notification end to end
smoke:
	@# PROVIDER_URL is set here, so a smoke test never goes to webhook.site.
	PROVIDER_URL=http://fakeprovider:9090/ docker compose up -d --build --wait
	./scripts/smoke.sh

## check: the code checks CI runs (vet, lint, tests); run before pushing
check:
	go vet ./...
	$(MAKE) lint
	$(MAKE) test

## up: build and start the whole system in Docker (in the background)
up:
	docker compose up --build -d

## down: stop and remove all containers (database data is kept)
down:
	docker compose down

## infra-up: start PostgreSQL and Redis and apply migrations, for running the API with `make run-api`
infra-up:
	docker compose up -d postgres redis
	$(MAKE) migrate-up

## migrate-up: apply all pending migrations
migrate-up:
	docker compose run --rm migrate up

## migrate-down: roll back the last migration
migrate-down:
	docker compose run --rm migrate down 1

## loadtest: start WORKERS workers (default 3) against the fake provider, send PER_CHANNEL
## notifications (default 1000) on every channel at once, check the rate limit per channel
WORKERS ?= 3
PER_CHANNEL ?= 1000
loadtest:
	@# PROVIDER_URL is set here, so a load test never goes to webhook.site.
	PROVIDER_URL=http://fakeprovider:9090/ docker compose up -d --build --wait --scale worker=$(WORKERS) api worker fakeprovider
	go run ./cmd/loadtest -per-channel $(PER_CHANNEL)
	@echo "The workers still send to the fake provider; 'docker compose up -d --scale worker=1 worker' returns to your .env setting."

## logs: follow the API, worker and fake provider container logs (Ctrl+C stops following, not the containers)
logs:
	docker compose logs -f api worker fakeprovider
