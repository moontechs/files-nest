# Plan: iCloud Backup Backend Server

## Overview
Build the backend-only iCloud Photos backup server from the current plans. The server will be a single Go process that owns BadgerDB state, embeds tusd for resumable uploads behind a narrow adapter, exposes the documented API, safely organizes completed files, and includes unit/integration tests.

## Context
- VERIFIED (from `docs/architecture.md`): server is the single source of truth; the client is stateless.
- VERIFIED (from `docs/plans/20260626-icloud-backup-system.md`): original scope included both Go backend and macOS app.
- VERIFIED (from `docs/plans/20260629-icloud-backup-server.md`): backend-only direction is to embed tusd in-process and remove the separate upload-backend container.
- VERIFIED (from repo scan): root `README.md` is currently absent; current repo is effectively greenfield with docs/plans only.
- This revised plan treats tusd internals as untrusted until compile-time and integration tests prove exact APIs, file paths, completion semantics, and cleanup behavior.

## Success Criteria
- [x] Backend lives under `server/` and builds as a Go module.
- [x] `go test ./...` passes from `server/`.
- [ ] All API endpoints are implemented behind HTTP Basic Auth.
- [ ] tusd is embedded behind `internal/uploadbackend`; no API handler imports tusd packages directly.
- [ ] There is no `UPLOAD_BACKEND_URL` and no separate upload-backend service.
- [x] BadgerDB stores upload records and maintains date/status/local/backend indexes without ghost keys.
- [x] Client-facing IDs are path-safe even when PhotoKit `localIdentifier` contains `/`, spaces, Unicode, or URL-reserved characters.
- [x] Duplicate `POST /uploads` calls are idempotent and do not leak newly-created tusd uploads.
- [ ] Duplicate `POST /uploads` calls are idempotent and do not leak newly-created tusd uploads.
- [ ] `PATCH /uploads/:id/status` verifies tusd reports known length and `offset == length` before any file move.
- [ ] Completion is crash-recoverable using persisted completion intents.
- [ ] Concurrent `PATCH /data`, `PATCH /status`, and `DELETE` for the same upload are serialized.
- [ ] `backend_lost` is detected only through normalized tusd not-found errors and returns `409 {"error":"backend_lost"}`.
- [ ] curl smoke test can run `POST /uploads` → `PATCH /uploads/:id/data` → `HEAD /uploads/:id/data` → `PATCH /uploads/:id/status` and verify the file moved to `organized/YYYY/MM/DD/`.

## Design Decisions
- Embed tusd as a library, but isolate it behind a small project-owned adapter (`internal/uploadbackend`) so tusd API changes do not leak through the codebase.
- Add a dedicated tusd verification/spike task before building handlers. The implementation must prove method names, handler configuration, deferred-length support, upload-info access, file path resolution, not-found errors, and termination cleanup behavior with compiling tests.
- Use deterministic, path-safe server IDs derived from `localIdentifier`; keep the original PhotoKit identifier in `local_identifier`.
- Store `backend_id` as the tusd upload ID.
- Do not store upload offsets in BadgerDB; always fetch offset from tusd.
- Preserve endpoint paths and `409 {"error":"backend_lost"}` response shape.
- Use per-upload in-memory locks for same-process request serialization plus BadgerDB conditional updates for durable state safety.
- Use BadgerDB completion intents to recover crashes between final file move, DB update, and tusd cleanup.
- Do not trust `PATCH /status complete` by itself; verify tusd completion first.
- Treat tusd terminate-after-move as verified behavior. If tusd cannot safely clean sidecars after the data file is moved, implement a narrow metadata cleanup path only after tests prove exact sidecar names and safety constraints.
- Tests use real BadgerDB temp dirs and real embedded tusd temp storage.

## API Surface
All routes require HTTP Basic Auth.

```text
POST   /uploads
GET    /uploads
GET    /uploads/:id
HEAD   /uploads/:id/data
PATCH  /uploads/:id/data
PATCH  /uploads/:id/status
DELETE /uploads/:id
```

## Core Request / Response Contracts
- `POST /uploads` request:
  - `localIdentifier` or `local_identifier`
  - `filename`
  - `creationDate` or `creation_date`
  - optional `bundleId` / `bundle_id`
  - optional `metadata` JSON object/blob
- `POST /uploads` response includes:
  - `id`
  - `local_identifier`
  - `status`
  - `backend_id`
  - `upload_url: "/uploads/<id>/data"`
- `HEAD /uploads/:id/data` returns TUS headers, including `Upload-Offset`.
- `PATCH /uploads/:id/data` forwards standard TUS headers/body and returns tusd response headers, including updated `Upload-Offset`.
- `PATCH /uploads/:id/status` accepts only `{ "status": "complete" }`.
- Incomplete completion returns `409 {"error":"upload_incomplete"}`.
- Lost tusd backend state returns `409 {"error":"backend_lost"}`.

## Data Model
Upload records are keyed by safe server ID: `uploads/<id>`.

```json
{
  "id": "<path-safe deterministic server id>",
  "local_identifier": "<original PhotoKit localIdentifier>",
  "status": "uploading | completing | complete | deleted | backend_lost",
  "backend_id": "<tusd upload ID>",
  "metadata": {},
  "filename": "IMG_1234.jpg",
  "bundle_id": "<Live Photo parent localIdentifier>",
  "creation_date": "2024-03-15T10:30:00Z",
  "created_at": "2024-03-15T10:30:00Z",
  "updated_at": "2024-03-15T10:30:00Z",
  "organized_path": "organized/2024/03/15/IMG_1234.jpg"
}
```

Indexes:
- `idx/date/YYYY-MM-DD/<id>` → RFC3339 creation date.
- `idx/status/<status>/<id>` → `backend_id`.
- `idx/local/<base64url(local_identifier)>` → `<id>`.
- `idx/backend/<backend_id>` → `<id>`.

Completion intent records are stored at `completion/<id>` before moving a completed upload and removed only after the DB record is safely marked `complete`.

```json
{
  "id": "<id>",
  "backend_id": "<tusd upload ID>",
  "src": "<absolute incoming path>",
  "dst": "<absolute organized path>",
  "dst_rel": "organized/YYYY/MM/DD/IMG_1234.jpg",
  "created_at": "2024-03-15T10:30:00Z"
}
```

## Storage Layout
```text
$STORAGE_PATH/
  db/
  incoming/
  organized/
    YYYY/
      MM/
        DD/
          IMG_1234.jpg
          IMG_0001_<backend_id>.jpg
```

## Invariants
- Never use raw `localIdentifier`, filename, bundle ID, or backend ID as an unescaped Badger key segment or filesystem path segment.
- `POST /uploads` idempotency is based on `local_identifier`, not raw path ID.
- If `POST /uploads` creates a tusd upload but the DB insert loses a race or fails, terminate the newly-created tusd upload before returning.
- API handlers must depend on project-owned `uploadbackend` interfaces, not tusd types.
- `UpdateStatus` must delete the old status index and write the new one in the same transaction.
- Completion must verify tusd reports known length and `offset == length`.
- `PATCH /uploads/:id/status` only accepts `complete`.
- Completed files are moved before status becomes `complete`.
- File move and DB completion must be recoverable from persisted completion intent.
- `PATCH /data`, `PATCH /status`, and `DELETE` for the same upload must not run concurrently in the same process.
- `backend_lost` is only set on normalized `uploadbackend.ErrNotFound`, not generic I/O errors.
- `DELETE` ignores `uploadbackend.ErrNotFound` but still updates BadgerDB to `deleted`.

---

### Task 1: Go Server Scaffold
**Files:**
- Create: `server/go.mod`
- Create: `server/main.go`
- Create: `server/internal/store/store.go`
- Create: `server/internal/store/store_test.go`

- [x] Initialize Go module under `server/`.
- [x] Add dependencies: `github.com/dgraph-io/badger/v4`, `github.com/go-chi/chi/v5`, and `github.com/tus/tusd/v2`.
- [x] Implement `store.Open(path string) (*Store, error)` and close support.
- [x] Add tests that open BadgerDB in a temp dir and verify the DB directory is created.
- [x] Run `go test ./...` from `server/`.

### Task 2: tusd Adapter Verification Spike
**Files:**
- Create: `server/internal/uploadbackend/tusd_api_test.go`
- Create: `server/internal/uploadbackend/errors.go`

- [x] Verify tusd handler/config/store APIs against the installed module source with compiling tests.
- [x] Verify deferred-length upload creation works with the embedded filestore.
- [x] Verify how to obtain upload ID, upload offset, known/deferred length, and upload file path.
- [x] Verify how tusd surfaces not-found and normalize it to `uploadbackend.ErrNotFound`.
- [x] Verify terminate behavior before and after the data file is moved; document whether sidecar cleanup requires a custom safe cleanup path.
- [x] Do not implement API handlers until this task passes.

### Task 3: Safe IDs, Models, And Indexes
**Files:**
- Create: `server/internal/store/index.go`
- Create: `server/internal/store/uploads.go`
- Create: `server/internal/store/uploads_test.go`
- Create: `server/internal/api/ids.go`
- Create: `server/internal/api/ids_test.go`

- [x] Define `UploadRecord`, `CompletionIntent`, status constants, `ErrNotFound`, and JSON serialization.
- [x] Implement deterministic safe ID derivation from `localIdentifier`.
- [x] Implement filename sanitization: strip directories, reject empty names, reject traversal, preserve safe extensions.
- [x] Implement `IndexRegistry`, `DateIndex`, `StatusIndex`, `LocalIdentifierIndex`, and `BackendIndex`.
- [x] Add tests for IDs and filenames containing `/`, spaces, Unicode, URL-reserved characters, and traversal attempts.

### Task 4: BadgerDB CRUD And Idempotency Primitives
**Files:**
- Modify: `server/internal/store/uploads.go`
- Modify: `server/internal/store/uploads_test.go`

- [x] Implement `PutUploadIfAbsent`, `GetUpload`, `GetUploadByLocalIdentifier`, `GetUploadByBackendID`, `UpdateStatus`, `UpdateComplete`, `DeleteUpload`, and `ListUploadsByDateRange`.
- [x] Implement cursor pagination as base64url of `<YYYY-MM-DD>/<id>`.
- [x] Ensure upload record and all indexes update atomically.
- [x] Return an existing record on duplicate local identifier without modifying it.
- [x] Add tests for CRUD, duplicate create, idempotency lookup, date pagination, status filtering, not-found behavior, and no status-index ghost keys.

### Task 5: Completion Intent Store
**Files:**
- Create: `server/internal/store/completion.go`
- Create: `server/internal/store/completion_test.go`

- [x] Implement `PutCompletionIntent`, `GetCompletionIntent`, `ListCompletionIntents`, and `DeleteCompletionIntent`.
- [x] Include `id`, `backend_id`, `src`, `dst`, `dst_rel`, and `created_at` in each intent.
- [x] Ensure completion intent writes are durable before any file move.
- [x] Add tests for create, list, delete, missing intent, and JSON compatibility.

### Task 6: Per-Upload Locking
**Files:**
- Create: `server/internal/api/locks.go`
- Create: `server/internal/api/locks_test.go`

- [x] Implement a keyed lock registry for upload IDs.
- [x] Ensure locks are released on handler return, including error paths.
- [x] Use locks for same-process serialization of `PATCH /data`, `PATCH /status`, and `DELETE`.
- [x] Add tests that concurrent operations for the same ID serialize while different IDs proceed independently.

### Task 7: Basic Auth Middleware
**Files:**
- Create: `server/internal/api/auth.go`
- Create: `server/internal/api/auth_test.go`

- [x] Implement `BasicAuth(user, pass string) func(http.Handler) http.Handler`.
- [x] Return `401` and `WWW-Authenticate` on missing or wrong credentials.
- [x] Use constant-time credential comparison.
- [x] Add tests for allowed, missing, and invalid credentials.
- [x] Run `go test ./...`.

### Task 8: Embedded tusd Wrapper
**Files:**
- Create: `server/internal/uploadbackend/tushandler.go`
- Create: `server/internal/uploadbackend/tushandler_test.go`

- [x] Implement `TUSHandler` using only APIs verified in Task 2.
- [x] Implement `CreateUpload(metadata string) (backendID string, err error)` using an in-process tusd request with `Upload-Defer-Length: 1`.
- [x] Implement `GetOffset`, `ForwardPatch`, `GetInfo`, `IsComplete`, `FilePath`, and `TerminateOrCleanup`.
- [x] Normalize tusd not-found into `uploadbackend.ErrNotFound` while preserving non-not-found errors.
- [x] Add tests using temp storage that create an upload, patch bytes with standard TUS headers, resolve deferred length, read offset/info, verify completion, and cleanup.

### Task 9: Upload Registration And Listing API
**Files:**
- Create: `server/internal/api/handlers.go`
- Create: `server/internal/api/handlers_test.go`

- [x] Implement `POST /uploads`: parse request, sanitize filename, derive safe `id`, create tusd upload, and store `uploading` record.
- [x] Make `POST /uploads` race-safe: if DB insert fails due to an existing local identifier, terminate the newly-created tusd upload and return the existing record.
- [x] If DB write fails after tusd creation for any other reason, terminate the newly-created tusd upload before returning 500.
- [x] Implement `GET /uploads` with `from`, `to`, `status`, `limit`, and `cursor`.
- [x] Implement `GET /uploads/:id`.
- [x] Add httptest coverage for create, duplicate create, unsafe ID handling, DB-failure cleanup, list pagination, status filter, get success, and get 404.

### Task 10: TUS Data API
**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`

- [x] Implement `HEAD /uploads/:id/data` by looking up `backend_id` and returning tusd `Upload-Offset`.
- [x] Implement `PATCH /uploads/:id/data` by forwarding request body and standard TUS headers to embedded tusd.
- [x] Wrap same-ID `PATCH /data` operations with the per-upload lock.
- [x] On `uploadbackend.ErrNotFound`, update status to `backend_lost` and return `409 {"error":"backend_lost"}`.
- [x] On non-not-found tusd errors, return `500` without changing status to `backend_lost`.
- [x] Add tests for offset, patch upload, deferred-length finalization, missing upload record, typed `backend_lost`, non-not-found error handling, and same-ID serialization.

### Task 11: File Move Planning And Moving
**Files:**
- Create: `server/internal/filestore/mover.go`
- Create: `server/internal/filestore/mover_test.go`

- [x] Implement `PlanDestination(root, creationDate, createdAt, filename, backendID)` returning deterministic final path and relative organized path.
- [x] Implement collision handling by inserting `_<backend_id>` before the file extension.
- [x] Implement `MoveFile(src, dst string) error` with `os.Rename` first and copy-delete fallback on `EXDEV`.
- [x] Make move idempotent for recovery: if `src` is missing but intended `dst` exists, treat as already moved.
- [x] Add tests for normal move, collision suffix, nested directories, creation-date fallback, missing-source/already-moved recovery, and copy fallback where practical.

### Task 12: Complete And Delete API
**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`

- [x] Implement `PATCH /uploads/:id/status` accepting only `{ "status": "complete" }`.
- [x] Acquire the per-upload lock before checking tusd state, writing intent, moving files, or updating status.
- [x] Before moving, call `TUSHandler.IsComplete`; return `409 {"error":"upload_incomplete"}` if length is deferred or offset does not equal length.
- [x] Write `completion/<id>` intent before moving the file.
- [x] On complete: move file idempotently, update record to `complete` with `organized_path`, delete completion intent, then run verified tusd cleanup.
- [x] If cleanup-after-complete fails with non-not-found, keep status `complete` and log cleanup failure; do not roll back completion.
- [x] Implement `DELETE /uploads/:id`: acquire the per-upload lock, terminate tusd state, ignore not-found, update status to `deleted`.
- [x] Add tests for complete success, incomplete rejection, invalid status, move failure preserving `uploading`, completion intent behavior, cleanup-after-complete, delete success, and delete ignoring not-found.

### Task 13: Startup Recovery
**Files:**
- Create: `server/internal/api/recovery.go`
- Create: `server/internal/api/recovery_test.go`
- Modify: `server/main.go`

- [x] On startup, scan `completion/<id>` records before serving requests.
- [x] For each intent, acquire the per-upload lock before recovery.
- [x] If record is already `complete`, delete stale intent and retry verified tusd cleanup.
- [x] If record is still `uploading` or `completing`, rerun idempotent move, update status to `complete`, delete intent, and attempt tusd cleanup.
- [x] If source and destination are both missing, leave the record unchanged, keep the intent, log enough detail for manual repair, and do not delete data.
- [x] Add tests simulating crash before move, after move before DB update, after DB update before intent cleanup, and after completion before tusd cleanup.

### Task 14: Router, Runtime Wiring, And Docker
**Files:**
- Create: `server/internal/api/router.go`
- Modify: `server/main.go`
- Create: `server/Dockerfile`
- Create: `server/docker-compose.yml`
- Create: `server/Caddyfile`

- [ ] Wire chi router with Basic Auth applied to all upload routes.
- [ ] Read config from `BACKUP_USER`, `BACKUP_PASS`, `STORAGE_PATH`, and `PORT`.
- [ ] Initialize BadgerDB at `$STORAGE_PATH/db`.
- [ ] Initialize embedded tusd at `$STORAGE_PATH/incoming`.
- [ ] Run startup completion recovery before `ListenAndServe`.
- [ ] Start BadgerDB value-log GC goroutine and ignore `badger.ErrNoRewrite`.
- [ ] Create multi-stage Dockerfile and docker-compose with one backend service plus Caddy only.
- [ ] Confirm there is no `UPLOAD_BACKEND_URL` or second upload-backend service.

### Task 15: Backend Documentation And Verification
**Files:**
- Create: `server/README.md`
- Modify: `docs/plans/20260629-icloud-backup-backend-server.md`

- [ ] Document setup, env vars, storage layout, Docker usage, and Caddy usage.
- [ ] Document all API endpoints with request/response examples.
- [ ] Document safe server IDs versus original `local_identifier`.
- [ ] Document TUS deferred-length requirements and standard headers.
- [ ] Document status lifecycle, `backend_lost` behavior, startup recovery, and tusd cleanup behavior.
- [ ] Run `go test ./...` from `server/`.
- [ ] Run curl smoke tests for create/upload/head/complete, incomplete-complete rejection, delete, backend-lost, and recovery scenarios.
- [ ] Move the implementation plan to `docs/plans/completed/` when implementation is finished.
