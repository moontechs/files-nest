# Design: SyncCoordinator

**Date:** 2026-07-26
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (new sync layer + fakes; no app-target work this slice)
**Depends on:**
- `docs/design/20260723-serverclient.md` (`ServerClient`, merged `9eb5feb`)
- `docs/design/20260724-assetuploader.md` (`AssetUploader`, `AssetDataSource`, merged `51c168b`)
- `docs/design/20260724-photosassetdatasource.md` (`ResourceKey`, merged `13ed638`) — §5/§9/§11.4 deliberately left the Live Photo collision latent for this slice to close.
- `docs/architecture.md` "Mac app: sync logic" (authoritative)

---

## 1. Purpose

`SyncCoordinator` reconciles the Mac's Photos library against the server. It is the component
`architecture.md` names in step 3 of the sync logic, and the one that finally **builds upload
records keyed by `ResourceKey.encoded`** — closing the latent Live Photo `SafeID` collision that
`PhotosAssetDataSource` design §5/§11.4 defined the key for but explicitly did not fix.

It composes the two existing seams directly — `ServerClient` (HTTP) and `AssetUploader`
(capacity-1 streaming) — per the standing "no separate TUSClient wrapper" decision
(`architecture.md` "client layer").

This slice is **Core-only and headless-testable**. The real PhotoKit enumeration adapter, Xcode
wiring, `KeychainStore`, and `MenuBarExtra` are later slices (§9).

---

## 2. Scope

**In this slice** (all in `apple/FilesNestCore`, all reachable by `swift test`):

- `AssetResource`, `AssetLibrary` (enumeration seam)
- `SyncRange`
- `SyncStateStore` protocol + `InMemorySyncStateStore` + `UserDefaultsSyncStateStore`
- `SyncPlanner` (pure diff) + `SyncPlan` / `PlannedUpload` / `PlannedDelete`
- `SyncCoordinator` (executor) + `SyncReport` / `FailedItem`
- Test fakes: `FakeAssetLibrary`, `FakeServerClient` (see §7 for the client-faking approach)
- `architecture.md` correction: the persisted "position" is **emergent from re-diff**, not stored (§5)

**Deferred (unchanged from the handover sequencing):**

- PhotoKit `AssetLibrary` adapter (enumerate `PHAsset` by date range, map `PHAssetResource` →
  `ResourceKey`) + Xcode wiring
- `KeychainStore`, `MenuBarExtra` shell
- Free-space pre-flight guard (`20260724-photosassetdatasource.md` §9)
- Incremental-range **policy** (deciding `.dates` bounds from `lastSyncStarted`) — the coordinator
  accepts a range; who chooses it is the scheduler/UI slice's concern.

---

## 3. Decisions (with the forks that were considered)

1. **Enumeration seam is a batch metadata list, not an `AsyncSequence`.** The diff must materialize a
   key `Set` for *both* sides (upload tests library-key ∉ server; delete tests server-key ∉
   library), so streaming buys no memory win while adding backpressure machinery. Metadata is
   strings only — a 200k-resource library is ~tens of MB of small structs, and the capacity-1
   **byte** guarantee is untouched (bytes still flow through `AssetDataSource`/`AssetUploader`).
   *Accepted trade-off: sync metadata memory scales with resource count.*

2. **Diff is a pure function (`SyncPlanner`) separate from execution.** All status-branching lives in
   a pure, fake-free unit. `SyncCoordinator` only executes the plan. This is the
   pure-testable-core / thin-adapter split the prior slices used.

3. **Persist only `lastSyncStarted`; crash-resume is emergent from re-diff.** The architecture is
   server-is-single-source-of-truth / stateless-client. A persisted queue *position* would be a
   second source of truth that can drift. After a crash, re-running the diff naturally skips
   completed items (they read `complete` on the server) and resumes `uploading` ones from the HEAD
   offset — so "resume from the first incomplete item, not from scratch" holds **without** storing a
   position. This is a deliberate deviation from a literal reading of `architecture.md` step 8, and
   the doc is corrected to match.

4. **Failure policy: skip-and-continue with a structured report.** One unreadable asset or transient
   transport error must not halt a multi-hundred-item backup. Per-item non-recoverable failures are
   collected into `SyncReport.failed` (key + reason) for the later UI to render as a list; the sync
   proceeds. `backendLost` is **always** auto-recovered and is not a failure. `CancellationError`
   stops the sync promptly and propagates (it is not an item failure).

---

## 4. Types

All types are `public`, `Sendable`, and (where they carry only value data) `Equatable`.

```swift
// The enumeration seam — the one thing FakeAssetLibrary replaces.
public struct AssetResource: Sendable, Equatable {
    public let key: ResourceKey        // <localIdentifier>#<kind> — the diff key
    public let filename: String
    public let creationDate: Date
    public let bundleID: String?       // Live Photo parent localIdentifier; nil if standalone
}

public protocol AssetLibrary: Sendable {
    /// All resources in `range`, one entry per uploadable resource (a Live Photo
    /// yields two: `#photo` and `#pairedVideo`, sharing `bundleID`).
    func resources(in range: SyncRange) async throws -> [AssetResource]
}

public enum SyncRange: Sendable, Equatable {
    case all
    case dates(ClosedRange<Date>)
}

public protocol SyncStateStore: Sendable {
    func loadLastSyncStarted() -> Date?
    func saveLastSyncStarted(_ date: Date)
}
```

`UserDefaultsSyncStateStore` wraps an **injected** `UserDefaults` suite (not `.standard`) so it is
testable without polluting real defaults; stores the date as an ISO-8601 string under one key.
`InMemorySyncStateStore` is an actor-free `final class` behind a lock (it is `Sendable` and mutated
from the coordinator).

### 4.1 Plan

```swift
public struct SyncPlan: Sendable, Equatable {
    public let uploads: [PlannedUpload]   // ordered: creationDate asc, then key.encoded
    public let deletes: [PlannedDelete]
    public let skipped: Int
}

public struct PlannedUpload: Sendable, Equatable {
    public let resource: AssetResource
    public let mode: Mode
    public enum Mode: Sendable, Equatable {
        case create                     // no server record for this key
        case resume(uploadID: String)   // server status=uploading → uploader resumes from HEAD offset
        case recover(uploadID: String)  // server status=backend_lost → delete→create→upload from 0
    }
}

public struct PlannedDelete: Sendable, Equatable {
    public let uploadID: String
    public let key: ResourceKey
}
```

### 4.2 Report

```swift
public struct SyncReport: Sendable, Equatable {
    public let uploaded: [ResourceKey]
    public let deleted: [ResourceKey]
    public let failed: [FailedItem]
    public let skipped: Int
}

public struct FailedItem: Sendable, Equatable {
    public let key: ResourceKey
    public let reason: String   // human-readable; the UI slice renders these as a list
}
```

---

## 5. `SyncPlanner` — the pure diff

```swift
public enum SyncPlanner {
    public static func plan(library: [AssetResource],
                            server: [UploadRecord],
                            range: SyncRange) -> SyncPlan
}
```

The server record for a resource is found by matching `UploadRecord.localIdentifier` against
`AssetResource.key.encoded`. This is correct because the client sends `key.encoded` as the
`local_identifier` field on `createUpload`, the server hashes it into the `SafeID` and round-trips
it back unchanged (`server/internal/api/handlers.go:205,228,296`). **The bare `PHAsset`
localIdentifier never appears as a key** — only the encoded resource key does.

### 5.1 Upload/skip decisions (iterate library resources)

| Server record for this key | Action |
|---|---|
| none | `.create` |
| `uploading` | `.resume(uploadID:)` |
| `completing` | skip — transient server move-in-progress; reverts to `uploading` if the move fails, and next sync resumes it |
| `complete` | skip |
| `deleted` | skip — matches the `architecture.md` POST-idempotency table ("skip unless explicitly re-syncing"; explicit re-sync is out of scope) |
| `backend_lost` | `.recover(uploadID:)` |

`SyncPlan.skipped` counts **library resources that require no action** (the `completing` / `complete`
/ `deleted` rows above) — i.e. "already in sync". Server-side skips (a `deleted` or `completing`
record absent from the library) are not counted; they represent no user-visible work.

### 5.2 Delete decisions (iterate server records)

A server record is a delete candidate when its key is **absent** from the library key set, with two
exceptions:

- `deleted` → skip (already gone; never re-delete)
- `completing` → skip (mid-move; let it settle, next sync handles it)

`uploading`, `complete`, and `backend_lost` records absent from the library → `PlannedDelete`.

### 5.3 Range-scoped deletes (correctness, not an optimization)

`ServerClient.listUploads` pages the **entire** server index — it has no range filter. Without
scoping, `sync(.dates(January))` enumerates only January's library and would see February's server
records as "not in the library" and **delete every February backup**.

Therefore, for `range == .dates(r)`, the planner considers only server records whose parsed
`creationDate ∈ r` as delete candidates (the library side is already `r`-scoped, making both sides
consistent). For `.all`, every server record is a candidate.

**Nil / unparseable `creationDate`:** in a `.dates` sync such a record cannot be located in the
window, so it is **never** a delete candidate (conservative — never delete what we can't place). In
`.all` it is still deleted if absent from the library. (`UploadRecord.creationDate` is typed
optional because the Go wire field is; the server always sets it from the required
`CreateUploadRequest.creationDate`, so nil is not expected in practice — this rule is defensive.)

The upload side is unaffected by range scoping: it is driven by the library list, which the
`AssetLibrary` already scoped to `range`.

### 5.4 Live Photos are handled by enumeration, not by the planner

"Both uploaded or both deleted as a unit" is **emergent**: `AssetLibrary` yields both `#photo` and
`#pairedVideo` for a Live Photo (shared `bundleID`), and each is diffed independently. When the
asset is new, both keys are absent from the server → two `.create`s. When the asset is deleted from
the library, both keys are absent from the library → two `PlannedDelete`s. No cross-record
transaction is needed, and the planner never special-cases `bundleID`.

---

## 6. `SyncCoordinator` — the executor

```swift
public struct SyncCoordinator: Sendable {
    public init(client: ServerClient,
                library: any AssetLibrary,
                uploader: AssetUploader,
                state: any SyncStateStore,
                now: @Sendable () -> Date = { Date() })   // injectable clock for deterministic tests

    public func sync(range: SyncRange) async throws -> SyncReport
}
```

`sync(range:)`:

1. `state.saveLastSyncStarted(now())` — recorded at the **start**, matching the field name, so a
   crashed sync still records that it began.
2. `library = try await library.resources(in: range)`.
3. Page `client.listUploads(cursor:)` from `nil`, following `nextCursor` until it is `nil`,
   accumulating all `UploadRecord`s.
4. `plan = SyncPlanner.plan(library: library, server: records, range: range)`.
5. **Uploads, sequentially, in plan order.** For each `PlannedUpload`, via one `uploadOne` helper:
   - `.create` / `.recover` → `let rec = try await client.createUpload(request)` where `request =
     CreateUploadRequest(localIdentifier: key.encoded, filename:, creationDate:, bundleID:)`. For
     `.recover`, the server ReRegisters the existing `backend_lost` record in place (same id, fresh
     backend, reset to `uploading`) rather than creating a duplicate (`handlers.go:258`).
   - `.resume(uploadID)` → use the existing `uploadID` (no create).
   - Then `try await uploader.upload(assetID: key.encoded, uploadID: id)`. **`AssetUploader.upload`
     already PATCHes every blob and calls `markComplete` in `finish()`** — the coordinator does
     **not** call `markComplete` separately.
   - On success → append `key` to `report.uploaded`.
   - **Error handling** around the whole create+upload+complete flow:
     - `ServerClientError.backendLost` (can surface from the HEAD offset, a data PATCH, or the
       status PATCH — all inside `uploader.upload`) → recover **once**: `createUpload` →
       `uploader.upload`. **No `deleteUpload`** (post-review correction, §6.3). If recovery itself
       throws → record `failed`.
     - `is CancellationError` → **rethrow** (stop the sync).
     - any other error → append `FailedItem(key, reason)`; continue to the next item.
6. **Deletes, sequentially, after all uploads.** For each `PlannedDelete`: `client.deleteUpload(id:)`;
   success → `report.deleted`; other error → `failed`, continue; `CancellationError` → rethrow.
7. Return the assembled `SyncReport` (`skipped` carried from the plan).

### 6.1 Ordering and cancellation

- Upload order is `creationDate` ascending then `key.encoded` (deterministic; oldest first).
- Cancellation is checked cooperatively between items (the `await`s already are cancellation points);
  a cancelled sync throws `CancellationError` and does **not** return a partial report — the caller
  re-runs and re-diff resumes.

### 6.2 Why re-diff is a safe resume

Re-running `sync` after any interruption is idempotent: `.create` only fires for keys with no server
record; a partially-uploaded item is `uploading` on the server → `.resume` from HEAD; a finished one
is `complete` → skipped. No client-side position is needed or kept (§3, decision 3).

### 6.3 Recovery does not delete (post-review correction)

The original design (and `architecture.md` step 6) had recovery call `deleteUpload` → `createUpload`
→ upload. The Codex slice-completion review surfaced that this can **strand a still-present asset**:
the `deleteUpload` leaves a `deleted` tombstone, and if recovery is interrupted before the re-create
succeeds, the next sync sees that tombstone for a library asset that is still present. The planner
skips `deleted` records (§5.1, matching the architecture idempotency table) — so the asset is never
re-uploaded.

Verified against the server: `GET /uploads` **returns `deleted` records** (the `DateIndex` key is
status-agnostic and survives `UpdateStatus`, `store/index.go:78` / `store/uploads.go:271`), so the
planner really would see the tombstone. And `POST /uploads` on a `backend_lost` **or** `deleted`
record calls `ReRegister`, resetting it to `uploading` in place with a fresh backend
(`handlers.go:258`).

The fix keeps the desired "skip `deleted` unless explicitly re-syncing" behavior **unchanged** and
removes the hazard at its source: **recovery no longer deletes.** `backend_lost` → `createUpload`
(the server ReRegisters in place) → upload from 0. The `deleteUpload` was redundant anyway — the lost
backend is already gone, and `ReRegister` handles `backend_lost` directly. A mid-recovery failure now
leaves a resumable `uploading` record instead of a `deleted` tombstone, so re-diff resume (§6.2) is
genuinely non-stranding. Proven by `recoveryFailureLeavesResumableRecordForNextSync` (two syncs) and
the no-`DELETE` assertion in `backendLostRecordIsRecoveredProactively`.

The `FakeServer` harness was correspondingly corrected to model `SafeID`-keyed create + `ReRegister`
(the prior version minted a fresh id on every create, so recovery tests asserted a *new* id when the
real server reuses it).

---

## 7. Testing

All tests are headless `swift test`. Every test is **failure-injected and watched to fail first**
(`20260724-photosassetdatasource.md` §8.2 discipline — the reader slice produced ~4 vacuous tests
caught only this way).

### 7.1 The client-faking approach (the one open implementation risk)

`ServerClient` is a concrete `struct` and `AssetUploader`/`SyncCoordinator` take it concretely, by
the "one concrete client, no protocol" decision. To exercise `SyncCoordinator` end to end, the
**primary plan is to drive a real `ServerClient` through `MockURLProtocol`** (the existing seam
`ServerClientNetworkTests` already uses) — scripting HTTP responses for `listUploads`, `createUpload`,
HEAD offset, PATCH data, status, and `deleteUpload`, including 409 `backend_lost` bodies to trigger
recovery.

**Risk & fallback:** if scripting the ordered `delete→create→upload-from-0` recovery sequence through
`MockURLProtocol` proves too awkward to keep tests legible, the fallback is a **minimal internal
client protocol** that `ServerClient` conforms to and `FakeServerClient` implements — introduced only
if needed, and kept internal so the public "one client" surface is unchanged. This choice is
confirmed during the first coordinator test, not deferred to review.

### 7.2 `SyncPlanner` (pure — no fakes)

Table tests for every §5.1 row and every §5.2 case, plus:
- Range scoping: a February server record **survives** a `.dates(January)` sync (not deleted).
- Nil-`creationDate` record: not deleted under `.dates`, deleted under `.all` when absent.
- Live Photo: one asset → two `.create`s; deleted asset → two `PlannedDelete`s.
- Upload ordering is `creationDate` asc then key.
- `skipped` count is accurate.

### 7.3 `SyncCoordinator`

- `.create` happy path: createUpload → data PATCHes → markComplete (via uploader) → `uploaded`.
- `.resume`: existing `uploading` record, uploader resumes from a non-zero HEAD offset.
- `.recover`: `backend_lost` record → create (server ReRegisters in place) → upload from 0.
- `backendLost` injected at HEAD, at a data PATCH, and at the status PATCH — each recovers once.
- Recovery that fails again → item lands in `failed`, sync continues.
- Skip-and-continue: one item throws a transport error; later items still upload; report is accurate.
- Cancellation mid-queue: throws `CancellationError`, stops promptly, no partial report.
- Delete queue runs strictly after all uploads.
- `lastSyncStarted` persisted at start (assert via `InMemorySyncStateStore` + injected `now`).

### 7.4 `UserDefaultsSyncStateStore`

Round-trips a date through an injected in-memory `UserDefaults(suiteName:)`; absent key → `nil`.

---

## 8. Error handling summary

| Condition | Handling |
|---|---|
| `ServerClientError.backendLost` (resume/upload/complete) | auto-recover once: create (server ReRegisters in place) → upload from 0; no delete (§6.3) |
| recovery itself fails | `FailedItem`, continue |
| transport / source / other `ServerClientError` | `FailedItem`, continue |
| `CancellationError` | rethrow, stop sync, no partial report |
| `listUploads` / enumeration throws | propagates from `sync` (no plan to execute) |

Enumeration and the initial server paging are **pre-plan**: if either throws, `sync` throws (there
is nothing to partially report). Per-item errors during execution are the only ones collected.

---

## 9. Deferred / out of scope

- PhotoKit `AssetLibrary` adapter + `ResourceKey.kind → PHAssetResourceType` enumeration + Xcode
  wiring — the untestable residue, deferred to the app/UI slice.
- `KeychainStore` (Basic Auth creds; `swift-security-expert`), then `MenuBarExtra` shell.
- Free-space pre-flight guard (needs a resource size PhotoKit does not expose publicly).
- Incremental-range policy (choosing `.dates` bounds from `lastSyncStarted`).
- Explicit re-sync of `deleted` records (planner currently skips them).
- Server-side range filter on `GET /uploads` (a future optimization; until then a ranged sync pages
  the full list — correct, just not minimal).

---

## 10. Open items

1. **Client-faking approach (§7.1).** `MockURLProtocol` vs. a minimal internal client protocol —
   resolved during the first coordinator test.
2. **`completing` visibility in `GET /uploads`.** The planner is robust whether or not `completing`
   (or `deleted`) records appear in the list; confirm the list's actual status coverage while
   writing planner tests, and pin it with a test fixture.
3. **Asset/offset divergence** (`20260724-assetuploader.md` §11.3) is unchanged — the uploader trusts
   the server HEAD offset and cannot detect an asset that shrank between runs. Accepted, carried
   forward.
