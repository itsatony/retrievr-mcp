.DEFAULT_GOAL := help
.PHONY: help build mcp cli test test-fast vet fmt fmt-check lint lint-install ci ci-fast clean install-hooks

# Match the golangci-lint version pinned in .github/workflows/ci.yaml.
GOLANGCI_LINT_VERSION := v1.64.8
GOLANGCI_LINT := $(shell go env GOPATH)/bin/golangci-lint

COVERAGE_FILE := coverage.out
COVERAGE_MIN  := 80

help:  ## list targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# --------------------------------------------------------------------
# Build
# --------------------------------------------------------------------

build: mcp cli  ## build both binaries

mcp:  ## build the MCP server binary
	go build -o retrievr-mcp ./cmd/retrievr-mcp

cli:  ## build the CLI binary
	go build -o retrievr-cli ./cmd/retrievr-cli

# --------------------------------------------------------------------
# Tests
# --------------------------------------------------------------------

test:  ## go test -race + coverage check (matches CI exactly)
	go test -race -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./...
	@COVERAGE=$$(go tool cover -func=$(COVERAGE_FILE) | grep total | awk '{print $$3}' | tr -d '%'); \
	echo "Coverage: $${COVERAGE}%"; \
	if [ "$$(echo "$$COVERAGE < $(COVERAGE_MIN)" | bc -l)" -eq 1 ]; then \
		echo "FAIL: coverage $${COVERAGE}% is below $(COVERAGE_MIN)% threshold"; \
		exit 1; \
	fi

test-fast:  ## go test (no race, no coverage; fast iteration)
	go test ./...

# --------------------------------------------------------------------
# Lint / format
# --------------------------------------------------------------------

vet:  ## go vet ./...
	go vet ./...

fmt:  ## gofmt -w . (auto-format)
	gofmt -w .

fmt-check:  ## gofmt -l . (check only — fail if any unformatted file)
	@UNFORMATTED=$$(gofmt -l .); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "FAIL: files not formatted:"; \
		echo "$$UNFORMATTED"; \
		echo ""; \
		echo "Run 'make fmt' to fix."; \
		exit 1; \
	fi

lint-install:  ## install golangci-lint at the CI-pinned version
	@if [ ! -x "$(GOLANGCI_LINT)" ] || ! $(GOLANGCI_LINT) --version 2>/dev/null | grep -q "$(GOLANGCI_LINT_VERSION:v%=%)"; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION) (CI-pinned version)"; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	else \
		echo "golangci-lint $(GOLANGCI_LINT_VERSION) already installed"; \
	fi

lint: lint-install  ## golangci-lint run ./... (CI-pinned version)
	$(GOLANGCI_LINT) run ./...

# --------------------------------------------------------------------
# CI simulation — matches .github/workflows/ci.yaml step-for-step.
#
# This is the single source of truth for "would CI pass right now?"
# Run before every push. The pre-push git hook (install via
# `make install-hooks`) calls this target automatically.
# --------------------------------------------------------------------

ci:  ## simulate the full GitHub CI pipeline locally
	@echo "=== retrievr-mcp local CI sim ==="
	@echo
	@echo "[1/7] go mod tidy check"
	@go mod tidy
	@git diff --exit-code go.mod go.sum >/dev/null 2>&1 || { \
		echo "FAIL: go.mod/go.sum drift after 'go mod tidy'"; \
		git diff go.mod go.sum; \
		exit 1; \
	}
	@echo "  ✓ tidy"
	@echo
	@echo "[2/7] go build ./..."
	@go build ./...
	@echo "  ✓ build"
	@echo
	@echo "[3/7] go vet ./..."
	@go vet ./...
	@echo "  ✓ vet"
	@echo
	@echo "[4/7] gofmt -l ."
	@$(MAKE) --no-print-directory fmt-check
	@echo "  ✓ gofmt"
	@echo
	@echo "[5/7] golangci-lint run ./... (CI version: $(GOLANGCI_LINT_VERSION))"
	@$(MAKE) --no-print-directory lint
	@echo "  ✓ lint"
	@echo
	@echo "[6/7] go test -race -coverprofile -covermode=atomic ./..."
	@go test -race -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./...
	@echo "  ✓ tests"
	@echo
	@echo "[7/7] coverage threshold (>= $(COVERAGE_MIN)%)"
	@COVERAGE=$$(go tool cover -func=$(COVERAGE_FILE) | grep total | awk '{print $$3}' | tr -d '%'); \
	echo "  coverage: $${COVERAGE}%"; \
	if [ "$$(echo "$$COVERAGE < $(COVERAGE_MIN)" | bc -l)" -eq 1 ]; then \
		echo "  FAIL: below $(COVERAGE_MIN)%"; \
		exit 1; \
	fi
	@echo "  ✓ coverage"
	@echo
	@echo "=== ALL 7 STEPS GREEN — safe to push ==="

ci-fast:  ## faster iteration: skip golangci-lint + race detector + coverage threshold
	@$(MAKE) --no-print-directory fmt-check
	@go vet ./...
	@go build ./...
	@go test ./...
	@echo "ci-fast: OK (run 'make ci' for the full GitHub-CI-equivalent check)"

# --------------------------------------------------------------------
# Git hooks
# --------------------------------------------------------------------

install-hooks:  ## install the pre-push hook (runs `make ci` before every push)
	@if [ ! -d .git ]; then \
		echo "FAIL: not a git repo (or run from repo root)"; \
		exit 1; \
	fi
	@cp scripts/git-hooks/pre-push .git/hooks/pre-push
	@chmod +x .git/hooks/pre-push
	@echo "Installed .git/hooks/pre-push"
	@echo "Bypass with 'git push --no-verify' when the hook is in your way."

# --------------------------------------------------------------------
# Misc
# --------------------------------------------------------------------

clean:  ## remove build artifacts
	rm -f retrievr-mcp retrievr-cli $(COVERAGE_FILE)
