# Design: PhotoKit AssetLibrary adapter + one-shot real Sync Now

**Date:** 2026-07-26
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (new `LiveSyncEngine` + progress hook on `SyncCoordinator`) · `apple/macos/FilesNest` (PhotoKit `PhotosAssetLibrary`, composition-root rewire)
**Depends on:**
- `docs/design/20260726-synccoordinator.md` (`SyncCoordinator`, `SyncPlanner`, `AssetLibrary`, `AssetResource`, `SyncRange`, merged #4)
- `docs/design/20260724-photosassetdatasource.md` (`ResourceKey`, `ResourceKind`, `PhotosAssetDataSource`, merged #3)
- `docs/design/20260726-menubar-shell.md` (`SyncEngine` seam, `StubSyncEngine`, `AppModel`, `FilesNestApp`, `ServerURLStore`, merged #6)

---

## 1. Purpose

Turn **Sync Now** from a fake progress animation into a real one-shot reconcile of the Mac's
Photos library against the server. This slice supplies the two things every prior slice deferred as
"the untestable PhotoKit residue + its driver":

1. **`PhotosAssetLibrary`** — the PhotoKit **enumeration** adapter conforming to `AssetLibrary`
   (enumerate `PHAsset` by range → `[AssetResource]`). It is to enumeration what the already-shipped
   `PhotosAssetDataSource` is to byte-reading.
2. **`LiveSyncEngine`** — a real `SyncEngine` whose `syncNow()` drives
   `SyncCoordinator.sync(.all)` against the live library + server and publishes real `SyncStatus`.
   It **replaces `StubSyncEngine`** in the app's composition root with no UI change.

The `SyncCoordinator` executor, `SyncPlanner` diff, `AssetUploader`, and `ServerClient` already
exist and are exhaustively tested (#1–#4). This slice adds the enumeration adapter, a small progress
hook, and the status-choreography engine that ties Sync Now to the coordinator.

---

## 2. Scope

**In this slice:**

- `apple/FilesNestCore`:
  - `SyncCoordinator.sync(range:onProgress:)` — additive, backward-compatible progress hook (§4).
  - `LiveSyncEngine` — real `SyncEngine`, testable via an injected `perform` closure (§5).
- `apple/macos/FilesNest`:
  - `PhotosAssetLibrary` — PhotoKit `AssetLibrary` conformance (§3).
  - `FilesNestApp` composition root — build a `LiveSyncEngine` in place of `StubSyncEngine` (§6).

**Deferred (unchanged from the standing sequencing):**

- **Continuous watching / auto-sync** — no `PHPhotoLibraryChangeObserver`, no scheduler. `start()`,
  `pause()`, `resume()` stay minimal; `syncNow()` is the only real action this slice. The engine is
  named `LiveSyncEngine` (not `ContinuousSyncEngine`) to make that boundary explicit.
- **Incremental-range policy** — Sync Now uses `.all` (full reconcile). Choosing `.dates` bounds from
  `lastSyncStarted` is the scheduler slice's concern (`20260726-synccoordinator.md` §9). The adapter
  still *supports* `.dates` because the protocol requires it.
- **Free-space pre-flight guard** — needs a resource size PhotoKit does not expose publicly
  (`20260724-photosassetdatasource.md` §9); `bytesRemaining` stays `nil` (§4).
- **`StubSyncEngine`** is retained (it is Core, `swift test`-covered, and useful for previews/tests);
  it is simply no longer wired into the app.

---

## 3. `PhotosAssetLibrary` — the PhotoKit enumeration adapter (app target)

```swift
nonisolated struct PhotosAssetLibrary: AssetLibrary {
    func resources(in range: SyncRange) async throws -> [AssetResource]
}
```

Mirrors `PhotosAssetDataSource`: a `nonisolated struct` in the app target, the only new PhotoKit
surface. Its byte-reading sibling already exists; this is its enumeration counterpart.

### 3.1 Authorization

On first use it ensures Photos access via `PHPhotoLibrary.requestAuthorization(for: .readWrite)`
(full-library **read** requires `.readWrite`; there is no read-only level on current macOS).
`.denied` / `.restricted` → **throw** a descriptive error. The engine maps a thrown enumeration
error to `.error(message:)` (§5) — authorization failure is a whole-sync failure, not a per-item one.

### 3.2 Fetch

- `.all` → `PHAsset.fetchAssets(with: options)` with no media-type filter (photos, videos, audio),
  sorted by `creationDate` ascending.
- `.dates(r)` → same, with `options.predicate = NSPredicate("creationDate >= %@ AND creationDate <= %@", r.lowerBound, r.upperBound)`.

### 3.3 Resource mapping (per asset)

For each `PHAsset`, iterate `PHAssetResource.assetResources(for: asset)` and, for each resource whose
`.type` maps to our **closed** `ResourceKind` set, emit one `AssetResource`:

```swift
AssetResource(
    key: ResourceKey(localIdentifier: asset.localIdentifier, kind: mappedKind),
    filename: resource.originalFilename,
    creationDate: asset.creationDate ?? <fallback>,   // see §3.4
    bundleID: isLivePhoto ? asset.localIdentifier : nil)
```

- **Kind map** is the reverse of `PhotosAssetDataSource.mapKind` — a `PHAssetResourceType →
  ResourceKind?` switch returning `nil` for types we do not address (`.adjustmentData`,
  `.adjustmentBasePhoto`, `.adjustmentBaseVideo`, `.fullSizeVideo`, etc.). Unmapped resources are
  **skipped**, not errored — they are not uploadable resources.
- **Live Photo pairing.** `isLivePhoto = asset.mediaSubtypes.contains(.photoLive)`. For a Live Photo,
  both the `#photo` and `#pairedVideo` resources get `bundleID = asset.localIdentifier`; standalone
  assets get `nil`. This is exactly what `SyncPlanner` §5.4 expects: Live-Photo "both or neither" is
  **emergent** from enumeration yielding two keyed resources that share `bundleID` — the planner never
  special-cases it.

### 3.4 `creationDate` fallback

`PHAsset.creationDate` is optional. `AssetResource.creationDate` is non-optional and drives upload
ordering and the server's date index. A nil creation date is not expected in practice; if it occurs,
fall back to `.distantPast` so the asset still syncs (sorted first) rather than being dropped. This is
defensive and documented, matching the coordinator's "nil creationDate is defensive" stance
(`20260726-synccoordinator.md` §5.3).

---

## 4. `SyncCoordinator` progress hook (small change to a shipped Core unit)

Additive and backward-compatible — the default no-op means every existing coordinator test and caller
is untouched:

```swift
public func sync(range: SyncRange,
                 onProgress: @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport
```

Immediately **before** processing upload item `i` (0-based) of `N = plan.uploads.count`, it calls:

```swift
onProgress(SyncProgress(completed: i,
                        total: N,
                        currentItemName: item.resource.filename,
                        bytesRemaining: nil))
```

- `completed: i` before item `i` reads as "`i` done, now starting `<filename>`"; the panel's ring
  shows `i / N`.
- `bytesRemaining` is `nil` — PhotoKit does not publicly expose resource size (see §2 deferred /
  `20260724-photosassetdatasource.md` §9). The panel already renders `bytesRemaining == nil`.
- **Deletes are not counted** in the ring: they are quick metadata operations and `plan.uploads.count`
  is the user-meaningful "files to back up" total. (A future slice may add a delete phase indicator.)
- No progress callback fires when `N == 0` (nothing to upload); the engine still transitions
  `.syncing` → `.watching` (§5), so an already-in-sync library shows a brief spin then idle.

The hook is a `@Sendable` closure, not an `AsyncStream`, so it inherits the coordinator's structured
`async` context and needs no buffering policy.

---

## 5. `LiveSyncEngine` — status choreography (Core, testable)

Pure `SyncStatus` state management around an **injected** sync operation. It imports no PhotoKit and
requires **no change to the `SyncStatus` enum**.

```swift
public final class LiveSyncEngine: SyncEngine, @unchecked Sendable {
    public init(credentials: any CredentialStore,
                state: any SyncStateStore,
                perform: @escaping @Sendable (SyncRange, @Sendable (SyncProgress) -> Void) async throws -> SyncReport,
                now: @escaping @Sendable () -> Date = { Date() })
}
```

- **Streaming.** Reuses `StubSyncEngine`'s proven `NSLock` + `[UUID: continuation]` `statusStream()`
  pattern verbatim: a fresh stream yields the current status first, then every change.
- **`start()`** — `let creds = try? await credentials.basicCredentials()`; set `.signedOut` if nil,
  else `.watching(lastSync: state.loadLastSyncStarted())`.
- **`syncNow()`**:
  1. If signed out or paused → no-op (leave status as-is).
  2. **Re-entrancy guard:** if already `.syncing`, ignore (a second Sync Now during a run is a no-op).
  3. Set `.syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil))`.
  4. `let report = try await perform(.all) { progress in self.set(.syncing(progress)) }`.
  5. **Success → `.watching(lastSync: state.loadLastSyncStarted())`**, even when `report.failed` is
     non-empty. Per-item failures are logged (and available to a future "failed items" UI); a
     mostly-successful backup is not shown as broken (resolved fork — matches the coordinator's
     skip-and-continue philosophy, `20260726-synccoordinator.md` §3 decision 4).
  6. **A whole-sync throw** (auth denied, enumeration failure, server paging failure, "not signed in"
     from the wiring) → `.error(message:)`. `CancellationError` → return to `.watching` (a cancelled
     Sync Now is not an error). No `CancellationError` is expected this slice (nothing cancels a
     one-shot run yet), but it is handled defensively.
- **`pause()` / `resume()`** — flip `.paused(pending: 0)` ⇄ `.watching(lastSync:)`; `syncNow()` is
  ignored while paused. Minimal, no queue exists yet. (`pending: 0` because there is no pre-scan
  count until the coordinator plans, which happens inside `perform`.)

**`lastSync` semantics.** `SyncStateStore` persists `lastSyncStarted` (written by the coordinator at
the *start* of `sync`). The engine reads it back for `.watching(lastSync:)`, so "last sync" reflects
the start of the most recent run. This is the single source of truth (§3 decision 3 of the
coordinator design); stamping a separate completion time would reintroduce a second source. Acceptable
and documented.

### 5.1 The `perform` seam

`SyncCoordinator` is a concrete `struct` requiring a concrete `ServerClient` (the standing "one
client, no protocol" decision). Rather than couple the engine to how that graph is built, the engine
takes a `perform` closure. This cleanly splits two concerns:

- **Engine unit (this slice's Core tests):** given a fake `perform`, does it emit the correct
  `SyncStatus` sequence and honor the guards? Fully testable with **no** `MockURLProtocol`.
- **Wiring (app target):** composes the real coordinator into `perform` (§6).

End-to-end coordinator behavior is already covered by `SyncCoordinatorTests` (#4), so the engine tests
deliberately do not re-exercise it.

---

## 6. App wiring (`FilesNestApp`)

Replace `StubSyncEngine(credentials:)` in the composition root with a `LiveSyncEngine`. The `perform`
closure — the only place PhotoKit adapters are named — reads the **current** URL + credentials **at
sync time** so a Settings change takes effect on the next Sync Now:

```swift
let urlStore  = UserDefaultsServerURLStore(defaults: defaults)
let credStore = KeychainStore()
let stateStore = UserDefaultsSyncStateStore(defaults: defaults)

let engine = LiveSyncEngine(
    credentials: credStore,
    state: stateStore,
    perform: { range, onProgress in
        guard let url = urlStore.load(),
              (try await credStore.basicCredentials()) != nil else {
            throw NotSignedInError()            // → engine shows .signedOut / .error
        }
        let client   = ServerClient(baseURL: url, credentials: credStore)
        let uploader = AssetUploader(client: client, source: PhotosAssetDataSource())
        let coordinator = SyncCoordinator(client: client,
                                          library: PhotosAssetLibrary(),
                                          uploader: uploader,
                                          state: stateStore)
        return try await coordinator.sync(range: range, onProgress: onProgress)
    })
```

`AppModel`, `PanelView`, and the `onSaved → restart()` hook are unchanged — they observe the
`SyncEngine` seam, which is identical. `restart()` re-runs `start()`, re-reconciling signed-in/out
after Settings save.

Confirm during implementation whether `UserDefaultsSyncStateStore` and a "not signed in" error type
already exist from #4/#6; if the error type does not, add a tiny app-local `struct NotSignedInError:
Error`.

---

## 7. Testing

Headless `swift test` for all Core work; PhotoKit residue is manual-verify. Every test is
failure-injected and **watched to fail first** (`20260724-photosassetdatasource.md` §8.2 discipline).

### 7.1 `LiveSyncEngine` (Core, fake `perform`)

- Signed-out: `syncNow()` is a no-op; status stays `.signedOut`.
- Happy path: `syncNow()` emits `.syncing(0/0)` → `.syncing` per progress callback → `.watching`.
- Progress translation: a `perform` that invokes `onProgress` N times yields N `.syncing` statuses in
  order with the same `completed/total/currentItemName`.
- Partial failure: `perform` returns a `SyncReport` with non-empty `failed` → engine still ends at
  `.watching` (not `.error`).
- Whole-sync error: `perform` throws → `.error(message:)`.
- Re-entrancy: a second `syncNow()` while `.syncing` is ignored (assert via a `perform` that blocks on
  a signal).
- Paused: `pause()` → `.paused`; `syncNow()` ignored; `resume()` → `.watching`.
- `start()` reconciles `.signedOut` vs `.watching(lastSync:)` from creds + state (injected `now`).
- `statusStream()` fresh subscriber receives current status first (mirrors the `StubSyncEngine` test).

### 7.2 `SyncCoordinator` progress hook (Core)

- `onProgress` fires exactly `plan.uploads.count` times, in plan order (`creationDate` asc then key),
  each with correct `completed` (0-based), `total`, and `currentItemName`; `bytesRemaining == nil`.
- `N == 0` (nothing to upload) → `onProgress` never called; `sync` still returns the report.
- The default-argument call site (no `onProgress`) still compiles and behaves identically — existing
  tests are the regression guard.

### 7.3 `PhotosAssetLibrary` (manual verification)

Untestable PhotoKit residue → a checklist plan (like `20260726-menubar-shell-verification.md`):
build + run, grant Photos access, Sync Now against a live server, confirm real files upload with a
moving ring and correct current-item names, and that a Live Photo produces two server records sharing
`bundleID`. Denying Photos access surfaces `.error`.

---

## 8. Error handling summary

| Condition | Handling |
|---|---|
| Not signed in (no URL or no creds) at Sync Now | `perform` throws → engine `.error` (or no-op if already `.signedOut` from `start`) |
| Photos authorization denied/restricted | adapter throws → engine `.error(message:)` |
| Enumeration or server paging fails | throws from `perform` → engine `.error(message:)` |
| Per-item upload/delete failures | collected in `SyncReport.failed` by the coordinator; engine ends at `.watching`, failures logged |
| `backend_lost` mid-upload | auto-recovered once inside `SyncCoordinator` (unchanged, #4) |
| `CancellationError` | engine returns to `.watching`; not expected this slice (nothing cancels yet) |
| Second `syncNow()` while syncing | ignored (re-entrancy guard) |

---

## 9. Deferred / out of scope

- Continuous watching (`PHPhotoLibraryChangeObserver`) and any auto-sync scheduler — the next slice.
- Incremental `.dates` range policy from `lastSyncStarted`.
- Free-space pre-flight guard (needs a size PhotoKit does not publicly expose).
- A "failed items" list UI for `SyncReport.failed` (data is available; rendering is future).
- Cancelling an in-flight Sync Now from the UI.
- iOS enumeration adapter / shell.

---

## 10. Open items (resolved during implementation)

1. **Existing types check.** Confirm `UserDefaultsSyncStateStore` exists (from #4) and whether a
   "not signed in" error type is already defined; add a tiny app-local one only if needed (§6).
2. **`PHAssetResourceType` completeness.** Pin the exact reverse kind-map while writing the adapter;
   the closed `ResourceKind` set (§3.3) is authoritative — anything outside it is skipped.
