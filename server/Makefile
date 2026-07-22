# =============================================================================
# iCloud Backup Server — Makefile
# =============================================================================
# Targets:
#   help          Show this help.
#   build         Build the server binary (statically linked, CGO disabled).
#   test          Run all unit/integration tests (excludes e2e).
#   clean         Remove build artifacts.
#   e2e-up        Start the e2e Docker Compose stack (detached).
#   e2e-down      Stop the e2e stack and remove volumes/networks.
#   e2e-logs      Tail logs from the e2e stack.
#   e2e-ps        Show container status for the e2e stack.
#   e2e-test      Run e2e tests against an already-running stack.
#   e2e           Full e2e runner: up → wait → test → down (with diagnostics).
# =============================================================================

# ---------------------------------------------------------------------------
# Configuration — override via environment or `make VAR=value`
# ---------------------------------------------------------------------------

# Docker Compose project name to isolate e2e resources from production.
COMPOSE_PROJECT_NAME ?= files-nest-e2e

# Host port for the e2e Caddy reverse proxy (binds to 127.0.0.1 only).
E2E_HTTP_PORT ?= 18080

# Domain passed to Caddy (always "localhost" for e2e plain-HTTP).
DOMAIN ?= localhost

# Test credentials — override with your own values.
BACKUP_USER ?= testuser
BACKUP_PASS ?= testpass

# E2E readiness polling: max seconds to wait for /health before failing.
E2E_WAIT ?= 30

# Go test timeout (passed as -timeout to go test).
E2E_TIMEOUT ?= 120

# Derived server URL for e2e tests (exported when running tests).
SERVER_URL = http://127.0.0.1:$(E2E_HTTP_PORT)

# Docker Compose file for the e2e stack.
COMPOSE_FILE = docker-compose.e2e.yml

# Compose command prefix.
COMPOSE_CMD = docker compose -p "$(COMPOSE_PROJECT_NAME)" -f $(COMPOSE_FILE)

# Go binary output path.
BIN_PATH = bin/server

# ---------------------------------------------------------------------------
# Phony targets
# ---------------------------------------------------------------------------

.PHONY: help build test clean
.PHONY: e2e-up e2e-down e2e-logs e2e-ps e2e-test e2e

# ===========================================================================
# Development targets
# ===========================================================================

## Show this help.
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Development targets:"
	@echo "  build         Build the server binary"
	@echo "  test          Run all unit/integration tests"
	@echo "  clean         Remove build artifacts"
	@echo ""
	@echo "E2E test targets:"
	@echo "  e2e-up        Start the e2e Docker Compose stack (detached)"
	@echo "  e2e-down      Stop the e2e stack and remove volumes"
	@echo "  e2e-logs      Tail logs from the e2e stack"
	@echo "  e2e-ps        Show container status for the e2e stack"
	@echo "  e2e-test      Run e2e tests against an already-running stack"
	@echo "  e2e           Full e2e: up → wait → test → down (with diagnostics)"
	@echo ""
	@echo "Variables (defaults in parentheses):"
	@echo "  COMPOSE_PROJECT_NAME  ($(COMPOSE_PROJECT_NAME))"
	@echo "  E2E_HTTP_PORT         ($(E2E_HTTP_PORT))"
	@echo "  DOMAIN                ($(DOMAIN))"
	@echo "  BACKUP_USER           ($(BACKUP_USER))"
	@echo "  BACKUP_PASS           (***)"
	@echo "  E2E_WAIT              ($(E2E_WAIT) seconds)"
	@echo "  E2E_TIMEOUT           ($(E2E_TIMEOUT) seconds)"

## Build the server binary (statically linked, CGO disabled).
build:
	CGO_ENABLED=0 go build -ldflags="-s -w -extldflags=-static" -o $(BIN_PATH) .

## Run all unit/integration tests (excludes e2e via build tag).
test:
	go test ./... -v -count=1

## Remove build artifacts.
clean:
	rm -f $(BIN_PATH)

# ===========================================================================
# E2E stack management
# ===========================================================================

## Start the e2e Docker Compose stack (detached).
e2e-up:
	BACKUP_USER=$(BACKUP_USER) BACKUP_PASS=$(BACKUP_PASS) $(COMPOSE_CMD) up -d --build

## Stop the e2e stack and remove volumes, networks, and orphans.
e2e-down:
	$(COMPOSE_CMD) down -v --remove-orphans

## Tail logs from the e2e stack.
e2e-logs:
	$(COMPOSE_CMD) logs -f

## Show container status for the e2e stack.
e2e-ps:
	$(COMPOSE_CMD) ps

# ===========================================================================
# E2E test execution
# ===========================================================================

## Run e2e tests against an already-running stack.
e2e-test:
	@echo "=== Running e2e tests against $(SERVER_URL) ==="
	SERVER_URL=$(SERVER_URL) \
	BACKUP_USER=$(BACKUP_USER) \
	BACKUP_PASS=$(BACKUP_PASS) \
	COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME) \
	go test -tags=e2e ./e2e/... -v -count=1 -timeout=$(E2E_TIMEOUT)s

# ===========================================================================
# Full e2e runner: up → wait → test → down (with diagnostics on failure)
# ===========================================================================

## Full e2e runner: build stack, wait for readiness, run tests, always clean up.
## Prints diagnostics on failure before cleanup. Preserves the original exit code.
e2e:
	@set -eu; \
	cleanup() { \
		echo ""; \
		echo "=== Cleaning up e2e stack ==="; \
		$(COMPOSE_CMD) down -v --remove-orphans 2>/dev/null || true; \
	}; \
	trap 'cleanup' EXIT; \
	\
	echo "=== Building and starting e2e stack ==="; \
	BACKUP_USER=$(BACKUP_USER) BACKUP_PASS=$(BACKUP_PASS) $(COMPOSE_CMD) up -d --build 2>&1; \
	\
	echo ""; \
	echo "=== Waiting for server readiness (E2E_WAIT=$(E2E_WAIT)s) ==="; \
	_waited=0; \
	_ready=false; \
	while [ $$_waited -lt $(E2E_WAIT) ]; do \
		if curl -s -f -o /dev/null "http://127.0.0.1:$(E2E_HTTP_PORT)/health" 2>/dev/null; then \
			_ready=true; \
			break; \
		fi; \
		sleep 1; \
		_waited=$$((_waited + 1)); \
		printf "."; \
	done; \
	echo ""; \
	if [ "$$_ready" = false ]; then \
		echo "ERROR: Server not ready after $(E2E_WAIT) seconds."; \
		echo ""; \
		echo "=== Container status ==="; \
		$(COMPOSE_CMD) ps 2>/dev/null || true; \
		echo ""; \
		echo "=== Server logs ==="; \
		$(COMPOSE_CMD) logs --tail=50 server 2>/dev/null || true; \
		echo ""; \
		echo "=== Caddy logs ==="; \
		$(COMPOSE_CMD) logs --tail=20 caddy 2>/dev/null || true; \
		exit 1; \
	fi; \
	echo "Server is ready."; \
	\
	echo ""; \
	echo "=== Running e2e tests ==="; \
	_exit_code=0; \
	SERVER_URL=http://127.0.0.1:$(E2E_HTTP_PORT) \
	BACKUP_USER=$(BACKUP_USER) \
	BACKUP_PASS=$(BACKUP_PASS) \
	COMPOSE_PROJECT_NAME=$(COMPOSE_PROJECT_NAME) \
	go test -tags=e2e ./e2e/... -v -count=1 -timeout=$(E2E_TIMEOUT)s || _exit_code=$$?; \
	\
	if [ "$$_exit_code" -ne 0 ]; then \
		echo ""; \
		echo "=== TESTS FAILED (exit code $$_exit_code) ==="; \
		echo ""; \
		echo "=== Container status ==="; \
		$(COMPOSE_CMD) ps 2>/dev/null || true; \
		echo ""; \
		echo "=== Server logs ==="; \
		$(COMPOSE_CMD) logs --tail=50 server 2>/dev/null || true; \
		echo ""; \
		echo "=== Caddy logs ==="; \
		$(COMPOSE_CMD) logs --tail=20 caddy 2>/dev/null || true; \
	else \
		echo ""; \
		echo "=== All e2e tests passed ==="; \
	fi; \
	\
	exit "$$_exit_code"
