# Plan: Go E2E Tests for Server

## Overview
Add black-box Go e2e tests for the server under `server/e2e/`, gated by the `e2e` build tag so normal test runs remain unaffected. The suite will use a thin stdlib `net/http` client plus `testify/require`, run against an isolated Docker Compose/Caddy stack, and verify real HTTP behavior without importing `internal/` packages.

## Success Criteria
- [ ] `cd server && go test ./...` passes and does not compile e2e files.
- [ ] `cd server && go test -tags=e2e ./e2e/...` without `SERVER_URL` exits successfully with a clear skip banner.
- [ ] `cd server && SERVER_URL=http://127.0.0.1:18080 go test -tags=e2e ./e2e/...` without credentials exits non-zero with a clear configuration error.
- [ ] `cd server && make e2e` builds an isolated stack, waits for readiness, runs all e2e tests, prints diagnostics on failure, and always cleans up containers/volumes.
- [ ] E2E tests verify health, auth rejection, upload creation, deterministic IDs, TUS offsets, completion, listing envelope behavior, idempotent create, delete/re-register, validation failures, resume behavior, and cursor pagination.

## Context
- VERIFIED (from `server/go.mod`): module is `github.com/moontechs/files-nest/server`, with `go 1.26.2`.
- VERIFIED (from `server/Caddyfile`): Caddy proxies `/uploads*` and `/health` to `server:8080` without stripping paths.
- VERIFIED (from `server/docker-compose.yml`): production Compose binds host ports `80` and `443`; e2e should use a separate Compose file to avoid conflicts.
- VERIFIED (from `server/internal/api/router.go`): routes include `GET /health`, `POST/GET /uploads`, `GET /uploads/{id}`, `HEAD/PATCH /uploads/{id}/data`, `PATCH /uploads/{id}/status`, and `DELETE /uploads/{id}`.
- VERIFIED (from `server/internal/api/auth.go`): `/health` is unauthenticated; upload routes require Basic Auth when both credentials are configured.
- VERIFIED (from `server/internal/api/handlers.go`): `GET /uploads` returns `{ "items": [...], "next_cursor": "..." }`, not a bare array.
- VERIFIED (from `server/internal/api/handlers.go`): duplicate active/completed uploads return `200`; deleted/backend-lost uploads re-register with `201`.
- VERIFIED (from `server/internal/api/handlers.go`): `DELETE /uploads/{id}` soft-deletes; subsequent `GET /uploads/{id}` returns `200` with status `deleted`.
- VERIFIED (from `server/internal/api/handlers.go`): completion requires the tusd upload to have known final length and `offset == length`; the final data `PATCH` must send `Upload-Length`.
- VERIFIED (from `server/internal/api/ids.go` and README): upload IDs are `base64.RawURLEncoding(SHA-256(local_identifier))`, length 43.

## Design Decisions
- Use dedicated `server/docker-compose.e2e.yml` and `server/Caddyfile.e2e` to avoid production port/certificate assumptions.
- Bind Caddy to `127.0.0.1:${E2E_HTTP_PORT:-18080}:80`, not host `80` or `443`.
- Keep Go tests client-only: no Docker calls, no shelling out, no imports from `server/internal/...`.
- Treat empty `SERVER_URL` as skip; treat configured but unreachable `SERVER_URL` as failure.
- Rely on `TestMain` readiness polling for the final readiness gate; Compose healthchecks are diagnostic/supporting only.
- Make list/pagination tests resilient by using unique future-dated fixture windows and asserting only records created by the current run.
- Do not add destructive restart/backend-loss e2e in this pass; it needs a separate orchestrator that removes only tusd backend data while preserving DB state.

## Invariants
- Every file in `server/e2e/` starts with `//go:build e2e`.
- E2E tests must not import `github.com/moontechs/files-nest/server/internal/...`.
- E2E tests must close all response bodies.
- Authenticated requests use only `BACKUP_USER` and `BACKUP_PASS` from the environment.
- Listing assertions must be scoped to the current test run’s unique local identifier prefix and date window.
- Final upload chunks used before completion must include `Upload-Length: <total_size>` and `Upload-Complete: 1` where applicable.
- Idempotent create expectations must match the API: existing active/completed records return `200`, re-register after deleted/backend_lost returns `201`.

## API Surface
- `GET /health` → `200 {"status":"ok"}`, no auth.
- `POST /uploads` → `201` for new uploads, `200` for existing active/completed uploads, `201` for deleted/backend_lost re-registration.
- `GET /uploads` → JSON envelope with `items` and `next_cursor`.
- `GET /uploads/{id}` → `200` for existing records including deleted records; `404` only for unknown IDs.
- `HEAD /uploads/{id}/data` → `200`, `Upload-Offset`, `Tus-Resumable` for uploading records.
- `PATCH /uploads/{id}/data` → `204`, updated `Upload-Offset`; invalid headers return `400`; offset mismatch returns `409`.
- `PATCH /uploads/{id}/status` with `{"status":"complete"}` → `204` only after upload is fully written.
- `DELETE /uploads/{id}` → `204`; record status becomes `deleted`.

### Task 1: Add isolated e2e Compose stack
**Files:**
- Create: `server/docker-compose.e2e.yml`
- Create: `server/Caddyfile.e2e`

- [x] Define `server` and `caddy` services using the existing `Dockerfile`, e2e-only named volumes, and no dependency on the production `docker-compose.yml`.
- [x] Bind Caddy to `127.0.0.1:${E2E_HTTP_PORT:-18080}:80` only.
- [x] Use an e2e Caddyfile with a plain-HTTP `:80` site block that proxies `/health` and `/uploads*` to `server:8080` without path rewriting.
- [x] Add service healthchecks: `server` checks `http://localhost:8080/health`; `caddy` checks `http://localhost/health`.
- [x] Propagate `BACKUP_USER`, `BACKUP_PASS`, `STORAGE_PATH=/data`, `PORT=8080`, and `DOMAIN=localhost`.

### Task 2: Add robust Makefile runner
**Files:**
- Create: `server/Makefile`

- [x] Add `.PHONY: e2e e2e-down e2e-logs` and set safe defaults for `COMPOSE_PROJECT_NAME`, `E2E_HTTP_PORT`, `DOMAIN`, `BACKUP_USER`, and `BACKUP_PASS`.
- [x] Implement `make e2e` as a single shell script using `set -eu`, preserving the test exit status through cleanup.
- [x] Run `docker compose -p "$$COMPOSE_PROJECT_NAME" -f docker-compose.e2e.yml up -d --build` before tests; do not rely on `docker compose --wait` as the only readiness mechanism.
- [x] Export `SERVER_URL=http://127.0.0.1:${E2E_HTTP_PORT}`, `BACKUP_USER`, and `BACKUP_PASS` into `go test -tags=e2e ./e2e/...`.
- [x] Always run `docker compose ... down -v --remove-orphans` via trap/cleanup, including when build, startup, or tests fail.
- [x] On failure, print `docker compose ps` and service logs before cleanup while returning the original failure code.

### Task 3: Add e2e dependency and TestMain
**Files:**
- Modify: `server/go.mod`
- Modify: `server/go.sum`
- Create: `server/e2e/main_test.go`

- [x] Add `github.com/stretchr/testify` as a direct test dependency if not already direct.
- [x] Read `SERVER_URL`, `BACKUP_USER`, `BACKUP_PASS`, `E2E_WAIT`, and `E2E_TIMEOUT` once at startup.
- [x] If `SERVER_URL` is empty, print a skip banner to stderr and exit `0`.
- [x] If `SERVER_URL` is set but either credential is missing, print a configuration error and exit non-zero.
- [x] Poll unauthenticated `GET /health` until it returns `200 {"status":"ok"}` or `E2E_WAIT` expires; timeout is a failure when `SERVER_URL` is configured.

### Task 4: Implement thin HTTP e2e client
**Files:**
- Create: `server/e2e/client_test.go`

- [x] Define a `Client` with `baseURL`, `username`, `password`, and `*http.Client`.
- [x] Implement authenticated `do` and unauthenticated `doNoAuth` helpers that set `Tus-Resumable: 1.0.0`.
- [x] Add helpers for JSON `GET`, `POST`, `PATCH`, `DELETE`, `HEAD` offset reads, and TUS data `PATCH`.
- [x] Support final data patches with optional `Upload-Length` and `Upload-Complete`.
- [x] Add a list helper that supports `from`, `to`, `status`, `limit`, and `cursor`, and decodes the `items`/`next_cursor` envelope.
- [x] Include status, headers, and short body snippets in assertion failure messages.

### Task 5: Add isolated e2e fixtures
**Files:**
- Create: `server/e2e/fixtures_test.go`

- [x] Add test-local `safeID(localIdentifier string)` using SHA-256 plus raw URL-safe base64.
- [x] Generate a unique per-run prefix using timestamp plus random bytes.
- [x] Generate list/pagination fixture creation dates in a unique future-dated RFC3339Nano window to avoid collisions with real data.
- [x] Add fixture builders for lifecycle, auth, validation, delete/re-register, listing, pagination, and resume tests.
- [x] Keep all request/response structs local to `server/e2e`.

### Task 6: Add core upload lifecycle test
**Files:**
- Create: `server/e2e/lifecycle_test.go`

- [x] Add `TestE2E_Lifecycle` with ordered subtests and closure-scoped upload state.
- [x] Verify unauthenticated `GET /health` returns `200` and status `ok`.
- [x] Verify authenticated `POST /uploads` returns `201`, status `uploading`, expected deterministic ID, and upload URL `/uploads/{id}/data`.
- [x] Verify `HEAD /uploads/{id}/data` returns offset `0`, then final `PATCH` 1 KiB with `Upload-Length: 1024` returns offset `1024`.
- [x] Verify `PATCH /uploads/{id}/status` completes the upload, then `GET /uploads/{id}` returns status `complete` and non-empty `organized_path`.
- [x] Verify re-POSTing the completed fixture returns `200` with the same ID and status `complete`.

### Task 7: Add auth and validation tests
**Files:**
- Create: `server/e2e/auth_validation_test.go`

- [x] Verify unauthenticated `POST /uploads` returns `401` with a Basic `WWW-Authenticate` challenge.
- [x] Verify wrong Basic Auth credentials return `401`.
- [x] Verify create validation failures return `400` for missing `local_identifier`, invalid `creation_date`, unsafe filename, and unknown JSON field.
- [x] Verify `PATCH /uploads/{id}/data` without `Content-Type: application/offset+octet-stream` returns `400`.
- [x] Verify `PATCH /uploads/{id}/data` without `Upload-Offset` returns `400`.

### Task 8: Add delete and re-register tests
**Files:**
- Create: `server/e2e/reregister_test.go`

- [x] Create a fresh upload fixture independent of lifecycle tests.
- [x] Verify `DELETE /uploads/{id}` returns `204`.
- [x] Verify `GET /uploads/{id}` after delete returns `200` with status `deleted`.
- [x] Verify re-POSTing the same `local_identifier` returns `201`, same deterministic ID, and status `uploading`.
- [x] Verify the re-registered upload supports `HEAD /uploads/{id}/data` with offset `0`.

### Task 9: Add resume and offset conflict tests
**Files:**
- Create: `server/e2e/resume_test.go`

- [x] Create a fresh upload and send an initial chunk without `Upload-Length`.
- [x] Verify a wrong `Upload-Offset` returns `409`.
- [x] Verify a follow-up `HEAD` reports the unchanged server offset.
- [x] Create a new client instance, discover offset with `HEAD`, send the final chunk with `Upload-Length` and `Upload-Complete: 1`, and assert returned offset equals total size.
- [x] Complete the resumed upload and verify status `complete`.

### Task 10: Add scoped listing and pagination tests
**Files:**
- Create: `server/e2e/listing_test.go`

- [ ] Create a dedicated set of uploads with unique local identifiers and creation dates inside a unique future-dated window.
- [ ] Verify `GET /uploads?status=uploading&from=<window>&to=<window>` includes the expected current-run uploading fixtures after filtering by local identifier prefix.
- [ ] Complete one fixture and verify `status=complete` includes it while `status=uploading` excludes it.
- [ ] Verify date-range filtering using only the exact current-run fixture window.
- [ ] Verify `limit=2` pagination by following cursors until empty, filtering to current-run fixture IDs, and asserting no duplicates.
- [ ] Assert the paginated current-run union equals the expected fixture IDs without relying on global database emptiness.
- [ ] Verify invalid cursor and invalid date range return `400`.

### Task 11: Document e2e usage
**Files:**
- Modify: `server/README.md`

- [ ] Add an “E2E Tests” section documenting `make e2e`.
- [ ] Document configurable variables: `E2E_HTTP_PORT`, `COMPOSE_PROJECT_NAME`, `DOMAIN`, `BACKUP_USER`, `BACKUP_PASS`, `E2E_WAIT`, and `E2E_TIMEOUT`.
- [ ] Document direct usage against an already-running stack with `SERVER_URL`.
- [ ] Document that normal `go test ./...` excludes e2e tests because of the `e2e` build tag.
- [ ] Document skip/fail behavior for unset `SERVER_URL`, missing credentials, and unreachable configured stacks.

### Task 12: Verify skipped, failure, and full flows
**Files:**
- No file changes

- [ ] Run `cd server && go test ./...`.
- [ ] Run `cd server && go test -tags=e2e ./e2e/...` without `SERVER_URL` and confirm clean skip behavior.
- [ ] Run `cd server && SERVER_URL=http://127.0.0.1:18080 go test -tags=e2e ./e2e/...` without credentials and confirm non-zero failure.
- [ ] Run `cd server && make e2e` and confirm all e2e suites pass.
- [ ] Run `cd server && E2E_HTTP_PORT=18081 COMPOSE_PROJECT_NAME=files-nest-e2e-alt make e2e` to confirm port and project isolation.
