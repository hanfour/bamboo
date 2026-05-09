.DEFAULT_GOAL := help

# ----- Tool versions ---------------------------------------------------------

BUF_VERSION             ?= v1.45.0
GOLANGCI_LINT_VERSION   ?= v2.0.2
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
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
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

license-check: ## Verify every hand-written .go file has an SPDX header
	@bash scripts/check-license-headers.sh

proto-check: ## Verify generated protobuf code is up to date with proto/
	@command -v buf >/dev/null || { echo "buf not installed; run 'make bootstrap'"; exit 1; }
	@cd proto && buf generate
	@if ! git diff --quiet --exit-code -- proto/gen; then \
	  echo "proto/gen is out of date. Run 'make proto' and commit the result." >&2; \
	  git --no-pager diff --stat -- proto/gen; \
	  exit 1; \
	fi
	@echo "OK: proto/gen matches proto/ sources."

verify: lint test license-check proto-check ## Combined lint + test + license + proto-drift (CI gate)

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
	@if [ -d clients/cli/cmd/bamboo ]; then \
	  echo "==> build bamboo"; \
	  (cd clients/cli && go build -o ../../bin/bamboo ./cmd/bamboo); \
	fi
	@echo "==> done. binaries in ./bin"

# ----- AI (Python) -----------------------------------------------------------

ai-install: ## Set up apps/ai virtualenv + dev deps
	@cd apps/ai && python3 -m venv .venv && \
	  ./.venv/bin/pip install --upgrade pip --quiet && \
	  ./.venv/bin/pip install -e '.[dev]' --quiet

ai-lint: ## ruff check apps/ai
	@cd apps/ai && ./.venv/bin/ruff check src tests

ai-test: ## pytest apps/ai
	@cd apps/ai && ./.venv/bin/pytest

# ----- Apple clients (macOS + iOS) -------------------------------------------
#
# These targets only run on macOS with Xcode 15+ and `xcodegen` installed
# (`brew install xcodegen`). They are deliberately kept off the main
# `verify` gate because CI does not have macOS runners — but they are
# the local fast path for the bamboo Apple developers.
#
# Required environment:
#   BAMBOO_DEVELOPMENT_TEAM     Apple Team ID (10-char alphanumeric)
#   BAMBOO_BUNDLE_ID_PREFIX     Reverse-DNS prefix; default dev.hanfour.bamboo

apple-generate: ## Run xcodegen to materialize clients/apple/bamboo.xcodeproj
	@command -v xcodegen >/dev/null || { echo "xcodegen not installed; run 'brew install xcodegen'"; exit 1; }
	@cd clients/apple && xcodegen generate
	@echo "==> clients/apple/bamboo.xcodeproj"

apple-build-macos: apple-generate ## Build the macOS app + tunnel extension (Debug)
	@cd clients/apple && xcodebuild \
	  -project bamboo.xcodeproj \
	  -scheme BambooApp-macOS \
	  -destination 'platform=macOS' \
	  -configuration Debug \
	  build

apple-build-ios: apple-generate ## Build the iOS app + tunnel extension for the simulator
	@cd clients/apple && xcodebuild \
	  -project bamboo.xcodeproj \
	  -scheme BambooApp-iOS \
	  -destination 'generic/platform=iOS Simulator' \
	  -configuration Debug \
	  build

# ----- Web (Next.js) ---------------------------------------------------------

web-install: ## Install Node dependencies for apps/web
	@cd apps/web && npm install --no-audit --no-fund

web-lint: ## Lint apps/web (next lint + tsc --noEmit)
	@cd apps/web && npm run lint && npm run typecheck

web-build: ## Production build apps/web
	@cd apps/web && npm run build

web-dev: ## Run the apps/web dev server on http://localhost:3000
	@cd apps/web && npm run dev

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
