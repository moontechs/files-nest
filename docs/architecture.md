# Architecture & Design Decisions

This document captures the core ideas, constraints, and key decisions for the iCloud backup system. It is intended for agents and contributors picking up any part of this project cold.

---

## What this system does

Self-hosted backup of iCloud Photos and Videos from a Mac to a homeserver. The Mac app streams photo/video data directly from iCloud to the server over resumable uploads — no temp files on disk anywhere in the chain.

Two repos:
- `server/` — Go HTTP API + upload state store (this repo, public)
- `files-nest-mac/` — macOS SwiftUI menu bar app (private)

---

## Core constraints that drive every design decision

1. **No temp files *we* own, and no whole-file buffering in *our* memory.**
   `PHAssetResourceManager` streams data via callbacks; the server proxies chunks directly to the
   upload backend. A 7GB video never lands on the server as a completed file before it is moved to
   final storage, and never exists as a file this app created, named, or must clean up.

   **The bytes do touch the Mac's disk, and that is unavoidable.** When an asset is iCloud-only
   (Optimize Mac Storage), its bytes are not on the machine. The only sanctioned way to obtain them
   is `requestData` with `isNetworkAccessAllowed = true`, and PhotoKit materializes the resource
   into the Photos library container in order to serve it. **There is no public PhotoKit API for a
   ranged iCloud fetch** — you cannot ask for "bytes 0–8MB of this asset" and have only that
   fetched. That copy belongs to PhotoKit: it creates it, owns its lifecycle, and evicts it under
   disk pressure. We never create, name, or delete it. This is why the constraint is about
   *ownership*, not about bytes never touching storage.

   What we control is how many copies exist and how often they are made:
   - **Single pass** (`AssetUploader`) — one materialization per asset, not one per chunk.
   - **Sequential processing** — at most one asset materialized at a time, so peak transient cost
     is roughly the largest single asset, not the library.
   - **Free-space pre-flight** (adapter slice) — skip an asset with a typed error rather than
     filling the disk.

   The previous iOS client did not avoid this cost either; it depended on the materialization and,
   per `CODE_AUDIT.md` §5.1, triggered it once per chunk.

2. **No CGO.** The server runs in Docker on a homeserver. No C toolchain available. All dependencies must be pure Go. This is why BadgerDB (pure Go KV store) is used instead of SQLite.

3. **Server is the single source of truth.** The Mac app is stateless — no local database. All sync state lives on the server. This means the app can be reinstalled or moved to a new machine without losing sync state.

4. **Resumable uploads are mandatory.** 7GB+ video files over a home LAN connection will be interrupted. TUS 1.1 protocol is used, with `Upload-Defer-Length: 1` because file size is not known upfront (iCloud streams it lazily).

5. **Upload backend is never exposed externally.** All TUS traffic is proxied through the Go server. Clients only ever talk to the Go API.

---

## Storage: BadgerDB

BadgerDB is a pure-Go embedded KV store (no CGO). It is chosen over SQLite (which requires CGO via mattn/go-sqlite3) for the server's upload state.

### Schema

Main records keyed by `uploads/<SafeID(resourceKey)>`, where `SafeID` is the SHA-256 → base64url
of the resource key (`server/internal/api/ids.go`, `store/index.go` `recordKey`). The raw resource
key lives in the record body, not the BadgerDB key. The `resourceKey` is
`<localIdentifier>#<kind>` (`apple/FilesNestCore/Sources/FilesNestCore/ResourceKey.swift`) so a
Live Photo's JPEG and MOV resources — which share a `localIdentifier` — get distinct keys and
distinct `idx/local/*` index entries instead of colliding.

```json
{
  "id":            "<SafeID of the resource key>",
  "status":        "uploading | complete | deleted | backend_lost",
  "backend_id":    "<upload backend internal ID>",
  "filename":      "IMG_1234.jpg",
  "bundle_id":     "<localIdentifier of Live Photo parent>",
  "creation_date": "2024-03-15T10:30:00Z",
  "created_at":    "2024-03-15T10:30:00Z",
  "updated_at":    "2024-03-15T10:30:00Z"
}
```

### Secondary indexes

Because BadgerDB is a KV store, range queries and status scans require hand-rolled indexes. An `IndexRegistry` pattern is used: each `Index` implementation declares which keys it writes for a given record. All index keys are written/deleted in the same transaction as the main record.

Built-in indexes:
- `DateIndex` — key `idx/date/YYYY-MM-DD/<id>` — enables date-range iteration for `GET /uploads`
- `StatusIndex` — key `idx/status/<status>/<id>` — enables resume scan (find all `uploading` records)

**Critical invariant:** When a record's status changes, `UpdateStatus` must (1) read the old record to get the old status, (2) delete `idx/status/<old_status>/<id>`, (3) write `idx/status/<new_status>/<id>`, and (4) write the updated record — all in one transaction. Skipping step 2 leaves a ghost key that corrupts every status scan.

### BadgerDB GC

BadgerDB never reclaims value log space automatically. A background goroutine calls `db.RunValueLogGC(0.5)` every 5 minutes. Without this, disk usage grows unboundedly.

### Cursor pagination

`GET /uploads` uses cursor pagination over the DateIndex. The cursor encodes **both** date and id: `base64(<YYYY-MM-DD>/<localIdentifier>)`. A localIdentifier alone cannot seek a position in a date-ordered index scan.

---

## Status lifecycle

```
uploading ──── PATCH /status complete ──→ complete
    │
    └── server detects backend 404 ──→ backend_lost
                                            │
                                    Mac calls POST /uploads
                                    (re-registers, resets to uploading)

uploading / complete / backend_lost ──── DELETE /uploads/:id ──→ deleted
```

- `backend_lost` is set by the **server** when a HEAD or PATCH proxy call returns 404 from the upload backend (e.g. after tusd restart). The Mac app recovers by POSTing to `/uploads` again with the same `localIdentifier` — the server re-registers a fresh tusd upload against the existing record; no DELETE is required.
- `PATCH /uploads/:id/status` only accepts `complete`. Any other value returns 400. Deletion goes through `DELETE /uploads/:id` exclusively — using PATCH for deletion would skip the TUS Termination call and leak disk space in the upload backend.
- Records are created with `uploading` immediately on POST — no `pending` state. Crashes never leave orphaned pre-creation records.

---

## TUS proxy flow

```
POST /uploads
  → client provides { localIdentifier, metadata, creationDate, filename, bundleId }
  → server calls upload backend POST /files (Upload-Defer-Length: 1)
  → BadgerDB record created (status: uploading, backend_id stored)
  → response: { id, status, ... }  ← Mac builds URL as <baseURL>/uploads/<id>/data

PATCH /uploads/:id/data  (chunk)
  → server looks up backend_id, forwards to upload backend
  → copies Upload-Offset response header back to Mac

HEAD /uploads/:id/data  (resume offset)
  → proxied to upload backend
  → returns Upload-Offset header

HEAD or PATCH → backend 404
  → server sets status = backend_lost, returns 409 {"error":"backend_lost"}
  → Mac: delete record, POST /uploads to re-register

PATCH /uploads/:id/status { status: "complete" }
  → server moves file: incoming/<backend_id> → organized/YYYY/MM/DD/<stem>_<id><ext>
  → _<id> (the record's stable safe ID) always appended before the extension;
    destination is deterministic, no dest-exists collision check
  → only if move succeeds: UpdateStatus(complete)
  → if move fails: 500, status stays uploading (retryable)

DELETE /uploads/:id
  → TUS Termination to upload backend (ignore backend 404)
  → UpdateStatus(deleted)
```

The Mac app never constructs a URL from server-returned paths — it assembles `<baseURL>/uploads/<id>/data` itself to avoid relative-path / trailing-slash bugs.

---

## POST /uploads idempotency

On conflict (record already exists), the server returns the existing record with its current status. The Mac app decides:

| existing status | action |
|---|---|
| `uploading` | resume (call HEAD for offset, continue PATCH) |
| `complete` | skip |
| `deleted` | skip (unless user explicitly re-syncing) |
| `backend_lost` | delete record, POST /uploads again to re-register |

---

## File organization

```
$STORAGE_PATH/
  incoming/         ← upload backend writes partial files here
  organized/
    YYYY/
      MM/
        DD/
          IMG_1234_<id>.jpg   ← _<id>: the upload record's stable safe ID,
                                always appended before the extension
```

Every completed file in `organized/` carries a deterministic `_<id>` suffix —
the upload record's safe server ID, a SHA-256/base64url hash of the asset's
`localIdentifier` (see the `SafeID` derivation in the BadgerDB Schema section
above). The suffix is appended unconditionally, **not** only on collision, so
the final path is fully deterministic from (creation date, filename, id)
alone: it never depends on current disk state or on which tusd backend
attempt wrote the bytes. Because no two live records can share an `id` (the
store dedups by `localIdentifier`, so per-record IDs are unique by
construction) and the organized-tree mutex forecloses the planning-to-move
window, no collision check or fallback exists. A foreign file already at the
computed path — manual intervention in `organized/`, inconsistent disk
state, or a bug elsewhere — is overwritten by the move; the server
WARN-logs it as a safety net first. The scheme is prospective only: files
organized before this change keep their existing names indefinitely, so old
and new naming coexist on disk with no migration.

Cross-device moves: `os.Rename` first; fall back to copy+delete if source and dest are on different filesystems.

---

## Mac app: streaming without temp files

`PHAssetResourceManager.requestData` delivers data via a callback (`dataReceivedHandler`). The app:

1. Bridges the callback API to an **async sink**: `AssetDataSource.read(assetID:from:into:)` takes a
   `@Sendable (Data) async throws -> Void`, and the source must fully await the sink before
   consuming the next callback. Backpressure is therefore structural — capacity-1 is guaranteed by
   the signature, not by coordination code.
2. **PATCHes each blob straight through** as it arrives. There is no accumulation buffer, so there
   is no buffer to mismanage.
3. Holds exactly **one blob back** (look-ahead), so the final PATCH can carry a resolved
   `Upload-Length` — a blob is only known to be the last one once the source completes. (Not
   `Upload-Complete: 1`; the server resolves length via the `Upload-Length` header, see
   `handlers.go:616`.)

Peak memory is therefore **two blobs, independent of asset size**, enforced by a test gate that
counts live blobs exactly (`MemoryGateTests`).

**Why not `AsyncThrowingStream`?** It has no producer backpressure: while the consumer uploads
chunk 1, the stream buffers chunks 2..N — 7GB resident for a 7GB file. Its `bufferingPolicy` does
not help, because `.bufferingNewest(1)` and `.bufferingOldest(1)` **drop** elements rather than
throttling the producer, and dropping file bytes is silent corruption. A stream plus a
`DispatchSemaphore` was tried in the previous client: it serialized correctly but still grew
linearly, because `Data.append` with `prefix`/`dropFirst` on one long-lived buffer kept
copy-on-write backing storage alive. The async sink removes the buffer entirely rather than
managing it. See `docs/design/20260724-assetuploader.md` §2.

**iCloud resume asymmetry:** `requestData` cannot resume mid-file. If interrupted at 3GB of a 7GB video, iCloud restarts from byte 0 even if the TUS offset is at 3GB. The adapter discards the initial bytes up to `startOffset` using `OffsetSkip` and logs this clearly — it is expected behavior, not a bug. `OffsetSkip` lives in core, tested, rather than in each adapter: that discard is the exact `dropFirst` shape implicated in the previous client's leak, and it returns freshly-copied `Data` so the skipped buffer is not kept alive by an aliasing slice.

**Does PhotoKit stream during download or materialize first, and does it honour backpressure?** Measured with `MeasurementRunner`/`MeasurementView` (macOS app) against real assets on 2026-07-25.

- **Confirmed: PhotoKit materializes first, does not stream during download.** Wiring
  `PHAssetResourceRequestOptions.progressHandler` made it unambiguous: on a large iCloud-only video
  the download progress climbed (0.000 → 0.045 …) while `dataReceivedHandler` delivered **zero
  bytes** and our footprint stayed flat at ~28 MB. Download progress therefore cannot be inferred
  from delivered bytes; `progressHandler` is the only signal for the download phase. `maxConcurrentDeliver`
  was `1` on every run (serial delivery). This matches §3's "no ranged iCloud fetch" assumption.
- **Memory guarantee: confirmed.** Our process footprint stayed ~28–34 MB regardless of asset size (204 MB and 120 MB streamed) — the capacity-1 ceiling holds on real PhotoKit data.
- **Instrument limitations found:** (1) volume free space (`.volumeAvailableCapacity` and `…ForImportantUsage`) is **blind to PhotoKit's materialization** — it stayed flat even during an active multi-minute iCloud fetch, so disk-delta via volume capacity cannot quantify materialization; (2) the measurement is **one-shot per asset** — measuring caches the asset locally, so re-runs on the same asset read from disk and are vacuous; (3) "longest video" auto-pick selects multi-GB assets whose materialization exceeds a reasonable wait.
- **Still open for a fully quantified answer:** a medium (~150–300 MB) iCloud-only asset that completes, measured with a materialization probe that isn't volume-free-space (e.g. the Photos container size with non-sandboxed access, or `photolibraryd`/`cloudd` activity). The qualitative answer (materialize-first) is already indicated; quantification is deferred.

---

## Mac app: client layer

`ServerClient` is the single HTTP client. It handles:
- Basic Auth header injection on every request
- All server API calls: `createUpload`, `listUploads`, `getUpload`, `offset(forUploadID:)` (TUS HEAD), `patchData(uploadID:offset:data:finalLength:)` (TUS PATCH), `markComplete(uploadID:)`, `deleteUpload`
- Throws typed `ServerClientError` so callers branch without string-matching. The server overloads 409, so the error body string is the discriminator: `.backendLost`, `.offsetConflict`, `.alreadyCompleted`, `.alreadyDeleted`, `.notUploading`, `.uploadIncomplete`. Cancellation propagates as `CancellationError`, never as `.transport`.

There is no separate TUSClient wrapper — that would duplicate the call stack and require two sets of fakes for no benefit. `AssetUploader` and `SyncCoordinator` take `ServerClient` directly.

---

## Mac app: sync logic

`SyncCoordinator.sync(range:)`:

1. Fetch PHAssets from Photos library for the range (or all, for full sync).
2. Page through `GET /uploads` using cursor until `next_cursor` is empty.
3. Diff: assets missing on server → upload queue; server records not in library → delete queue.
4. Live Photos: JPEG and MOV resources are two separate upload records sharing `bundle_id`, addressed by distinct `ResourceKey`s (`<localIdentifier>#photo`, `<localIdentifier>#pairedVideo`). They are treated as a pair — both uploaded or both deleted as a unit. (`SyncCoordinator` must build records with these keys; until it does, the two resources still collide on `SafeID` and the `idx/local` index — see the PhotosAssetDataSource design §9/§11.)
5. Upload queue processed sequentially. Records with `status=uploading` are resumed from HEAD offset.
6. `ServerClientError.backendLost` during resume or upload: call `createUpload` to re-register — the server resets the `backend_lost` record back to `uploading` in place (same id) with a fresh backend via `ReRegister` (`handlers.go:258`) — then upload from offset 0. **No `deleteUpload` first:** it is redundant (the lost backend is already gone) and a `DELETE` would leave a `deleted` tombstone that the planner skips, stranding a still-present asset if recovery were interrupted. A mid-recovery failure instead leaves a resumable `uploading` record. See `docs/design/20260726-synccoordinator.md` §6.
7. Delete queue processed after all uploads complete.
8. Only `lastSyncStarted` is persisted to `UserDefaults` (via `SyncStateStore`). No queue position is stored: because the server is the single source of truth, a crash-restart re-runs the diff — completed items already read `complete` on the server and are skipped, and `uploading` items resume from the HEAD offset — so resume is emergent, not stored. See `docs/design/20260726-synccoordinator.md` §3 (decision 3).

---

## Auth

HTTP Basic Auth on all server endpoints. Credentials stored in macOS Keychain (`KeychainStore`). Never in UserDefaults or plists. TLS termination via Caddy (Let's Encrypt for public domains, self-signed / Tailscale for LAN).

---

## localIdentifier stability

`PHAsset.localIdentifier` is stable within a device but resets when a user restores or migrates to a new iPhone. After migration, every asset appears new — a full re-upload occurs. This is expected and logged. Content-hash deduplication to avoid re-uploads is a future concern, out of scope now.

---

## Testing approach

- **Server:** Table-driven Go unit tests + `httptest` for handler integration tests. Tests must pass before moving to the next task.
- **Mac app:** XCTest unit tests for `SyncCoordinator`, `AssetUploader`, `ServerClient`, `MetadataSerializer`. UI tested manually.
- **Fakes:** `FakeAsset`, `FakeAssetLibrary`, `FakeAssetResourceManager` (Task 9); `FakeServerClient` (Task 11). All fakes live in the test target, never in the app target.
- No mocking of BadgerDB — tests use a real BadgerDB instance in a temp directory.
