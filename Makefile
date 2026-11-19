.PHONY: hooks

## hooks: use the repo's Git hooks in .githooks (run once after cloning)
hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks enabled from .githooks/"
