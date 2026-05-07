.DEFAULT_GOAL := help

.PHONY: help bootstrap test lint build dev clean proto

help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

bootstrap: ## Install all toolchain dependencies
	@echo "==> bootstrap (placeholder — wire up Go, Node, Python toolchains)"

test: ## Run all tests across modules
	@echo "==> test (placeholder)"

lint: ## Run linters across modules
	@echo "==> lint (placeholder)"

build: ## Build all artifacts
	@echo "==> build (placeholder)"

proto: ## Regenerate gRPC code from proto/
	@cd proto && buf lint && buf generate

dev: ## Start local development stack (docker-compose)
	@cd infra && docker compose up

clean: ## Remove build artifacts
	@find . \( -name "dist" -o -name "build" -o -name "*.exe" \) -prune -exec rm -rf {} + 2>/dev/null || true
	@echo "==> cleaned"
