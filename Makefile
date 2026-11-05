SHELL          := /bin/bash
BINARY         := ccresu
MODULE         := github.com/jymchng/ccresu
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT         ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS        := -s -w \
                  -X main.version=$(VERSION) \
                  -X main.commit=$(COMMIT) \
                  -X main.buildDate=$(BUILD_DATE)
COVERAGE_FILE  := coverage.out
COVERAGE_MIN   := 80.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## Print available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build ./ccresu with version metadata
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/ccresu

.PHONY: install
install: ## Install the binary into $(go env GOPATH)/bin
	CGO_ENABLED=0 go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/ccresu

.PHONY: run
run: ## Run the CLI (ARGS="status")
	go run ./cmd/ccresu $(ARGS)

.PHONY: fmt
fmt: ## Format Go sources
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-clean (CI-safe, non-mutating)
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt found unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (v2)
	golangci-lint run

.PHONY: test
test: ## Run the unit and integration tests
	go test ./...

.PHONY: e2e
e2e: ## Prove stop-then-resume and reset against a stub claude CLI
	tests/scripts/background-resume-e2e.sh

.PHONY: web-captures
web-captures: ## Regenerate the site's real command-output figures from the binary
	scripts/web-captures.sh

.PHONY: web-captures-check
web-captures-check: ## Fail when the committed figures no longer match the binary
	scripts/web-captures.sh --check

.PHONY: race
race: ## Run tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Write $(COVERAGE_FILE) and print per-function coverage
	go test -covermode=atomic -coverprofile=$(COVERAGE_FILE) ./...
	go tool cover -func=$(COVERAGE_FILE) | tail -n 1

.PHONY: cover-check
cover-check: cover ## Fail when total coverage is below $(COVERAGE_MIN)%
	@total="$$(go tool cover -func=$(COVERAGE_FILE) | awk '/^total:/ {gsub(/%/,"",$$3); print $$3}')"; \
	echo "total coverage: $$total% (minimum: $(COVERAGE_MIN)%)"; \
	awk -v t="$$total" -v m="$(COVERAGE_MIN)" 'BEGIN { exit (t+0 >= m+0) ? 0 : 1 }' \
		|| { echo "coverage gate failed"; exit 1; }

.PHONY: web-install
web-install: ## Install website dependencies from the lockfile
	cd web && npm ci

.PHONY: web-lint
web-lint: ## Lint the website
	cd web && npm run lint

.PHONY: web-typecheck
web-typecheck: ## Type-check the website
	cd web && npx tsc --noEmit

.PHONY: web-test
web-test: ## Run website tests
	cd web && npm run test

.PHONY: web-build
web-build: ## Production-build the website
	cd web && npm run build

.PHONY: ci
ci: fmt-check vet test race cover-check ## Run the full Go quality gate

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(BINARY) bin dist $(COVERAGE_FILE) coverage.html
