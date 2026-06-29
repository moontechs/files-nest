# iCloud Backup System — Go Server (Backend-Only)

## Overview

Backend-only implementation of the iCloud Photos backup system. A single Go process owns sync state (BadgerDB) and serves resumable uploads by embedding tusd v2 as a library — no separate upload-backend container, no internal HTTP hop.

- **server/** — Go HTTP API + embedded tusd upload handler + BadgerDB state store + organized file tree
- **Client** — out of scope for this plan. Any TUS 1.1 client (the macOS app in the sibling repo, or curl for smoke testing) talks to the same API.

Solves resumable upload of very large files (7GB+) without landing temp files on the client. The server is the single source of truth for sync state.

## Context

- Derived from `docs/plans/20260626-icloud-backup-system.md` (full system) and `docs/architecture.md`
- Scope cut: all macOS app tasks (original Tasks 8–16) and app-specific acceptance checks are dropped
- Architecture change: the "Go server + separate tusd container proxied over HTTP" pair is collapsed into one Go process. tusd v2 is embedded via `github.com/tus/tusd/v2/pkg/handler` + `github.com/tus/tusd/v2/pkg/filestore`.
- Greenfield — no existing server code yet
- Go + BadgerDB (pure Go, no CGO) + chi router + embedded tusd
- Auth: HTTP Basic Auth (`BACKUP_USER` / `BACKUP_PASS`)
- TUS feature used: `Upload-Defer-Length: 1` (file size not known upfront)

## Development Approach

- **Testing approach**: Regular (code first, tests after)
- Complete each task fully before moving to the next
- All tests must pass before starting the next task
- Update this plan if scope changes during implementation

## Testing Strategy

- **Server**: Go table-driven unit tests + `httptest` for handler integration tests
- No mocking of BadgerDB — tests use a real BadgerDB instance in a temp directory
- No mocking of tusd — tests use a real `uploadbackend.TUSHandler` against a temp `incoming/` dir
- TUS data-handler tests assert typed-error transitions (`errors.Is(err, handler.ErrNotFound)`), not HTTP status-code matching

## Progress Tracking

- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document blockers with ⚠️ prefix

---

## Solution Overview

### Single-process architecture

One Go process. tusd v2 is embedded as a library: `handler.UnroutedHandler` instantiated in-process, with `filestore.FileStore` pointing at `$STORAGE_PATH/incoming`. The server owns BadgerDB state; tusd owns partial-file storage under `incoming/`. There is no `UPLOAD_BACKEND_URL`, no second compose service, no internal HTTP hop.

```
server/
  main.go                              # config + wiring + BadgerDB GC goroutine
  internal/
    store/        # BadgerDB: Open, IndexRegistry, uploads CRUD (+ tests)
    uploadbackend/  # tusd wrapper: TUSHandler — CreateUpload/GetOffset/ForwardPatch/Terminate (+ tests)
    api/          # chi router, BasicAuth, handlers (+ tests)
    filestore/    # MoveFile for the organized/ tree (+ tests)
```

`uploadbackend.TUSHandler` is a thin struct holding an `*handler.UnroutedHandler` plus a `*filestore.FileStore`. It exposes the same interface the original plan's `uploadbackend.Client` did, so the handler layer is unchanged — it still talks to an `uploadbackend`-shaped dependency. The difference is that every method calls tusd in-process instead of over HTTP:

- `CreateUpload(metadata) (backendID, error)` builds an in-process `http.Request` carrying `Upload-Defer-Length: 1` and invokes tusd's `PostFile`, parsing the tusd upload ID from the response.
- `GetOffset(backendID)` → tusd `HeadFile`
- `ForwardPatch(backendID, r)` → tusd `PatchFile`
- `Terminate(backendID)` → tusd `DelFile` (terminate)

A 404 from tusd is caught as `errors.Is(err, handler.ErrNotFound)` — a typed error — instead of string-matching a proxied HTTP status. `backend_lost` detection becomes a typed-error branch. A genuine I/O error from the file store (e.g. disk full) is **not** `ErrNotFound` and must surface as 500 — it must not be misclassified as `backend_lost`.

> ⚠️ **Verify before implementing Task 5**: tusd v2's `handler.UnroutedHandler` method names (`PostFile`/`HeadFile`/`PatchFile`/`DelFile` or similar), the `handler.Config` / `StoreComposer` surface, and `Upload-Defer-Length` support in the disk `filestore` composer. Confirm against the tusd v2 docs / source at `github.com/tus/tusd/v2` before writing glue code. The method names above are recalled from training and may have shifted.

### API surface

The external API is unchanged from the original plan — a TUS 1.1 client sees the same endpoints. Only *how* the server fulfills `/uploads/:id/data` changes (delegated to the embedded tusd handler instead of reverse-proxied).

```
POST   /uploads              register asset, CreateUpload in-process, return id
GET    /uploads              list (from/to/status/limit/cursor)
GET    /uploads/:id          single record or 404
HEAD   /uploads/:id/data     GetOffset via tusd HeadFile
PATCH  /uploads/:id/data     ForwardPatch via tusd PatchFile
PATCH  /uploads/:id/status   complete only → MoveFile → Terminate → UpdateStatus(complete)
DELETE /uploads/:id          Terminate via tusd (ignore ErrNotFound) → UpdateStatus(deleted)
```

All routes sit behind the `BasicAuth` middleware. `POST /uploads` idempotency is unchanged: on an existing record, return the current record and let the client branch on `status` (`uploading`→resume, `complete`→skip, `deleted`→skip, `backend_lost`→DELETE then POST again).

### Status flow

```
uploading ── PATCH /status complete ──→ complete
uploading ── tusd ErrNotFound on HEAD/PATCH ──→ backend_lost
uploading / complete / backend_lost ── DELETE ──→ deleted
```

Identical to the architecture doc. The single behavioral difference is the `backend_lost` detection mechanism: in `HEAD /uploads/:id/data` and `PATCH /uploads/:id/data`, after looking up `backend_id` from BadgerDB, the wrapper calls the tusd method; on `errors.Is(err, handler.ErrNotFound)` the handler calls `UpdateStatus(id, "backend_lost")` and returns `409 Conflict {"error":"backend_lost"}`. The 409 response body shape is preserved so client `BackendLostError` parsing is untouched.

### BadgerDB schema

Main records keyed by `uploads/<localIdentifier>`:

```json
{
  "id":            "<localIdentifier>",
  "status":        "uploading | complete | deleted | backend_lost",
  "backend_id":    "<tusd upload ID>",
  "metadata":      "<JSON blob>",
  "filename":      "IMG_1234.jpg",
  "bundle_id":     "<localIdentifier of Live Photo parent>",
  "creation_date": "2024-03-15T10:30:00Z",
  "created_at":    "2024-03-15T10:30:00Z",
  "updated_at":    "2024-03-15T10:30:00Z"
}
```

`backend_id` is the **tusd upload ID** (the `<id>` segment tusd generates on `PostFile`), returned from `CreateUpload` and stored verbatim. No offset in BadgerDB — resume offset is fetched live via the tusd `HeadFile` path. `StatusIndex` value stays `<backend_id>` so the resume scan recovers the tusd ID without an extra lookup.

Embedding tusd changes only the `backend_id` semantics (tusd ID instead of proxied-backend ID) and the `backend_lost` transition source (typed error instead of HTTP 404). The record shape, indexes, ghost-key invariant, and cursor pagination are unchanged from the original plan.

### Indexes

```go
type IndexEntry struct {
    Key   []byte
    Value []byte
}

type Index interface {
    Entries(r *UploadRecord) []IndexEntry
}

type IndexRegistry struct {
    indexes []Index
}
func (reg *IndexRegistry) Register(idx Index)
```

- `DateIndex`: key `idx/date/2024-03-15/<id>`, value `"2024-03-15T10:30:00Z"` — range scans without loading main records
- `StatusIndex`: key `idx/status/uploading/<id>`, value `"<backend_id>"` — resume scan without extra lookup

All index entries written in the same BadgerDB transaction as the main record. **StatusIndex ghost-key invariant:** `UpdateStatus` must (1) load the existing record to determine the old status, (2) delete `idx/status/<old_status>/<id>`, (3) write `idx/status/<new_status>/<id>`, (4) write the updated main record — all in one transaction. Skipping step 2 leaves a ghost entry that corrupts every status scan.

### Cursor pagination

`GET /uploads` accepts `?from=&to=&status=&limit=500&cursor=<composite>`. Cursor is `base64(<YYYY-MM-DD>/<localIdentifier>)` — the date is required to reconstruct the exact seek key `idx/date/YYYY-MM-DD/<id>`. A localIdentifier alone cannot position a seek in a date-ordered index scan. Returned URL-safe base64 encoded; client pages until `next_cursor` is empty.

### Storage layout

```
$STORAGE_PATH/
  incoming/         ← tusd filestore writes partial files here (named by tusd ID) + .info sidecars + locks
  organized/
    YYYY/
      MM/
        DD/
          IMG_1234.jpg
          IMG_0001_abc123.jpg   ← collision suffix: _<backend_id> before extension
```

tusd's `filestore.FileStore` is constructed with `filepath.Join(storagePath, "incoming")` and owns everything under that directory. The server never reads or writes `incoming/` except through tusd, and except for **one** operation — `MoveFile` — which reads the completed file *after* tusd reports the upload as finished but *before* `UpdateStatus(complete)`.

`filestore.MoveFile(src, dst string) error` (in `internal/filestore/mover.go`):
1. Try `os.Rename(src, dst)` — fast path, same filesystem.
2. On `errors.Is(err, syscall.EXDEV)` (cross-device), fall back to `copyFile` (buffered `io.Copy`) + `os.Remove(src)`.
3. Before writing, if `dst` already exists, insert `_<backend_id>` before the extension. The caller (`PATCH /status complete` handler) passes the record's `backend_id` so the suffix is the tusd upload ID — deterministic and traceable.

The `PATCH /status complete` handler composes `dst` as `organized/YYYY/MM/DD/<filename>` using the record's `creation_date`, **falling back to `created_at` if `creation_date` is zero** (a defensive guard the original plan omits). It calls `MoveFile` first; **only on success** does it call `Terminate(backendID)` to let tusd clean up its `.info` sidecar and lock, **then** `UpdateStatus(id, "complete")`. On failure it returns 500 and leaves the status as `uploading` so the client retries — no half-state, no orphan in `organized/`.

> Embedded-tusd subtlety: after `MoveFile`, the tusd `.info` sidecar and any lock file remain in `incoming/`. The handler must call `Terminate(backendID)` *after* a successful move to prevent stale `.info` files from accumulating. DELETE also terminates (idempotent — `ErrNotFound` ignored).

### Config

`main.go` reads only: `BACKUP_USER`, `BACKUP_PASS`, `STORAGE_PATH`, `PORT`. **No `UPLOAD_BACKEND_URL`.** `STORAGE_PATH/incoming` is handed to `filestore.New()`; `STORAGE_PATH/organized` is the MoveFile root.

---

## Implementation Steps

---

### Task 1: Go server scaffold

**Files:**
- Create: `server/main.go`
- Create: `server/go.mod`
- Create: `server/internal/store/store.go`
- Create: `server/internal/store/store_test.go`

- [x] `go mod init` with module name, add dependencies: `dgraph-io/badger/v4`, `chi` router, `github.com/tus/tusd/v2` (`pkg/handler` + `pkg/filestore`)
- [x] open BadgerDB in `internal/store/store.go`: `Open(path string) (*Store, error)`
- [x] write tests: Open creates DB directory, second Open on same path succeeds
- [x] run tests — must pass before task 2

---

### Task 2: Index registry + upload record CRUD

**Files:**
- Create: `server/internal/store/index.go`
- Create: `server/internal/store/uploads.go`
- Create: `server/internal/store/uploads_test.go`

- [x] implement `IndexEntry`, `Index` interface, `IndexRegistry` with `Register` + internal write/delete helpers
- [x] implement `DateIndex` (key: `idx/date/YYYY-MM-DD/<id>`, value: RFC3339 date)
- [x] implement `StatusIndex` (key: `idx/status/<status>/<id>`, value: backend_id)
- [x] implement `CreateUpload` — writes main record + all index entries in one transaction
- [x] implement `GetUpload(id string) (*Upload, error)`
- [x] implement `UploadByLocalIdentifier` / `UploadByBackendID` for reverse lookups
- [x] implement `ListByStatus(status)` — scans status index prefix
- [x] implement `ListByDateRange(from, to, status, limit, cursor)` — scans date index, loads main records; cursor is base64(`<YYYY-MM-DD>/<id>`); returns next cursor or empty string
- [x] implement `UpdateStatus(id, status)` — in a single transaction: load existing record (return ErrNotFound if missing), delete old `idx/status/<old_status>/<id>` key, write updated record, write new `idx/status/<new_status>/<id>` key; never skip the delete step or ghost entries accumulate
- [x] implement `DeleteUpload(id)` — removes record + all index entries
- [x] implement `CompletionIntent` save/get/delete/list for crash recovery
- [x] implement `SafeID`, `ValidateSafeID`, `LocalIdentifierIndexKey` in `api/ids.go`
- [x] write comprehensive tests for each function (success + not-found + pagination + ghost-key + concurrent access)
- [x] run tests — must pass before task 3

> Embedding tusd touches nothing here. This task is verbatim from the original plan.

---

### Task 3: HTTP Basic Auth middleware

**Files:**
- Create: `server/internal/api/auth.go`
- Create: `server/internal/api/auth_test.go`

- [ ] implement `BasicAuth(user, pass string) func(http.Handler) http.Handler` middleware
- [ ] returns 401 with `WWW-Authenticate` header on missing/wrong credentials
- [ ] reads credentials from env vars `BACKUP_USER` and `BACKUP_PASS`
- [ ] write tests: correct credentials pass through, wrong credentials return 401
- [ ] run tests — must pass before task 4

---

### Task 4: POST /uploads + GET /uploads handlers + `uploadbackend.TUSHandler.CreateUpload`

**Files:**
- Create: `server/internal/api/handlers.go`
- Create: `server/internal/api/handlers_test.go`
- Create: `server/internal/uploadbackend/tushandler.go`
- Create: `server/internal/uploadbackend/tushandler_test.go`

- [ ] implement `uploadbackend.TUSHandler`: a struct wrapping `*handler.UnroutedHandler` + `*filestore.FileStore`, constructed with `New(storagePath string) (*TUSHandler, error)` — points the filestore at `filepath.Join(storagePath, "incoming")` and wires the handler config
- [ ] implement `TUSHandler.CreateUpload(metadata string) (backendID string, err error)` — builds an in-process `http.Request` with `Upload-Defer-Length: 1` and the `Upload-Metadata` header, invokes tusd's `PostFile` against an `httptest.ResponseRecorder`, parses the tusd upload ID from the response `Location` header (or body, per tusd v2 — verify)
- [ ] implement `POST /uploads` handler: parse `{ localIdentifier, metadata, creationDate, filename, bundleId }`, call `TUSHandler.CreateUpload`, store record in BadgerDB (status: uploading), return `{ id, status, upload_url: "/uploads/<id>/data" }`
- [ ] handle conflict: if record already exists, return the existing record's current status to the client regardless of what that status is — let the client decide: `uploading` → resume; `complete` → skip; `deleted` → skip (only re-upload if user explicitly re-syncs); `backend_lost` → treat as new upload (call DELETE /uploads/:id first, then POST again)
- [ ] implement `GET /uploads` handler: parse `from`, `to` (RFC3339), `status`, `limit`, `cursor`; call `ListUploadsByDateRange`; return JSON array + `next_cursor` field; empty result returns `{ "items": [], "next_cursor": "" }`
- [ ] implement `GET /uploads/:id` handler: return single record or 404
- [ ] write httptest tests for all three handlers; `TUSHandler` tests use a real handler against a temp `incoming/` dir
- [ ] run tests — must pass before task 5

---

### Task 5: TUS data handlers

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`
- Modify: `server/internal/uploadbackend/tushandler.go`
- Modify: `server/internal/uploadbackend/tushandler_test.go`

- [ ] ⚠️ first verify tusd v2 `UnroutedHandler` method names + `StoreComposer` defer-length support against `github.com/tus/tusd/v2` docs/source
- [ ] add `TUSHandler` methods: `GetOffset(backendID string) (int64, error)`, `ForwardPatch(backendID string, w http.ResponseWriter, r *http.Request) error`, `Terminate(backendID string) error` — each invokes the corresponding tusd method in-process
- [ ] implement `HEAD /uploads/:id/data`: look up backend_id from BadgerDB, call `GetOffset`; on `errors.Is(err, handler.ErrNotFound)` call `UpdateStatus(id, "backend_lost")` and return `409 Conflict` with body `{"error":"backend_lost"}` — never surface the tusd error raw; on non-`ErrNotFound` error return 500 (must not be misclassified as `backend_lost`)
- [ ] implement `PATCH /uploads/:id/data`: call `ForwardPatch` to stream the body + TUS headers to tusd in-process; same `backend_lost` transition on `ErrNotFound`; same 500 on other errors
- [ ] write httptest tests: correct offset returned; chunk forwarded with correct headers; 404 on unknown record id; tusd `ErrNotFound` transitions to `backend_lost` and returns 409; a non-`ErrNotFound` I/O error returns 500 (assert the typed-error branch, not status-code matching)
- [ ] run tests — must pass before task 6

---

### Task 6: PATCH /uploads/:id/status + DELETE /uploads/:id handlers

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`
- Create: `server/internal/filestore/mover.go`
- Create: `server/internal/filestore/mover_test.go`

- [ ] implement `filestore.MoveFile(src, dst string) error`: tries `os.Rename`, falls back to `copyFile` + `os.Remove` for cross-device moves (`errors.Is(err, syscall.EXDEV)`); before writing, check if `dst` already exists — if so, insert `_<backend_id>` before the extension (e.g. `IMG_0001_abc123.jpg`) to prevent silent overwrite. The caller passes `backend_id` so the suffix is the tusd upload ID.
- [ ] implement `PATCH /uploads/:id/status` handler:
  - only accepts `status == "complete"` — any other value returns 400
  - compose `dst` as `$STORAGE_PATH/organized/YYYY/MM/DD/<filename>` using the record's `creation_date`; **fall back to `created_at` if `creation_date` is zero** (defensive guard)
  - move file from `$STORAGE_PATH/incoming/<backend_id>` to `dst` (with collision suffix if needed)
  - **only if move succeeds**: call `TUSHandler.Terminate(backendID)` to clean up the tusd `.info` sidecar + lock, then call `UpdateStatus(complete)`
  - if move fails: return 500, leave status as `uploading` (retryable), do not call Terminate
  - return 204; return 404 if record not found
- [ ] implement `DELETE /uploads/:id` handler: call `TUSHandler.Terminate(backendID)` (treat `errors.Is(err, handler.ErrNotFound)` as success — backend may already be gone), then call `UpdateStatus(deleted)`, return 204
- [ ] write tests: PATCH complete succeeds and calls Terminate; PATCH with status=deleted returns 400; collision suffix applied when dest exists; MoveFile failure leaves status unchanged and does not call Terminate; creation_date zero falls back to created_at; DELETE ignores tusd `ErrNotFound`
- [ ] run tests — must pass before task 7

---

### Task 7: Wiring + Docker setup

**Files:**
- Create: `server/internal/api/router.go`
- Modify: `server/main.go`
- Create: `server/Dockerfile`
- Create: `server/docker-compose.yml`
- Create: `server/Caddyfile`

- [ ] wire chi router with BasicAuth middleware on all routes
- [ ] read config from env: `BACKUP_USER`, `BACKUP_PASS`, `STORAGE_PATH`, `PORT` only — **no `UPLOAD_BACKEND_URL`**
- [ ] construct `uploadbackend.TUSHandler` with `STORAGE_PATH`; construct `store.Open(STORAGE_PATH/db)`; construct `filestore` mover with `STORAGE_PATH/organized` root
- [ ] start a BadgerDB GC goroutine: call `db.RunValueLogGC(0.5)` every 5 minutes in a background goroutine, ignore `badger.ErrNoRewrite` (nothing to reclaim), stop on context cancellation — without this, the value log grows unboundedly as records are updated
- [ ] Dockerfile: multi-stage Go build (pure Go, no CGO needed — BadgerDB and tusd are pure Go)
- [ ] docker-compose: **single service** + Caddy reverse proxy
  - one container, one process — no second upload-backend service, no shared-volume two-service wiring
  - `STORAGE_PATH` volume mounted into the server container
  - Caddy handles TLS termination (auto-HTTPS via Let's Encrypt or self-signed for LAN)
  - `DOMAIN` env var passed to Caddyfile
- [ ] smoke test: `docker-compose up`, curl `POST /uploads` returns 200
- [ ] run unit tests — must pass before task 8

---

### Task 8: Backend acceptance + docs

**Files:**
- Create: `server/README.md`
- Modify: this plan (move to `docs/plans/completed/`)

- [ ] `go test ./...` — all server tests pass
- [ ] curl smoke test of the full flow: `POST /uploads` → `PATCH /uploads/:id/data` (chunk) → `HEAD /uploads/:id/data` (offset) → `PATCH /uploads/:id/status` complete → verify file appears under `organized/YYYY/MM/DD/`
- [ ] curl smoke test: `DELETE /uploads/:id` → record status `deleted`, tusd `.info` gone
- [ ] curl smoke test: simulate `backend_lost` (e.g. remove incoming file out-of-band, then `HEAD`) → 409 `{"error":"backend_lost"}`
- [ ] server README: setup, env vars (`BACKUP_USER`, `BACKUP_PASS`, `STORAGE_PATH`, `DOMAIN`, `PORT`), docker-compose usage, API reference (all endpoints + request/response shapes + status flow)
- [ ] move this plan to `docs/plans/completed/`

---

## Post-Completion

**Manual / curl testing scenarios:**
- Upload a multi-chunk file — verify partial file under `incoming/`, then organized move on complete
- Kill the server mid-upload, restart, `HEAD /uploads/:id/data` — verify resume offset returned
- Trigger `backend_lost` (remove incoming file) — verify 409 + status transition
- Collision: upload two files with same filename + creation_date — verify second gets `_<backend_id>` suffix
- Cross-device: mount `organized/` on a different volume — verify copy+delete fallback path

**Deployment:**
- Pin tusd v2 version in `go.mod`
- `DOMAIN` env var drives Caddy auto-HTTPS — point DNS before first `docker-compose up`
- For LAN-only use: replace Caddy with self-signed cert or use Tailscale
- Single container, single process — no upload-backend to pin or monitor separately

**Client integration (out of scope here):**
- The macOS app in the sibling repo (`files-nest-mac`) consumes this API unchanged — the 409 `backend_lost` body shape and all endpoint paths are preserved from the original full-system plan
- Any TUS 1.1 client works for smoke testing

## Open verification items

- ⚠️ tusd v2 `handler.UnroutedHandler` method names (`PostFile`/`HeadFile`/`PatchFile`/`DelFile` or equivalents) — verify before Task 5
- ⚠️ tusd v2 `StoreComposer` support for `Upload-Defer-Length` with the disk `filestore` — verify before Task 5
- ⚠️ How tusd v2 surfaces the upload ID on `PostFile` (response `Location` header vs body) — verify before Task 4
