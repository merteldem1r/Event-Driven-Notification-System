.PHONY: hooks run-api test docker-build docker-run

IMAGE ?= notification-api

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

## docker-build: build the API Docker image
docker-build:
	docker build -t $(IMAGE) .

## docker-run: run the API image on port 8080 (Ctrl+C to stop)
docker-run:
	docker run --rm -p 8080:8080 $(IMAGE)
