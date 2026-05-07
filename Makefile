.DEFAULT_GOAL := help

# ----- Tool versions ---------------------------------------------------------

BUF_VERSION             ?= v1.45.0
GOLANGCI_LINT_VERSION   ?= v1.61.0
GOOSE_VERSION           ?= v3.22.1
PROTOC_GEN_GO_VERSION   ?= v1.34.2
PROTOC_GEN_GO_GRPC_VERSION ?= v1.5.1

# ----- Discovery -------------------------------------------------------------

# All Go modules: each directory containing a go.mod, excluding node_modules.
GO_MODULES := $(shell find . -name go.mod -not -path "./node_modules/*" -not -path "*/node_modules/*" | xargs -n1 dirname)

# ----- Phony -----------------------------------------------------------------

.PHONY: help bootstrap doctor tidy test lint build proto fmt verify \
        dev dev-down dev-logs clean

# ----- Help (default) --------------------------------------------------------

help: ## Show available commands
	@printf "Usage: make <target>\n\n"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

# ----- Toolchain -------------------------------------------------------------

bootstrap: ## Install required dev toolchain (buf, golangci-lint, goose, protoc plugins)
	@echo "==> installing buf $(BUF_VERSION)"
	@go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	@echo "==> installing golangci-lint $(GOLANGCI_LINT_VERSION)"
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@echo "==> installing goose $(GOOSE_VERSION)"
	@go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	@echo "==> installing protoc-gen-go $(PROTOC_GEN_GO_VERSION)"
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	@echo "==> installing protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION)"
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	@echo "==> done."
	@echo "   Ensure \$$GOPATH/bin (or ~/go/bin) is on your PATH."

doctor: ## Report toolchain status without installing anything
	@printf "go              "; go version 2>/dev/null            || echo "MISSING"
	@printf "buf             "; buf --version 2>/dev/null         || echo "MISSING — run 'make bootstrap'"
	@printf "golangci-lint   "; golangci-lint --version 2>/dev/null | head -1 || echo "MISSING — run 'make bootstrap'"
	@printf "goose           "; goose -version 2>/dev/null        || echo "MISSING — run 'make bootstrap'"
	@printf "docker          "; docker --version 2>/dev/null      || echo "MISSING"
	@printf "modules         "; echo "$(GO_MODULES)"

# ----- Go module operations --------------------------------------------------

tidy: ## go mod tidy across all Go modules
	@for m in $(GO_MODULES); do \
	  echo "==> tidy $$m"; \
	  (cd $$m && go mod tidy) || exit 1; \
	done

test: ## go test across all Go modules (race-detector enabled)
	@for m in $(GO_MODULES); do \
	  echo "==> test $$m"; \
	  (cd $$m && go test -race ./...) || exit 1; \
	done

lint: ## golangci-lint across all Go modules
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed; run 'make bootstrap'"; exit 1; }
	@for m in $(GO_MODULES); do \
	  echo "==> lint $$m"; \
	  (cd $$m && golangci-lint run ./...) || exit 1; \
	done

fmt: ## gofmt all Go code
	@for m in $(GO_MODULES); do \
	  (cd $$m && gofmt -w .); \
	done

verify: lint test ## Combined lint + test (CI gate)

# ----- Build -----------------------------------------------------------------

build: ## Build all binaries into ./bin
	@mkdir -p bin
	@if [ -d apps/controller/cmd/controller ]; then \
	  echo "==> build controller"; \
	  (cd apps/controller && go build -o ../../bin/controller ./cmd/controller); \
	fi
	@if [ -d clients/core/cmd/dev-agent ]; then \
	  echo "==> build dev-agent"; \
	  (cd clients/core && go build -o ../../bin/dev-agent ./cmd/dev-agent); \
	fi
	@echo "==> done. binaries in ./bin"

# ----- Protos ----------------------------------------------------------------

proto: ## Lint protos and regenerate code (buf)
	@command -v buf >/dev/null || { echo "buf not installed; run 'make bootstrap'"; exit 1; }
	@cd proto && buf lint && buf generate
	@echo "==> proto generated under proto/gen/"

proto-breaking: ## Detect breaking changes vs origin/main
	@command -v buf >/dev/null || { echo "buf not installed; run 'make bootstrap'"; exit 1; }
	@cd proto && buf breaking --against '.git#branch=origin/main,subdir=proto' || true

# ----- Dev stack -------------------------------------------------------------

dev: ## Start local dev stack (Postgres + Redis)
	@docker compose -f infra/docker-compose.yml up -d
	@echo "==> dev stack up. Postgres on :15432, Redis on :16379"

dev-down: ## Stop local dev stack
	@docker compose -f infra/docker-compose.yml down

dev-logs: ## Follow dev stack logs
	@docker compose -f infra/docker-compose.yml logs -f

# ----- Cleanup ---------------------------------------------------------------

clean: ## Remove build artifacts
	@rm -rf bin/ dist/
	@find . -name "*.exe" -not -path "./node_modules/*" -delete 2>/dev/null || true
	@echo "==> cleaned"
