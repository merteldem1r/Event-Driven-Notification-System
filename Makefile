.PHONY: hooks run-api test lint check up down infra-up logs migrate-up migrate-down

# Same version as CI (.github/workflows/ci.yml). Change both together.
GOLANGCI_LINT_VERSION := v2.15.0

## hooks: use the repo's Git hooks in .githooks (run once after cloning)
hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks enabled from .githooks/"

## run-api: start the API locally (Ctrl+C to stop)
run-api:
	go run ./cmd/api

## test: run all tests with the race detector (repository tests need Docker running)
test:
	go test -race ./...

## lint: run golangci-lint in Docker (nothing to install; the cache volume keeps reruns fast)
lint:
	docker run --rm -v "$(CURDIR)":/app -w /app -v golangci-lint-cache:/root/.cache \
		golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint run ./...

## check: everything CI checks (vet, lint, tests); run before pushing
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

## logs: follow the API container logs (Ctrl+C stops following, not the API)
logs:
	docker compose logs -f api
