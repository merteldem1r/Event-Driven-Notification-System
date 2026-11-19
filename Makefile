.PHONY: hooks run-api test up down infra-up logs

## hooks: use the repo's Git hooks in .githooks (run once after cloning)
hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks enabled from .githooks/"

## run-api: start the API locally (Ctrl+C to stop)
run-api:
	go run ./cmd/api

## test: run all tests
test:
	go test ./...

## up: build and start the whole system in Docker (in the background)
up:
	docker compose up --build -d

## down: stop and remove all containers (database data is kept)
down:
	docker compose down

## infra-up: start only PostgreSQL and Redis, for running the API with `make run-api`
infra-up:
	docker compose up -d postgres redis

## logs: follow the API container logs (Ctrl+C stops following, not the API)
logs:
	docker compose logs -f api
