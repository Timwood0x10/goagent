# Makefile for ARES — Agent Runtime & Evolution System

.PHONY: all lint test test-race check check-core check-tools help clean install install-cli ci ci-freeze benchmark quickstart examples cover cover-html ci-test-race-short

# Default target
all: lint test

# Install dependencies
install:
	go mod download
	go get ./...

# CI target - runs all CI checks locally (matches .github/workflows/ci.yml)
ci: ci-deps ci-fmt ci-freeze ci-vet ci-lint ci-build ci-test-race ci-security
	@echo ""
	@echo "✅ All CI checks PASSED"

# CI dependency checks
ci-deps:
	@echo "Checking dependencies..."
	@go mod verify
	@echo "Dependencies: OK"

# CI format check
ci-fmt:
	@echo "Checking code formatting..."
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "ERROR: Code not formatted. Run 'make fmt'"; \
		gofmt -l .; \
		exit 1; \
	fi
	@echo "Formatting: OK"

# CI vet check
ci-vet:
	@echo "Running go vet..."
	@go vet ./...
	@echo "Vet: OK"

# CI linter
ci-lint:
	@echo "Running golangci-lint..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=10m; \
		echo "Linting: OK"; \
	else \
		echo "ERROR: golangci-lint not installed. Install with: brew install golangci-lint"; \
		exit 1; \
	fi

# CI build check
ci-build:
	@echo "Building all packages..."
	@go build -v ./...
	@echo "Build: OK"

# CI tests with race detection (FULL suite, -count=1 bypasses test cache)
ci-test-race:
	@echo "Running full test suite with race detection..."
	@go test -race -count=1 ./...
	@echo "Tests: OK"

# CI tests with race detection (short/fast path for quick local checks)
ci-test-race-short:
	@echo "Running short tests with race detection..."
	@go test -race -short -count=1 ./...
	@echo "Tests: OK"

# CI security scan
ci-security:
	@echo "Running gosec security scan..."
	@go run github.com/securego/gosec/v2/cmd/gosec@latest ./internal/... ./api/...
	@echo "Security scan: OK"

# Convergence freeze patrol (ARCHITECTURE.md Phase 0). Fails on new
# examples/ dirs, new internal/ packages, or production imports of the
# internal/fabric placeholder before Phase 2b.
ci-freeze:
	@echo "Checking convergence freeze..."
	@bash scripts/check_convergence_freeze.sh
	@echo "Freeze check: OK"

# Format code
# Uses golangci-lint --fix to auto-format with the same gci version it checks
# with, plus gofmt -s for final cleanup. gci replaces goimports which caused
# 800%+ CPU via modindex re-reads (see docs/bug@ques/zh/goimports-modindex-cpu-spike.md).
fmt:
	golangci-lint run --fix --timeout=5m
	gofmt -s -w .

# Lint targets
lint: lint-vet lint-staticcheck lint-golangci
	@echo ""
	@echo "All lint checks: PASSED"

lint-vet:
	@echo "Running go vet..."
	@go vet ./...
	@echo "go vet: PASSED"

lint-staticcheck:
	@echo "Running staticcheck..."
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./... && \
		echo "staticcheck: PASSED"; \
	else \
		echo "WARNING: staticcheck not installed. Install with: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	fi

lint-golangci:
	@echo "Running golangci-lint..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m && \
		echo "golangci-lint: PASSED"; \
	else \
		echo "ERROR: golangci-lint not installed. Install with: brew install golangci-lint"; \
		exit 1; \
	fi

# Test targets
test:
	go test -short -cover ./...

test-race:
	go test -race -cover ./...

# Coverage — full race-enabled coverage report (prints total coverage line)
cover:
	@echo "Running tests with race detection and coverage..."
	@go test -race -count=1 -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1

# Coverage — open HTML coverage report in browser (run `make cover` first)
cover-html:
	@echo "Opening HTML coverage report..."
	@go tool cover -html=cover.out

# Core modules — check total coverage across all core packages
test-core:
	@echo "Running core module tests with coverage..."
	@go test -cover -coverprofile=coverage.out ./internal/core/...
	@echo ""
	@echo "--- Per-function coverage ---"
	@go tool cover -func=coverage.out
	@echo ""
	@TOTAL=$$(go tool cover -func=coverage.out | grep "^total:" | awk '{print $$NF}' | sed 's/%//'); \
	if [ "$$TOTAL" = "" ]; then \
		echo "ERROR: could not determine total coverage"; \
		exit 1; \
	fi; \
	THRESHOLD=90; \
	if [ "$$(echo "$$TOTAL < $$THRESHOLD" | bc 2>/dev/null || echo 1)" = "1" ]; then \
		echo "ERROR: Core module total coverage is $$TOTAL%, expected >= $$THRESHOLD%"; \
		exit 1; \
	fi; \
	echo "✅ Core module total coverage: $$TOTAL% (threshold: $$THRESHOLD%)"

# Other modules — check total coverage across tools packages
test-tools:
	@echo "Running tools module tests with coverage..."
	@go test -cover -coverprofile=coverage.out ./internal/llm/... ./internal/workflow/... ./internal/ares_memory/... ./internal/ares_shutdown/... ./internal/ares_ratelimit/... ./internal/tools/... ./internal/storage/... ./internal/agents/...
	@echo ""
	@echo "--- Per-package coverage ---"
	@go tool cover -func=coverage.out | grep "total:" || true
	@echo ""
	@TOTAL=$$(go tool cover -func=coverage.out | grep "^total:" | awk '{print $$NF}' | sed 's/%//'); \
	if [ "$$TOTAL" = "" ]; then \
		echo "ERROR: could not determine total coverage"; \
		exit 1; \
	fi; \
	THRESHOLD=80; \
	if [ "$$(echo "$$TOTAL < $$THRESHOLD" | bc 2>/dev/null || echo 1)" = "1" ]; then \
		echo "ERROR: Tools module total coverage is $$TOTAL%, expected >= $$THRESHOLD%"; \
		exit 1; \
	fi; \
	echo "✅ Tools module total coverage: $$TOTAL% (threshold: $$THRESHOLD%)"

# All checks
check: lint test

# G1-G3 repair-plan gates: reachability, config contract, event contract.
# G4 (§8 closure): the design doc's acceptance assertions only exist under
# `-tags closure` — without this line `make check` green ≠ §8 verified.
gate:
	@./scripts/g1_reachability_gate.sh
	@go test -run TestG2ConfigContract ./internal/ares_config/...
	@go test -run TestEventContract ./internal/ares_events/...
	@go test -race -tags closure ./internal/runtime/ares_evolution/... ./internal/runtime/evolution/... ./internal/ares_bootstrap/...

# G4: nightly race + soak baseline (cron target).
nightly: test-race
	@echo "nightly: race suite passed; 12h soak is a separate scheduled job"

# Combined check with coverage
check-all: lint test-race test-core test-tools

# Quick check (lint + basic test)
check-quick: lint test

# Build targets
# LDFLAGS strips the symbol table and DWARF debug info; -trimpath makes builds
# reproducible and removes local paths (measured: bin/ares 56M -> 37M).
LDFLAGS := -s -w

# VERSION file is the single source of the release version; git tag overrides
# it when present (e.g. v0.3.0). The value is injected into main.version so
# `ares version` reports the build's actual version (P4).
VERSION_FILE := $(if $(wildcard VERSION),$(shell cat VERSION),dev)
VERSION := $(if $(TAG_VERSION),$(TAG_VERSION),$(VERSION_FILE))
VERSION_LDFLAGS := -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" -o bin/ares ./cmd/ares

build-all:
	go build -trimpath -ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" -o bin/ ./cmd/...

# Install CLI
install-cli:  ## Install ares CLI to $GOPATH/bin
	go install -trimpath -ldflags "$(LDFLAGS) $(VERSION_LDFLAGS)" ./cmd/ares/...

# Clean targets
clean:
	rm -rf bin/
	rm -f coverage.out

# Benchmark targets
benchmark:
	@echo "Running benchmarks..."
	@echo ""
	@echo "=== Evaluation Framework Benchmarks ==="
	@go test -bench=. -benchmem ./internal/eval/...
	@echo ""
	@echo "=== Streaming Handler Benchmarks ==="
	@go test -bench=. -benchmem ./api/handler/...
	@echo ""
	@echo "=== Plugin System Benchmarks ==="
	@go test -bench=. -benchmem ./internal/tools/resources/core/...
	@echo ""
	@echo "=== Agent Benchmarks ==="
	@go test -bench=. -benchmem ./internal/agents/...
	@echo ""
	@echo "✅ All benchmarks completed"

benchmark-quick:
	@echo "Running quick benchmarks (1s each)..."
	@go test -bench=. -benchtime=1s ./internal/eval/... ./api/handler/... ./internal/tools/resources/core/...

benchmark-profile:
	@echo "Running benchmarks with CPU profile..."
	@go test -bench=. -cpuprofile=cpu.prof ./internal/eval/...
	@go tool pprof -top cpu.prof

benchmark-save:
	@echo "Running benchmarks and saving results..."
	@mkdir -p benchmarks
	@echo "# ARES Performance Benchmark Report" > benchmarks/benchmark_report.md
	@echo "" >> benchmarks/benchmark_report.md
	@echo "**Date:** $(date +%Y-%m-%d)" >> benchmarks/benchmark_report.md
	@echo "**Platform:** $(uname -s)/$(uname -m)" >> benchmarks/benchmark_report.md
	@echo "" >> benchmarks/benchmark_report.md
	@echo "---" >> benchmarks/benchmark_report.md
	@echo "" >> benchmarks/benchmark_report.md
	@echo "## Evaluation Framework Benchmarks" >> benchmarks/benchmark_report.md
	@go test -bench=. -benchmem ./internal/eval/... >> benchmarks/benchmark_report.md 2>&1
	@echo "" >> benchmarks/benchmark_report.md
	@echo "## Streaming Handler Benchmarks" >> benchmarks/benchmark_report.md
	@go test -bench=. -benchmem ./api/handler/... >> benchmarks/benchmark_report.md 2>&1
	@echo "" >> benchmarks/benchmark_report.md
	@echo "✅ Benchmark results saved to benchmarks/benchmark_report.md"

# ──────────────────────────────────────────────
# Evaluation — run evaluation scenarios
# ──────────────────────────────────────────────
test-eval:  ## Run evaluation tests
	@echo "📊 Running evaluation tests..."
	@go test -count=1 -timeout=300s ./evaluation/...
	@echo "✅ Evaluation tests complete"

# Demo: MCP + Dashboard
# Usage: make demo-mcp TARGET=/path/to/analyze ADDR=:8090
demo-mcp: TARGET ?= .
demo-mcp: ADDR ?= :8090
demo-mcp:
	@echo "Building MCP dashboard demo..."
	@go build -o /tmp/mcp-dashboard ./examples/mcp-dashboard/
	@echo "Starting in background..."
	@PORT=$$(echo $(ADDR) | sed 's/://'); \
		/tmp/mcp-dashboard -target $(TARGET) -addr $(ADDR) > /tmp/mcp-dashboard.log 2>&1 & \
		PID=$$!; \
		echo "PID: $$PID"; \
		echo "Logs: tail -f /tmp/mcp-dashboard.log"; \
		echo "Dashboard: http://localhost:$$PORT"; \
		echo "Stop: kill $$PID"; \
		sleep 2; \
		open http://localhost:$$PORT 2>/dev/null || true

# ──────────────────────────────────────────────
# Demo: Docker + Integration Tests
# ──────────────────────────────────────────────
# Start all demo services (pgvector + optional embedding)
demo-up:
	@echo "Starting ARES demo services..."
	@docker compose up -d
	@echo "Waiting for PostgreSQL to be ready..."
	@until docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do \
		sleep 1; \
	done
	@echo ""
	@echo "✅ PostgreSQL is ready!"
	@echo "   DSN: postgres://postgres:postgres@localhost:5433/ARES_test?sslmode=disable"
	@echo ""
	@echo "Run tests:       make demo-test"
	@echo "View logs:       make demo-logs"
	@echo "Shutdown:        make demo-down"
	@echo ""

# Stop and clean up demo services
demo-down:
	@echo "Stopping ARES demo services..."
	@docker compose down -v
	@echo "✅ Demo services stopped"

# Run integration tests against demo services
demo-test:
	@echo "Running integration tests against demo services..."
	@TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5433/ARES_test?sslmode=disable" \
		go test -v -count=1 -timeout=180s ./internal/integration/... ./internal/events/... 2>&1 | \
		grep -E "^(=== RUN|--- |ok |FAIL|--- FAIL|PASS|SKIP)"
	@echo ""
	@echo "✅ Integration tests completed"

# Tail logs from demo services
demo-logs:
	@docker compose logs -f

# Quick smoke test — just verify the database connection
demo-smoke:
	@echo "Checking PostgreSQL connection..."
	@docker compose exec postgres psql -U postgres -d ARES_test -c "SELECT '✅ pgvector OK' AS status, extname, extversion FROM pg_extension WHERE extname='vector';"
	@echo ""
	@echo "Checking test databases..."
	@docker compose exec postgres psql -U postgres -c "\l ARES_test"
	@docker compose exec postgres psql -U postgres -c "\l testdb"

# ──────────────────────────────────────────────
# Quickstart — one-command 5-minute demo
# ──────────────────────────────────────────────
quickstart:  ## 5 分钟快速开始
	@echo "🚀 Running quickstart example..."
	@go run examples/01-quickstart/main.go

# ──────────────────────────────────────────────
# Examples — build all examples
# ──────────────────────────────────────────────
examples:  ## Build all examples
	@echo "Building all examples..."
	@for d in examples/*/; do \
		name=$$(basename $$d); \
		echo "  building $$name..."; \
		go build ./examples/$$name/... || exit 1; \
	done
	@echo "✅ All examples built successfully"

# Help
help:
	@echo "Available targets:"
	@echo "  install       - Download and install dependencies"
	@echo "  fmt           - Format code with goimports and gofmt"
	@echo "  lint          - Run all linters (vet, staticcheck, golangci-lint)"
	@echo "  lint-vet      - Run go vet"
	@echo "  lint-staticcheck  - Run staticcheck"
	@echo "  lint-golangci    - Run golangci-lint (REQUIRED)"
	@echo "  test          - Run tests with coverage"
	@echo "  test-race     - Run tests with race detection"
	@echo "  cover         - Run race tests with coverage and print total"
	@echo "  cover-html    - Open HTML coverage report (run 'make cover' first)"
	@echo "  test-core     - Run tests for core modules (requires 90%+ coverage)"
	@echo "  test-tools    - Run tests for tools modules (requires 80%+ coverage)"
	@echo "  benchmark     - Run all benchmarks"
	@echo "  benchmark-quick - Run quick benchmarks (1s each)"
	@echo "  benchmark-profile - Run benchmarks with CPU profiling"
	@echo "  benchmark-save - Run benchmarks and save results to file"
	@echo "  check         - Run lint and test"
	@echo "  check-all     - Run lint, tests with race detection, and coverage checks"
	@echo "  check-quick   - Quick check (lint + basic test)"
	@echo "  build         - Build server binary"
	@echo "  build-all     - Build all binaries"
	@echo "  clean         - Clean build artifacts"
	@echo "  ci            - Run full CI checks locally (deps, fmt, vet, lint, build, test-race)"
	@echo "  help          - Show this help message"
	@echo ""
	@echo "CI sub-targets:"
	@echo "  ci-deps       - Verify module dependencies"
	@echo "  ci-fmt        - Check code formatting"
	@echo "  ci-vet        - Run go vet"
	@echo "  ci-lint       - Run golangci-lint"
	@echo "  ci-build      - Build all packages"
	@echo "  ci-test-race  - Run full test suite with race detection"
	@echo "  ci-test-race-short - Run short tests with race detection"
	@echo ""
	@echo "Required tools:"
	@echo ""
	@echo "Demo targets (require Docker):"
	@echo "  demo-up       - Start demo services (pgvector:5432)"
	@echo "  demo-down     - Stop and clean up demo services"
	@echo "  demo-test     - Run integration tests against demo services"
	@echo "  demo-logs     - Tail demo service logs"
	@echo "  demo-smoke    - Quick check that pgvector is running"
	@echo ""
	@echo "Required tools:"
	@echo "  - go: https://go.dev/dl/"
	@echo "  - goimports: go install golang.org/x/tools/cmd/goimports@latest"
	@echo "  - staticcheck: go install honnef.co/go/tools/cmd/staticcheck@latest"
	@echo "  - golangci-lint: brew install golangci-lint (macOS)"