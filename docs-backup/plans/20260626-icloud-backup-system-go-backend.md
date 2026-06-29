# Plan: iCloud Backup System Go Backend

## Overview
Реализовать Go backend часть iCloud Backup System: HTTP API, BadgerDB state store, TUS/tusd proxy, финализацию файлов, runtime wiring и Docker/Caddy deployment. План следует архитектуре из `docs/architecture.md`, но backend implementation фиксирует проверенные уточнения: tusd — отдельный HTTP-процесс, TUS protocol `1.0.0`, `Upload-Complete: ?1` проксируется без нормализации.

## Context
- VERIFIED (from repo): проект greenfield; корневого `README.md`, `package.json`, `go.mod` и Go-кода сейчас нет.
- VERIFIED (from repo): есть `docs/architecture.md`, `docs/plans/20260626-icloud-backup-system.md`, `docs/plans/20260626-icloud-backup-system-go-backend.md`.
- VERIFIED (from `docs/architecture.md`): `server/` — Go HTTP API + upload state store; Mac app живёт в отдельном приватном repo.
- VERIFIED (from local tool): локальный Go toolchain — `go1.26.2 darwin/arm64`.
- VERIFIED (from user context): использовать `github.com/go-chi/chi/v5@v5.3.0`, `github.com/dgraph-io/badger/v4@v4.9.2`, tusd `v2.10.0`.
- VERIFIED (from user context): использовать chi `middleware.BasicAuth`, не самописный Basic Auth.
- VERIFIED (from user context): tusd v2 использует TUS protocol `1.0.0`; `Upload-Complete` должен проходить как `?1`.

## Success Criteria
- [ ] `cd server && go test ./...` passes.
- [ ] `cd server && go test -race ./...` passes.
- [ ] `cd server && test -z "$(gofmt -l .)"` passes.
- [ ] `git diff --check` passes.
- [ ] `cd server && docker compose up --build` starts Go server, tusd, and Caddy with one shared storage volume.
- [ ] `GET /healthz` is unauthenticated; every `/uploads` route requires HTTP Basic Auth.
- [ ] `POST /uploads` is idempotent for repeated and concurrent calls with the same `localIdentifier`.
- [ ] `HEAD /uploads/:id/data` and `PATCH /uploads/:id/data` proxy TUS traffic to tusd without Go temp files.
- [ ] TUS proxy preserves `Tus-Resumable: 1.0.0` and `Upload-Complete: ?1` exactly.
- [ ] `PATCH /uploads/:id/status {"status":"complete"}` moves `incoming/<backend_id>` into `organized/YYYY/MM/DD/`.
- [ ] Repeated status transitions leave no ghost keys under `idx/status/<old>/<id>`.

## Invariants
- Server is the only source of sync state.
- Go API layer never writes upload chunks to temp files.
- Upload offset is never stored in BadgerDB; tusd `HEAD` is source of truth.
- Status values are only `uploading`, `complete`, `deleted`, `backend_lost`.
- No `pending` state.
- Main upload record and secondary indexes are updated in one BadgerDB transaction.
- `UpdateStatus` always deletes `idx/status/<old>/<id>` before writing `idx/status/<new>/<id>`.
- `POST /uploads` returns an existing record as-is, including `complete`, `deleted`, and `backend_lost`.
- Concurrent `POST /uploads` calls for the same id are serialized in-process.
- If tusd upload creation succeeds but BadgerDB write fails, server attempts tusd termination to avoid orphaned backend files.
- tusd is internal-only; clients talk only to Go server.
- `deleted` and `backend_lost` are logical states; physical pruning is out of scope.
- Server shutdown must stop background goroutines and close BadgerDB.

## API Surface
- `GET /healthz`
- `POST /uploads`
- `GET /uploads`
- `GET /uploads/:id`
- `HEAD /uploads/:id/data`
- `PATCH /uploads/:id/data`
- `PATCH /uploads/:id/status`
- `DELETE /uploads/:id`

Implementation note: `localIdentifier` may contain `/`. Do not rely on ordinary chi `{id}` params for routes that need IDs. Use tested escaped-path suffix parsing for:
- `/uploads/<escaped-id>`
- `/uploads/<escaped-id>/data`
- `/uploads/<escaped-id>/status`

## Key Layout

Storage:

```text
$STORAGE_PATH/
  incoming/
  organized/
    YYYY/
      MM/
        DD/
          IMG_1234.jpg
          IMG_0001_<backend_id>.jpg
  state/
```

BadgerDB keys:

```text
uploads/<localIdentifier>
idx/date/YYYY-MM-DD/<localIdentifier>
idx/status/<status>/<localIdentifier>
```

Cursor format:

```text
base64url(<YYYY-MM-DD>/<localIdentifier>)
```

Cursor parsing must split after the fixed `YYYY-MM-DD/` prefix, not by all slashes.

## Auth / Security
- Use chi `middleware.BasicAuth(realm, map[string]string{user: pass})`.
- Read credentials from `BACKUP_USER` and `BACKUP_PASS`.
- Reject startup if credentials are empty.
- Caddy terminates TLS; production Basic Auth must not be used over cleartext HTTP.
- Do not expose tusd directly through Caddy.

## Testing Strategy
- Store tests use real BadgerDB in `t.TempDir()`.
- Handler tests use `httptest`.
- tusd/backend tests use fake `httptest.Server`.
- File mover tests use temp dirs and explicit collision/failure scenarios.
- Runtime tests cover config validation, graceful shutdown helpers, startup retry, and GC goroutine cancellation.
- Race detector must pass for the full backend test suite.

### Task 1: Go Server Scaffold

**Files:**
- Create: `server/go.mod`
- Create: `server/main.go`
- Create: `server/internal/store/store.go`
- Create: `server/internal/store/store_test.go`

- [ ] Initialize `server/` module as `github.com/moontechs/files-nest/server`.
- [ ] Pin `github.com/go-chi/chi/v5@v5.3.0` and `github.com/dgraph-io/badger/v4@v4.9.2`.
- [ ] Implement `store.Open(path string) (*Store, error)` using `badger.DefaultOptions(path).WithSyncWrites(true)`.
- [ ] Implement `Store.Close() error`; keep `*badger.DB` private.
- [ ] Add tests for open, close, directory creation, and reopen after close.

### Task 2: Upload Records and Index Registry

**Files:**
- Create: `server/internal/store/index.go`
- Create: `server/internal/store/uploads.go`
- Create: `server/internal/store/uploads_test.go`

- [ ] Define `UploadRecord`, `UploadStatus`, `IndexEntry`, `Index`, and `IndexRegistry`.
- [ ] Implement `DateIndex`, `StatusIndex`, `PutUpload`, `GetUpload`, `UpdateStatus`, `DeleteUpload`, and `ListUploadsByDateRange`.
- [ ] Ensure every record/index mutation happens in one BadgerDB transaction.
- [ ] Ensure `UpdateStatus` deletes the old status index key in the same transaction before writing the new key.
- [ ] Add table-driven tests for CRUD, pagination, status filtering, cursor errors, not-found behavior, metadata over 1 MiB, IDs containing `/`, and repeated `UpdateStatus` ghost-key prevention.

### Task 3: Upload Backend Client

**Files:**
- Create: `server/internal/uploadbackend/client.go`
- Create: `server/internal/uploadbackend/client_test.go`

- [ ] Implement a context-aware tusd client with base URL, injected `http.Client`, and request timeout.
- [ ] Implement `CreateUpload`, `GetOffset`, `ForwardPatch`, and `Terminate` against tusd `/files/<backend_id>`.
- [ ] Use TUS protocol `1.0.0`; create uploads with `Upload-Defer-Length: 1`; extract `backend_id` from tusd `Location`.
- [ ] Preserve TUS request/response headers, including `Tus-Resumable`, `Upload-Offset`, `Upload-Defer-Length`, `Upload-Complete: ?1`, `Upload-Metadata`, `Upload-Checksum`, and `Upload-Expires`.
- [ ] Add `httptest.Server` tests for create, HEAD offset, PATCH streaming, header passthrough, response header passthrough, and terminate errors.

### Task 4: API Router, Auth, and Reads

**Files:**
- Create: `server/internal/api/router.go`
- Create: `server/internal/api/handlers.go`
- Create: `server/internal/api/paths.go`
- Create: `server/internal/api/handlers_test.go`
- Create: `server/internal/api/paths_test.go`

- [ ] Wire chi router with unauthenticated `GET /healthz` and chi `middleware.BasicAuth` on `/uploads` routes only.
- [ ] Implement a tested upload ID extractor that supports URL-escaped `localIdentifier` values containing `/`.
- [ ] Implement `POST /uploads` idempotently: existing records return as-is; new records create one tusd upload and one BadgerDB record with status `uploading`.
- [ ] Serialize concurrent create attempts per `localIdentifier`; test that concurrent duplicate POSTs create only one tusd upload.
- [ ] Implement `GET /uploads` with `from`, `to`, `status`, `limit`, and opaque `cursor`; implement `GET /uploads/:id`.

### Task 5: TUS Proxy Handlers

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`

- [ ] Implement `HEAD /uploads/:id/data`: lookup record, proxy to tusd, copy TUS response headers.
- [ ] Implement `PATCH /uploads/:id/data`: stream request body to tusd and copy TUS request/response headers without normalizing `Upload-Complete`.
- [ ] Strip only hop-by-hop HTTP headers; pass through TUS headers exactly.
- [ ] On tusd `404` from HEAD/PATCH, update record to `backend_lost` and return `409 {"error":"backend_lost"}`.
- [ ] Add tests for offset response, PATCH body streaming, full header passthrough, `Upload-Complete: ?1`, backend 404 transition, and unknown id.

### Task 6: File Finalization and Deletion

**Files:**
- Create: `server/internal/filestore/mover.go`
- Create: `server/internal/filestore/mover_test.go`
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`

- [ ] Implement `MoveFile(src, dst, backendID string) (finalPath string, err error)` with `os.Rename` first and copy+delete fallback for cross-device moves.
- [ ] Apply filename collision suffix `_<backend_id>` before extension.
- [ ] On copy fallback failure, cleanup only deterministic partial destinations for the same `backendID`; never delete arbitrary existing files.
- [ ] Implement `PATCH /uploads/:id/status`: accept only `complete`; already `complete` returns `204`; `deleted` or `backend_lost` returns conflict; move failure leaves status unchanged.
- [ ] Implement `DELETE /uploads/:id`: terminate tusd upload, treat backend `404` as success, then update status to `deleted`.

### Task 7: Server Runtime Wiring

**Files:**
- Modify: `server/main.go`
- Create: `server/internal/config/config.go`
- Create: `server/internal/config/config_test.go`
- Create: `server/internal/runtime/gc.go`
- Create: `server/internal/runtime/gc_test.go`

- [ ] Read and validate env config: `BACKUP_USER`, `BACKUP_PASS`, `UPLOAD_BACKEND_URL`, `STORAGE_PATH`, `PORT`.
- [ ] Create required directories: `incoming/`, `organized/`, and BadgerDB `state/` under storage.
- [ ] Add startup retry/backoff against `UPLOAD_BACKEND_URL` before accepting traffic.
- [ ] Add BadgerDB GC goroutine: `RunValueLogGC(0.5)` every 5 minutes; ignore `badger.ErrNoRewrite`; stop on context cancellation.
- [ ] Implement graceful shutdown with `signal.Notify`, HTTP server shutdown, GC stop, and `db.Close()`.

### Task 8: Docker, tusd, and Caddy

**Files:**
- Create: `server/Dockerfile`
- Create: `server/docker-compose.yml`
- Create: `server/Caddyfile`
- Create: `server/.env.example`

- [ ] Write multi-stage Dockerfile with `CGO_ENABLED=0`.
- [ ] Define compose services: `server`, `tusd`, and `caddy`; pin tusd image/version to `v2.10.0`.
- [ ] Mount one shared `STORAGE_PATH` volume into Go server and tusd.
- [ ] Configure tusd to write under `$STORAGE_PATH/incoming`.
- [ ] Add healthchecks for Go server and tusd.
- [ ] Configure Caddy TLS via `DOMAIN` and reverse proxy only to Go server.

### Task 9: Backend Documentation and Final Verification

**Files:**
- Create: `server/README.md`
- Modify: `docs/architecture.md`
- Modify: `docs/plans/20260626-icloud-backup-system-go-backend.md`

- [ ] Document local development, env vars, test commands, Docker startup, and API endpoints.
- [ ] Document BadgerDB schema, indexes, cursor format, status lifecycle, and known limitations.
- [ ] Document TUS specifics: protocol `1.0.0`, tusd `v2.10.0`, `Upload-Defer-Length: 1`, and `Upload-Complete: ?1`.
- [ ] Align backend-relevant architecture wording so it no longer says Go backend implements TUS `1.1` or rewrites `Upload-Complete: 1`.
- [ ] Run final verification: `cd server && go test ./...`, `cd server && go test -race ./...`, `cd server && test -z "$(gofmt -l .)"`, and `git diff --check`.
