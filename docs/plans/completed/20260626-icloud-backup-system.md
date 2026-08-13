# iCloud Backup System — Go Server + macOS Menu Bar App

## Overview

Two-repo system to back up iCloud Photos and Videos to a self-hosted homeserver.

- **server/** — Go HTTP API with resumable upload backend, stores upload state in BadgerDB, public repo
- **files-nest-mac/** — macOS SwiftUI menu bar app, streams from iCloud to server via TUS, private repo

Solves resumable upload of very large files (7GB+) without landing temp files on disk. Server is the single source of truth — the Mac app is stateless (no local DB).

iOS support is a future goal; PhotoKit and TUS client code in the Mac app should be modular.

## Context

- Greenfield — no existing code
- Server: Go + BadgerDB + resumable upload backend (internal, not exposed)
- App: Swift + SwiftUI + PhotoKit + custom TUS 1.1 client
- Auth: HTTP Basic Auth (username + password stored in macOS Keychain)
- File streaming: PHAssetResourceManager.requestData → no temp files
- TUS feature used: Upload-Defer-Length (no upfront file size needed)
- All TUS traffic proxied through Go server — upload backend is never exposed externally

## Development Approach

- **Testing approach**: Regular (code first, tests after)
- Complete each task fully before moving to the next
- All tests must pass before starting the next task
- Update this plan if scope changes during implementation

## Testing Strategy

- **Server**: Go table-driven unit tests + httptest for handler integration tests
- **Mac app**: XCTest unit tests for SyncCoordinator, TUSClient, MetadataSerializer; UI tested manually

## Progress Tracking

- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document blockers with ⚠️ prefix

---

## Solution Overview

### Server

Go API that owns sync state and proxies all TUS traffic. Exposes:
- `POST /uploads` — register asset, create upload backend entry, return upload URL
- `GET /uploads` — list assets (filterable by date range + status, cursor-paginated)
- `GET /uploads/:id` — get single upload record
- `HEAD /uploads/:id/data` — get current TUS offset (proxied to upload backend)
- `PATCH /uploads/:id/data` — upload chunk (proxied to upload backend)
- `PATCH /uploads/:id/status` — update status to `complete` only; moves file on server, updates record
- `DELETE /uploads/:id` — mark deleted + send TUS Termination to upload backend

All endpoints require HTTP Basic Auth. State stored in BadgerDB (pure Go, no CGO).

File storage layout under `STORAGE_PATH`:
```
$STORAGE_PATH/
  incoming/         ← upload backend writes here
  organized/
    YYYY/
      MM/
        DD/
          IMG_1234.jpg
```

### macOS App

Menu bar app (NSStatusItem + SwiftUI popover). On each sync:
1. Fetch PHAssets for the selected range (or all, for Full Sync) via `AssetLibraryProtocol`
2. Query server `GET /uploads` for same range (cursor-paginated)
3. Diff: assets missing on server → upload; assets on server missing in library → delete
4. For each asset: `POST /uploads` → stream via `AssetResourceManagerProtocol` → `PATCH /uploads/:id/data` chunks
5. `PATCH /uploads/:id/status { status: "complete" }` after each upload
6. Live Photos: upload JPEG resource and MOV resource as two linked records (same `bundle_id`)

## Technical Details

### BadgerDB schema

All upload records keyed by `uploads/<localIdentifier>`:

```json
{
  "id":            "<localIdentifier>",
  "status":        "uploading",
  "backend_id":    "<upload backend internal ID>",
  "metadata":      "<JSON blob from PHAsset>",
  "filename":      "IMG_1234.jpg",
  "bundle_id":     "<localIdentifier of parent PHAsset, for Live Photo pairs>",
  "creation_date": "2024-03-15T10:30:00Z",
  "created_at":    "2024-03-15T10:30:00Z",
  "updated_at":    "2024-03-15T10:30:00Z"
}
```

**No offset stored in BadgerDB.** Resume offset is always fetched live via `HEAD /uploads/:id/data`.

**StatusIndex ghost key invariant:** When a record's status changes, `UpdateStatus` must (1) load the existing record to determine the old status, (2) delete the old `idx/status/<old_status>/<id>` key, (3) write the new `idx/status/<new_status>/<id>` key, and (4) write the updated main record — all in a single BadgerDB transaction. Failing to delete the old key leaves a stale ghost entry; every status scan for the old status will include records that have already transitioned.

**Status values:** `uploading | complete | deleted | backend_lost` — no `pending` state. Records are created with `uploading` immediately so crashes never leave orphaned pending records. `backend_lost` is set by the server when a HEAD or PATCH proxy call returns 404 from the upload backend (e.g. after tusd restart); the Mac app handles this by calling `POST /uploads` again to create a fresh backend entry.

### Extensible index registry

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

Built-in indexes:
- `DateIndex`: key `idx/date/2024-03-15/<id>`, value `"2024-03-15T10:30:00Z"` — enables range scans without loading main records
- `StatusIndex`: key `idx/status/uploading/<id>`, value `"<backend_id>"` — enables resume scan without extra lookup

All index entries written in the same BadgerDB transaction as the main record. Adding a new index = implement `Index`, register at startup.

### TUS proxy flow (no temp files)

```
POST /uploads
  → Go server calls upload backend POST /files (Upload-Defer-Length: 1)
  → BadgerDB record created (status: uploading, backend_id stored)
  → returns { id, upload_url: "/uploads/<id>/data" }

PATCH /uploads/:id/data  (chunk upload)
  → Go server looks up backend_id from BadgerDB
  → forwards PATCH to upload backend /files/<backend_id>
  → copies Upload-Offset response header back to Mac app

HEAD /uploads/:id/data  (resume offset)
  → Go server proxies HEAD to upload backend
  → returns Upload-Offset header

PATCH /uploads/:id/status { status: "complete" }
  → Go server moves file: incoming/<backend_id> → organized/YYYY/MM/DD/<filename>
  → if dest path already exists, append _<backend_id> before extension to avoid silent overwrite
  → cross-device safe: os.Rename attempted first, falls back to copy+delete
  → BadgerDB status → complete (if MoveFile fails, status stays uploading — no half-state)

HEAD /uploads/:id/data or PATCH /uploads/:id/data → backend returns 404
  → Go server updates BadgerDB status → backend_lost
  → returns 409 Conflict with body: { "error": "backend_lost" }
  → Mac app re-calls POST /uploads to get a fresh backend_id (which resets status to uploading)

DELETE /uploads/:id
  → sends TUS Termination DELETE to upload backend (ignore 404 — backend may already be gone)
  → BadgerDB status → deleted
```

### Live Photo handling

A Live Photo has two `PHAssetResource`s (JPEG + MOV). Each is uploaded as a separate record:
- `<localIdentifier>_photo` → JPEG resource
- `<localIdentifier>_video` → MOV resource
- Both records share `bundle_id = <localIdentifier>`

The sync coordinator treats the pair as one unit for diffing and deletion.

### Metadata fields (PHAsset → JSON → TUS Upload-Metadata)

- localIdentifier, creationDate, modificationDate
- latitude, longitude, altitude (CLLocation)
- mediaType, mediaSubtypes, pixelWidth, pixelHeight
- duration (video), isFavorite, isHidden
- burstIdentifier, sourceType, filename
- bundleId (for Live Photo pairs), resourceType (photo / video)

### Pagination

`GET /uploads` accepts `?from=&to=&status=&limit=500&cursor=<composite>`. BadgerDB date index enables efficient range iteration. **Cursor is a composite value encoding both the date and the id: `<YYYY-MM-DD>/<localIdentifier>`.** A localIdentifier alone cannot position a seek in a date-ordered index scan — the date is needed to reconstruct the exact key `idx/date/YYYY-MM-DD/<id>`. The cursor is URL-safe base64 encoded in the response. Mac app pages until `next_cursor` is empty.

### Deletion scoping

- Range sync: diff only assets within the date range
- Full sync: diff entire library vs all server records (paginated)

### macOS entitlements (required)

- `com.apple.security.personal-information.photos-library` in entitlements file
- `NSPhotoLibraryUsageDescription` in Info.plist
- Without these, PhotoKit authorization silently fails

### localIdentifier stability

`PHAsset.localIdentifier` is stable within a device but **resets on device restore or migration to a new iPhone**. After a migration, every asset appears new to the server — a full re-upload occurs. This is expected behavior; no special handling is planned beyond logging it clearly. Future mitigation (content hash deduplication) is out of scope.

### iCloud download asymmetry

`requestData` cannot resume mid-file. If interrupted at byte 3GB of a 7GB video, iCloud restarts from byte 0 even if the TUS upload resumes from byte 3GB. The coordinator logs this clearly. Set `networkAccessAllowed = true` and a generous timeout on `PHAssetResourceRequestOptions` to minimize interruptions.

---

## Implementation Steps

---

### Task 1: Go server scaffold

**Files:**
- Create: `server/main.go`
- Create: `server/go.mod`
- Create: `server/internal/store/store.go`
- Create: `server/internal/store/store_test.go`

- [x] `go mod init` with module name, add dependencies: `dgraph-io/badger/v4`, `chi` router
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
- [x] implement `PutUpload(r *UploadRecord)` — writes main record + all index entries in one transaction
- [x] implement `GetUpload(id string) (*UploadRecord, error)`
- [x] implement `ListUploadsByDateRange(from, to time.Time, status string, limit int, cursor string) ([]*UploadRecord, string, error)` — scans date index, loads main records; cursor is base64(`<YYYY-MM-DD>/<id>`), used to seek to the exact key `idx/date/YYYY-MM-DD/<id>` before iterating; returns next cursor or empty string
- [x] implement `UpdateStatus(id, status string)` — in a single transaction: load existing record (return ErrNotFound if missing), delete old `idx/status/<old_status>/<id>` key, write updated record, write new `idx/status/<new_status>/<id>` key; never skip the delete step or ghost entries accumulate
- [x] implement `DeleteUpload(id string)` — calls `UpdateStatus(id, "deleted")`
- [x] write table-driven tests for each function (success + not-found + pagination cases); include a test that calls `UpdateStatus` twice with different statuses and asserts only the new status key exists in the index (no ghost)
- [x] run tests — must pass before task 3

---

### Task 3: HTTP Basic Auth middleware

**Files:**
- Create: `server/internal/api/auth.go`
- Create: `server/internal/api/auth_test.go`

- [x] implement `BasicAuth(user, pass string) func(http.Handler) http.Handler` middleware
- [x] returns 401 with `WWW-Authenticate` header on missing/wrong credentials
- [x] reads credentials from env vars `BACKUP_USER` and `BACKUP_PASS`
- [x] write tests: correct credentials pass through, wrong credentials return 401
- [x] run tests — must pass before task 4

---

### Task 4: POST /uploads + GET /uploads handlers

**Files:**
- Create: `server/internal/api/handlers.go`
- Create: `server/internal/api/handlers_test.go`
- Create: `server/internal/uploadbackend/client.go`

- [x] implement `uploadbackend.Client` with `CreateUpload(metadata string) (backendID string, err error)` — calls upload backend `POST /files` with `Upload-Defer-Length: 1`
- [x] implement `POST /uploads` handler: parse `{ localIdentifier, metadata, creationDate, filename, bundleId }`, call `uploadbackend.Client.CreateUpload`, store record in BadgerDB (status: uploading), return `{ id, upload_url: "/uploads/<id>/data" }`
- [x] handle conflict: if record already exists, return the existing record's current status to the Mac app regardless of what that status is — let the client decide: `uploading` → resume; `complete` → skip; `deleted` → skip (only re-upload if user explicitly re-syncs); `backend_lost` → treat as new upload (call DELETE /uploads/:id first, then POST again)
- [x] implement `GET /uploads` handler: parse `from`, `to` (RFC3339), `status`, `limit`, `cursor`; call `ListUploadsByDateRange`; return JSON array + `next_cursor` field; empty result returns `{ "items": [], "next_cursor": "" }`
- [x] implement `GET /uploads/:id` handler: return single record or 404
- [x] write httptest tests for all three handlers
- [x] run tests — must pass before task 5

---

### Task 5: TUS proxy handlers

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`
- Modify: `server/internal/uploadbackend/client.go`

- [x] add `uploadbackend.Client` methods: `GetOffset(backendID string) (int64, error)`, `ForwardPatch(backendID string, r *http.Request) (*http.Response, error)`, `Terminate(backendID string) error`
- [x] implement `HEAD /uploads/:id/data`: look up backend_id from BadgerDB, proxy HEAD to upload backend; if backend returns 404, call `UpdateStatus(id, "backend_lost")` and return `409 Conflict` with body `{"error":"backend_lost"}` — never return the backend 404 raw
- [x] implement `PATCH /uploads/:id/data`: proxy PATCH body + TUS headers to upload backend; if backend returns 404, apply same `backend_lost` transition and return 409
- [x] write httptest tests: correct offset returned; chunk forwarded with correct headers; 404 on unknown record id; backend 404 transitions to backend_lost and returns 409
- [x] run tests — must pass before task 6

---

### Task 6: PATCH /uploads/:id/status + DELETE /uploads/:id handlers

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go`
- Create: `server/internal/filestore/mover.go`

- [x] implement `filestore.MoveFile(src, dst string) error`: tries `os.Rename`, falls back to `copyFile` + `os.Remove` for cross-device moves; before writing, check if `dst` already exists — if so, insert `_<backend_id>` before the extension (e.g. `IMG_0001_abc123.jpg`) to prevent silent overwrite of an existing photo with the same name and date
- [x] implement `PATCH /uploads/:id/status` handler:
  - only accepts `status == "complete"` — any other value returns 400
  - move file from `$STORAGE_PATH/incoming/<backend_id>` to `$STORAGE_PATH/organized/YYYY/MM/DD/<filename>` (with collision suffix if needed); **only if move succeeds**, call `UpdateStatus(complete)` — if move fails, return 500 and leave status as `uploading` so the Mac app can retry
  - return 204; return 404 if record not found
- [x] implement `DELETE /uploads/:id` handler: send TUS Termination DELETE to upload backend (treat backend 404 as success — it may already be gone), then call `UpdateStatus(deleted)`, return 204
- [x] write tests: PATCH complete succeeds; PATCH with status=deleted returns 400; collision suffix applied when dest exists; MoveFile failure leaves status unchanged; DELETE ignores backend 404
- [x] run tests — must pass before task 7

---

### Task 7: Server wiring + Docker setup

**Files:**
- Create: `server/internal/api/router.go`
- Modify: `server/main.go`
- Create: `server/Dockerfile`
- Create: `server/docker-compose.yml`
- Create: `server/Caddyfile`

- [x] wire chi router with BasicAuth middleware on all routes
- [x] read config from env: `BACKUP_USER`, `BACKUP_PASS`, `UPLOAD_BACKEND_URL`, `STORAGE_PATH`, `PORT`
- [x] start a BadgerDB GC goroutine: call `db.RunValueLogGC(0.5)` every 5 minutes in a background goroutine, ignore `badger.ErrNoRewrite` (nothing to reclaim), stop on context cancellation — without this, the value log grows unboundedly as records are updated
- [x] Dockerfile: multi-stage Go build (pure Go, no CGO needed — BadgerDB is pure Go)
- [x] docker-compose: server + upload backend services + Caddy reverse proxy
  - single `STORAGE_PATH` volume mounted into both server and upload backend containers
  - Caddy handles TLS termination (auto-HTTPS via Let's Encrypt or self-signed for LAN)
  - `DOMAIN` env var passed to Caddyfile
- [x] smoke test: `docker-compose up`, curl POST /uploads returns 200
- [x] run unit tests — must pass before task 8

---

### Task 8: macOS app scaffold

**Files:**
- Create: `files-nest-mac/FilesNest.xcodeproj` (via Xcode new project)
- Create: `files-nest-mac/FilesNest/App.swift`
- Create: `files-nest-mac/FilesNest/MenuBarController.swift`

- [x] create new macOS app in Xcode: SwiftUI, menu bar only (LSUIElement = YES in Info.plist)
- [x] add `com.apple.security.personal-information.photos-library` to entitlements
- [x] add `NSPhotoLibraryUsageDescription` to Info.plist
- [x] set up NSStatusItem with a cloud icon in `MenuBarController`
- [x] attach empty SwiftUI popover on click
- [x] verify app shows in menu bar and popover opens/closes
- [x] no automated test for this task — manual verification only

---

### Task 9: PhotoKit protocol wrappers

**Files:**
- Create: `files-nest-mac/FilesNest/PhotoKitProtocols.swift`
- Create: `files-nest-mac/FilesNest/PhotoLibrary.swift`
- Create: `files-nest-mac/FilesNestTests/PhotoLibraryFakes.swift`

- [x] define `AssetProtocol`: localIdentifier, creationDate, mediaType, mediaSubtypes, pixelWidth, pixelHeight, duration, isFavorite, isHidden, burstIdentifier, sourceType, location
- [x] define `AssetLibraryProtocol`: `requestAuthorization() async -> Bool`, `fetchAssets(from:to:) -> [AssetProtocol]`, `resources(for:) -> [AssetResourceProtocol]`
- [x] define `AssetResourceProtocol`: type (photo/video/pairedVideo), uniformTypeIdentifier, originalFilename
- [x] define `AssetResourceManagerProtocol`: `requestData(for:options:dataReceivedHandler:completionHandler:)`
- [x] implement `PhotoLibrary` conforming to `AssetLibraryProtocol` using real PHAsset/PHAssetResource
- [x] implement `PHAsset`, `PHAssetResource`, `PHAssetResourceManager` conformance extensions in a separate file
- [x] create `FakeAsset`, `FakeAssetLibrary`, `FakeAssetResourceManager` in test target for use in Tasks 11–14
- [x] no automated tests for PhotoLibrary itself (requires real Photos library) — manual verification only
- [x] run tests — must pass before task 10

---

### Task 10: Keychain + Settings

**Files:**
- Create: `files-nest-mac/FilesNest/KeychainStore.swift`
- Create: `files-nest-mac/FilesNest/SettingsView.swift`
- Create: `files-nest-mac/FilesNestTests/KeychainStoreTests.swift`

- [x] implement `KeychainStore` with `save(serverURL:user:password:)` and `load() -> Credentials?`
- [x] use `Security.framework` `SecItemAdd` / `SecItemCopyMatching` / `SecItemUpdate`
- [x] build `SettingsView`: server URL field, username, password (SecureField), "Test Connection" button
- [x] Test Connection: HEAD request to server with credentials, show success/failure inline
- [x] on save: write to Keychain, dismiss settings
- [x] show settings automatically on first launch (no credentials in Keychain)
- [x] write unit tests for KeychainStore save + load roundtrip
- [x] run tests — must pass before task 11

---

### Task 11: Server API client

**Files:**
- Create: `files-nest-mac/FilesNest/ServerClient.swift`
- Create: `files-nest-mac/FilesNestTests/ServerClientTests.swift`
- Create: `files-nest-mac/FilesNestTests/ServerClientFakes.swift`

- [x] implement `ServerClient` with `URLSession` + Basic Auth header injected on every request
- [x] `createUpload(localIdentifier:metadata:creationDate:filename:bundleId:) async throws -> UploadRecord` — POST /uploads; caller inspects `record.status` to decide whether to upload, resume, or skip
- [x] `listUploads(from:to:status:limit:cursor:) async throws -> UploadPage` (`UploadPage` has `items` + `nextCursor`; cursor is the opaque composite value from the server)
- [x] `getOffset(id:) async throws -> Int64` — HEAD /uploads/:id/data, reads Upload-Offset header; throws `BackendLostError` on 409 with error=backend_lost
- [x] `uploadChunk(id:data:offset:isLast:) async throws -> Int64` — PATCH /uploads/:id/data; sends `Upload-Offset`, `Content-Type: application/offset+octet-stream`, `Upload-Complete: 1` on last chunk; throws `BackendLostError` on 409
- [x] `updateStatus(id:status:) async throws` — PATCH /uploads/:id/status; only valid value is `"complete"`
- [x] `deleteUpload(id:) async throws` — DELETE /uploads/:id
- [x] `uploadURL` is constructed by the client as `<baseURL>/uploads/<id>/data` — never stored as a relative path; no trailing-slash ambiguity
- [x] define `UploadRecord` Codable struct matching server response; include `status` field typed as `UploadStatus` enum
- [x] define `BackendLostError` so callers can handle re-registration without string-matching
- [x] write tests using `URLProtocol` stub: correct URL and Auth header; correct decoding; cursor pagination; 409 backend_lost throws BackendLostError
- [x] create `FakeServerClient` in `ServerClientFakes.swift` implementing the same protocol as `ServerClient` — used by Tasks 14 and 15 instead of `URLProtocol` stubs
- [x] run tests — must pass before task 12

---

### Task 12: MetadataSerializer

**Files:**
- Create: `files-nest-mac/FilesNest/MetadataSerializer.swift`
- Create: `files-nest-mac/FilesNestTests/MetadataSerializerTests.swift`

- [x] implement `MetadataSerializer.serialize(_ asset: AssetProtocol, resource: AssetResourceProtocol) -> [String: String]` using protocol types (not PHAsset directly)
- [x] include all fields: localIdentifier, dates, location, mediaType, dimensions, duration, isFavorite, isHidden, burstIdentifier, sourceType, filename, bundleId, resourceType
- [x] encode metadata dict as TUS-compatible base64 key-value pairs
- [x] write unit tests using `FakeAsset` from Task 9
- [x] run tests — must pass before task 13

---

### Task 13: ~~TUS client~~ (eliminated)

`TUSClient` is not a separate class. `ServerClient` already implements `getOffset`, `uploadChunk`, and `createUpload` — a wrapper with no added logic creates two sets of fakes and two call stacks for no benefit. `AssetUploader` (Task 14) takes `ServerClient` directly. No files to create; move to Task 14.

---

### Task 14: iCloud streamer (AssetResourceManager → TUS)

**Files:**
- Create: `files-nest-mac/FilesNest/AssetUploader.swift`
- Create: `files-nest-mac/FilesNestTests/AssetUploaderTests.swift`

- [x] implement `AssetUploader.upload(asset:resource:uploadId:startOffset:) async throws` taking `AssetProtocol` and `AssetResourceProtocol`
- [x] inject `AssetResourceManagerProtocol` and `ServerClient` — never reference `PHAssetResourceManager` directly; no TUSClient wrapper
- [x] bridge `requestData` callback API to async/await using `AsyncThrowingStream`: the `dataReceivedHandler` closure appends `Data` chunks to the stream; `completionHandler` finishes or errors it — this avoids holding more than one in-flight callback result at a time
- [x] back-pressure: do not call `requestData` in fire-and-forget fashion; await each `uploadChunk` before the stream can deliver the next callback — use a bounded `AsyncChannel` (capacity 1) or explicit semaphore so iCloud cannot outpace TUS uploads; for a 7GB file at 8MB chunks that means at most 1 chunk buffered, not 875
- [x] accumulate incoming data in a mutable buffer; flush as TUS PATCH exactly when buffer reaches chunk size (configurable, default 8MB); remainder is the final chunk
- [x] on last chunk (stream exhausted), set `isLast: true` to send `Upload-Complete: 1`
- [x] if `startOffset > 0`, discard initial iCloud data up to `startOffset` bytes before buffering — log clearly that iCloud restarted from byte 0 (resume asymmetry, expected)
- [x] propagate errors from both iCloud download side and TUS upload side; if `ServerClient` throws `BackendLostError`, surface it immediately so `SyncCoordinator` can re-register
- [x] write tests using `FakeAssetResourceManager` + `FakeServerClient`: verify chunk sequencing; verify exactly one chunk buffered at a time under back-pressure; verify offset skip on resume; verify `BackendLostError` propagated
- [x] run tests — must pass before task 15

---

### Task 15: SyncCoordinator

**Files:**
- Create: `files-nest-mac/FilesNest/SyncCoordinator.swift`
- Create: `files-nest-mac/FilesNestTests/SyncCoordinatorTests.swift`

- [x] implement `SyncCoordinator.sync(range: DateRange?) async throws` (nil = full sync)
- [x] inject `AssetLibraryProtocol`, `ServerClient`, `AssetUploader` — no `TUSClient` (eliminated)
- [x] fetch PHAssets via `AssetLibraryProtocol`; page through server `GET /uploads` using cursor until `nextCursor` is empty; treat cursor as opaque — pass it back verbatim
- [x] build upload queue: assets in library not in server uploads (status = complete); include both resources of Live Photo pairs; treat the pair as one unit — skip both or upload both
- [x] build delete queue: server uploads not in current library (scoped to range); delete Live Photo pairs together via two sequential `deleteUpload` calls
- [x] process resume: server records with `status=uploading` → `getOffset` → resume `AssetUploader.upload(startOffset:)` from that offset; if `getOffset` throws `BackendLostError`, call `createUpload` to re-register (resets to fresh upload, startOffset=0)
- [x] handle `BackendLostError` during `uploadChunk`: call `deleteUpload` to clean up the lost record, then `createUpload` to re-register, then restart upload from offset 0
- [x] run uploads sequentially (1 at a time); call `updateStatus(complete)` after each
- [x] run deletes after uploads complete
- [x] sync cooldown: persist a `lastSyncStarted: Date` in `UserDefaults`; on launch, if a sync was in progress when the app last quit (stored state = `syncing`), resume from the first incomplete item in the upload queue rather than rebuilding from scratch — avoids re-diffing an entire 10k library on each relaunch after a crash mid-sync
- [x] expose `@Published var progress: SyncProgress` (total, completed, currentFileName)
- [x] write tests with `FakeAssetLibrary` + `FakeServerClient` + `FakeAssetUploader`: upload queue correct; delete queue correct; Live Photo pairs treated as unit; resume uses HEAD offset; BackendLostError triggers re-registration; cursor pagination exhausted; cooldown resumes from correct position
- [x] run tests — must pass before task 16

---

### Task 16: Main UI — popover

**Files:**
- Create: `files-nest-mac/FilesNest/ContentView.swift`
- Modify: `files-nest-mac/FilesNest/MenuBarController.swift`

- [x] build `ContentView` with SwiftUI:
  - "Full Sync" button (prominent)
  - divider + label "Re-sync by range"
  - [Last Month] [Last Quarter] [Last 6 Months] [Last Year] buttons (2×2 grid)
  - progress area: shows current file name + "N / M files" when sync running
  - gear icon → opens SettingsView as sheet
- [x] buttons disabled while sync is running
- [x] wire buttons to `SyncCoordinator.sync(range:)`
- [x] show error alert if sync fails
- [x] manual UI verification — no automated test for this task

---

### Task 17: Verify acceptance criteria

- [x] full sync: fetches all library assets (paginated), uploads missing ones, deletes orphans on server
- [x] range sync: correctly scoped to date range, deletions checked within range
- [x] resume: interrupted upload resumes from last offset on next sync (TUS HEAD, not DB)
- [x] 7GB file: streams without OOM, no temp file on disk
- [x] Live Photo: both JPEG and MOV resources uploaded, linked by bundle_id
- [x] credentials: saved to Keychain, never to UserDefaults or plist
- [x] first launch: settings shown automatically if no credentials
- [x] TLS: server reachable only over HTTPS, Basic Auth credentials not sent in cleartext
- [x] run full test suite: `swift test` in `files-nest-mac/`
- [x] run server tests: `go test ./...` in `server/`

---

### Task 18: Documentation

**Files:**
- Create: `server/README.md`
- Create: `files-nest-mac/README.md`

- [x] server README: setup, env vars (`BACKUP_USER`, `BACKUP_PASS`, `STORAGE_PATH`, `DOMAIN`, `PORT`), docker-compose usage, API reference
- [x] app README: build requirements (Xcode, entitlements), how to configure server URL
- [x] move this plan to `docs/plans/completed/`

---

## Post-Completion

**Manual testing scenarios:**
- Upload a Live Photo — verify both JPEG and MOV appear on server, linked by bundle_id
- Upload a 4K video > 1GB
- Kill the app mid-upload, relaunch, verify resume from correct offset (check server logs for HEAD request)
- Delete a photo from Photos.app, run range sync, verify it disappears from server
- Full sync on a library with 10k+ assets (check memory usage, verify cursor pagination)
- Verify no tusd or backend URLs appear in any user-facing UI or logs

**Deployment:**
- Pin upload backend version in docker-compose
- `DOMAIN` env var drives Caddy auto-HTTPS — point DNS before first `docker-compose up`
- For LAN-only use: replace Caddy with self-signed cert or use Tailscale

**iOS (future):**
- Extract `PhotoKitProtocols`, `MetadataSerializer`, `TUSClient`, `AssetUploader` into a Swift Package
- `SyncCoordinator` and `ServerClient` are reusable with minor adaptation
- `AssetLibraryProtocol` needs PHAuthorizationStatus handling for iOS permission model differences
