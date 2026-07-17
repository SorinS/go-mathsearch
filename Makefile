# go-mathsearch Makefile
# A formula search engine over the corpus: exact (canonical signature) + fuzzy
# (structural features), backed by SQLite, rendered with KaTeX.

# ------------------------------------------------------------------------------
# Configuration
# ------------------------------------------------------------------------------

BINARY_NAME  := mathsearch
MAIN_PACKAGE := ./cmd/mathsearch
BUILD_DIR    := bin

# Reproducible, stripped binaries.
LDFLAGS      := -s -w
GOFLAGS      := -trimpath

# Defaults for the convenience run targets (override on the command line).
DB      ?= mathsearch.db
CORPUS  ?= $(HOME)/Dev/Mathematica/MathRepo.git
ADDR    ?= :8080

# ------------------------------------------------------------------------------
# Derived values
# ------------------------------------------------------------------------------

NATIVE_OS   := $(shell uname -s | tr A-Z a-z)
NATIVE_ARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
GIT_COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS += -X main.gitCommit=$(GIT_COMMIT) -X main.buildTime=$(BUILD_TIME)

# ------------------------------------------------------------------------------
# Helpers
# ------------------------------------------------------------------------------

# Print a message in bold.
define echo
	@echo "\033[1m>> $(1)\033[0m"
endef

# ------------------------------------------------------------------------------
# Default target
# ------------------------------------------------------------------------------

.PHONY: help
help: ## Show this help message
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: all
all: fmt vet test ## Format, vet, and test (default)

.DEFAULT_GOAL := all

# ------------------------------------------------------------------------------
# Build
# ------------------------------------------------------------------------------

.PHONY: build
build: ## Build the native binary into bin/
	$(call echo,"Building $(BINARY_NAME) for $(NATIVE_OS)-$(NATIVE_ARCH)")
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PACKAGE)
	@echo "  -> $(BUILD_DIR)/$(BINARY_NAME)"

.PHONY: install
install: ## Install the binary into $(GOBIN) or GOPATH/bin
	$(call echo,"Installing $(BINARY_NAME)")
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(MAIN_PACKAGE)

# ------------------------------------------------------------------------------
# Test
# ------------------------------------------------------------------------------

.PHONY: test
test: ## Run the test suite
	$(call echo,"Running tests")
	go test -count=1 ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	$(call echo,"Running tests with race detector")
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests and print per-function coverage
	$(call echo,"Measuring coverage")
	@mkdir -p $(BUILD_DIR)
	go test -count=1 -coverprofile=$(BUILD_DIR)/coverage.out ./...
	go tool cover -func=$(BUILD_DIR)/coverage.out | tail -20

.PHONY: bench
bench: ## Run benchmarks
	$(call echo,"Running benchmarks")
	go test -run '^$$' -bench=. -benchmem ./...

# ------------------------------------------------------------------------------
# Linting & formatting
# ------------------------------------------------------------------------------

.PHONY: vet
vet: ## Run go vet
	$(call echo,"Running go vet")
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go source files
	$(call echo,"Formatting code")
	@gofmt -w -s .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean
	$(call echo,"Checking formatting")
	@out="$$(gofmt -l -s .)"; \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi
	@echo "  All files are formatted."

.PHONY: lint
lint: fmt-check vet ## Run golangci-lint if installed (plus fmt-check + vet)
	$(call echo,"Running linters")
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "  -> golangci-lint"; golangci-lint run ./...; \
	else \
		echo "  -> golangci-lint not installed (skip)"; \
	fi

# ------------------------------------------------------------------------------
# Modules
# ------------------------------------------------------------------------------

.PHONY: tidy
tidy: ## Tidy module dependencies
	$(call echo,"Tidying modules")
	go mod tidy

# ------------------------------------------------------------------------------
# Application convenience targets
# ------------------------------------------------------------------------------

.PHONY: ingest
ingest: build ## Ingest a corpus into the DB (CORPUS=dir DB=file)
	$(call echo,"Ingesting $(CORPUS) -> $(DB)")
	$(BUILD_DIR)/$(BINARY_NAME) ingest -db $(DB) $(CORPUS)

.PHONY: serve
serve: build ## Run the search web server (DB=file ADDR=:8080)
	$(call echo,"Serving $(DB) on $(ADDR)")
	$(BUILD_DIR)/$(BINARY_NAME) serve -db $(DB) -addr $(ADDR)

# ------------------------------------------------------------------------------
# CI & clean
# ------------------------------------------------------------------------------

.PHONY: ci
ci: fmt-check vet test-race ## Checks suitable for CI

.PHONY: clean
clean: ## Remove build and coverage artifacts
	$(call echo,"Cleaning artifacts")
	rm -rf $(BUILD_DIR)
	go clean
