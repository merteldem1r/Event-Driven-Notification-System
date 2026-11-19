.PHONY: hooks run-api test

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
