# Design: Count library on start — exact at-rest Pending with a counting state

**Date:** 2026-07-29
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (`SyncStatus.counting`, `SyncSummary.pending`, `Assessment`, `AssetLibrary.resources` progress hook, `LiveSyncEngine` assess pass, `SyncStateStore` cache) · `apple/macos/FilesNest` (`FilesNestApp` assess/cache closures, `PanelView` counting hero + Pending tile)
**Depends on:**
- `docs/design/20260728-panel-stats-failed-detail.md` (`SyncSummary`, `summaryStream`, panel tiles, merged #9)
- `docs/design/20260726-photos-library-real-syncnow.md` (`LiveSyncEngine` serial command loop, `PhotosAssetLibrary`, `SyncPlanner`, `SyncCoordinator`, merged #7/#10)

---

## 1. Purpose

The panel shows an honest "Pending" only *during* a sync; at rest it shows "—" because an exact backlog needs a per-resource library scan (the expensive ~63s `PHAssetResource.assetResources(for:)` enumeration over ~46k assets → ~70k resources). An earlier *estimate* (`libraryAssetCount − backedUp`) was reverted in #10 because it conflated units (assets vs. resources — a Live Photo is 1 asset but 2 resources).

This slice does the **real** count: on launch, run the per-resource scan in the background behind a **determinate "Counting N of 46,039" state** (so the panel never looks frozen — the pain that motivated this), then show the **exact** at-rest Pending computed by a `SyncPlanner` dry-run. Warm launches show the **last cached** counts instantly and recount in the background.

---

## 2. Scope

**In this slice:**

- `apple/FilesNestCore`:
  - `SyncStatus.counting(done:total:)` (§4.1).
  - `SyncSummary.pending: Int?` (§4.2).
  - `Assessment` value type, `Codable` for caching (§4.3).
  - `AssetLibrary.resources(in:onProgress:)` progress hook (§4.4).
  - `SyncStateStore.loadAssessment()/saveAssessment(_:)` (§4.5).
  - `LiveSyncEngine` assess pass: `assess`/`cachedAssessment` seams, `.counting`/`.assessed` commands, assess child, `doStart` wiring, `finishSync` deriving pending from the report (§5).
- `apple/macos/FilesNest`:
  - `FilesNestApp` `assess` + `cachedAssessment` closures; `UserDefaultsSyncStateStore` cache impl (§6).
  - `PanelView` `.counting` hero + at-rest Pending tile (§7).

**Deferred (out of scope) — tracked so they are not lost (§9):**

1. Determinate progress on the **sync's own** scan phase (keeps "Scanning library…").
2. Live idle updates — `PHPhotoLibraryChangeObserver` + auto-sync scheduler (the next slice).
3. Avoiding the **double scan** when Sync Now immediately follows launch counting.

---

## 3. Decisions (with forks considered)

1. **Exact Pending via a `SyncPlanner` dry-run, not a subtraction.** `SyncPlanner.plan(library:server:range:).uploads.count` is *exactly* what the next Sync Now would upload — correct even when local and server diverge (deletes, partial uploads). The subtraction `local − backedUp` is approximate and can misstate. Cost is near-zero incrementally: launch already pages the full server list to count the Backed-up tile, so assess reuses that list instead of discarding it.

2. **`counting` is a first-class `SyncStatus` case, not a side stream.** Counting is a distinct top-level state the hero renders determinately; modeling it as a case reuses the serial command loop's child + generation-gating machinery (cancellable, superseded by pause/syncNow/sign-out) exactly like a sync. The cost is churn in every `SyncStatus` switch; that is honest and localized.

3. **Warm launch shows cached counts instantly, then recounts.** The last `Assessment` is cached in `SyncStateStore`; on launch the engine seeds the summary from it (panel never blank), enters `.counting`, and refreshes when the scan finishes. First-ever launch (no cache) shows "—" during counting.

4. **`pending` lives inside `SyncSummary` (`Int?`), not a separate published field.** The three tile numbers (backedUp, pending, failed) are one snapshot; keeping them together is simplest. `nil` = never counted → panel shows "—".

5. **Progress denominator is the asset count (cheap, known upfront); the result is resources.** `PHAsset.fetchAssets().count` is cheap, so "Counting `done` of `total`" tracks assets enumerated against the known asset total. The *resulting* counts (backedUp, pending, resourceTotal) are per-resource. The bar measures scan progress; the tiles measure resources.

6. **`finishSync` derives pending from the report — no post-sync re-scan.** After an `.all` sync, everything uploaded except failures, so `pending = report.failed.count` and `backedUp = report.skipped + report.uploaded.count` (as today). Avoids a second ~63s scan. The cache is rewritten only by assess-on-launch; warm launch always recounts, so post-sync cache staleness self-heals.

---

## 4. Types (Core)

### 4.1 `SyncStatus` (+ `counting`)

```swift
public enum SyncStatus: Sendable, Equatable {
    case signedOut
    case counting(done: Int, total: Int)   // NEW — launch scan in progress
    case watching(lastSync: Date?)
    case syncing(SyncProgress)
    case paused(pending: Int)
    case error(message: String)
}
```

### 4.2 `SyncSummary` (+ `pending`)

```swift
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int
    public let pending: Int?          // NEW — exact at-rest backlog; nil = never counted
    public let failed: [FailedItem]
    public init(backedUp: Int, pending: Int?, failed: [FailedItem]) { … }

    public static let empty = SyncSummary(backedUp: 0, pending: nil, failed: [])
}
```

All existing `SyncSummary(backedUp:failed:)` call sites (engine, StubSyncEngine, tests) gain `pending:`.

### 4.3 `Assessment`

```swift
public struct Assessment: Sendable, Equatable, Codable {
    public let backedUp: Int         // server records with status == .complete
    public let pending: Int          // SyncPlanner.plan(...).uploads.count
    public let resourceTotal: Int    // library resources enumerated
    public init(backedUp: Int, pending: Int, resourceTotal: Int) { … }
}
```

`Codable` so `SyncStateStore` can persist it as JSON. `resourceTotal` is not shown in this slice's tiles but is cached for future use (e.g. a "N of M backed up" display) and cheap to keep.

### 4.4 `AssetLibrary` progress hook

```swift
public protocol AssetLibrary: Sendable {
    func resources(in range: SyncRange,
                   onProgress: (@Sendable (_ done: Int, _ total: Int) -> Void)?) async throws -> [AssetResource]
}
```

`PhotosAssetLibrary`: fetch `total = assets.count` up front, then call `onProgress(index + 1, total)` inside `enumerateObjects` (throttled — see §8). `onProgress` defaults to `nil`; the existing `SyncCoordinator` call passes `nil` (its scan phase stays "Scanning library…" — deferral §9.1). Fakes in tests conform to the new signature.

### 4.5 `SyncStateStore` cache

```swift
public protocol SyncStateStore: Sendable {
    func loadLastSyncStarted() -> Date?
    func saveLastSyncStarted(_ date: Date)
    func loadAssessment() -> Assessment?     // NEW
    func saveAssessment(_ assessment: Assessment)   // NEW
}
```

`UserDefaultsSyncStateStore` encodes/decodes `Assessment` as JSON under a new key. `InMemorySyncStateStore` (tests) holds it in a property.

---

## 5. `LiveSyncEngine` — the assess pass

Replaces the `refreshBackedUp: (() async throws -> Int)?` seam with two:

```swift
assess: (@Sendable (_ onProgress: @Sendable (Int, Int) -> Void) async throws -> Assessment)?
cachedAssessment: (@Sendable () -> Assessment?)?
```

New commands (private): `case counting(gen: UInt64, done: Int, total: Int)` and `case assessFinished(gen: UInt64, Assessment?)` (`nil` = the scan failed → just leave `.counting`, keep the seeded/cached summary). New consumer state: `assessChild: Task<Void, Never>?` (mirrors `syncChild`).

**`doStart()` signed-in idle branch** (the `!isSyncingStatus` reconcile path):
1. Bump generation; clear `lastProgress` (kept from #10).
2. Seed the summary from `cachedAssessment()` if present: `setSummary(SyncSummary(backedUp: c.backedUp, pending: c.pending, failed: currentSummary.failed))`.
3. `setStatus(.counting(done: 0, total: 0))`.
4. Spawn `assessChild`: run `assess { done, total in submit(.counting(gen:gen,done:done,total:total)) }`; on success `submit(.assessFinished(gen:gen, result))`; on `CancellationError` do nothing (supersession set the terminal status); on any other error `submit(.assessFinished(gen:gen, nil))` (log; the handler leaves `.counting` for `.watching`, keeping the seeded/"—" pending).

**Handlers (generation-gated):**
- `.counting(gen,done,total)` → if `gen == generation` `setStatus(.counting(done:done,total:total))`.
- `.assessFinished(gen,a)` → if `gen == generation`: `assessChild = nil`; if `let a` `setSummary(SyncSummary(backedUp: a.backedUp, pending: a.pending, failed: currentSummary.failed))`; `setStatus(.watching(lastSync: lastSync))`.

**Supersession:** `doPause`, `doResume`, `doSyncNow`, and the sign-out branch of `doStart` cancel `assessChild` (alongside `syncChild`) and bump the generation, so a stale `.counting`/`.assessed` is dropped by the gate. `doSyncNow` while `.counting` cancels the count and starts the sync. `doStart` while already `.counting` is a no-op re-entry guard (treat `.counting` like a busy state — do not restart the assess child; see §5.1).

**`finishSync(report)`** (no re-scan): `setSummary(SyncSummary(backedUp: report.skipped + report.uploaded.count, pending: report.failed.count, failed: report.failed))` then `.watching`; no assess scheduled. (Removes the old `scheduleBackedUpRefresh`.)

### 5.1 Busy-state guard

`doStart`'s reconcile branch currently runs when `!isSyncingStatus`. Extend the "busy, don't restart" set to include `.counting`: a second `start()` arriving while a count is already in flight must not spawn a duplicate assess child. Concretely, guard on `!isSyncingStatus && !isCountingStatus` before entering the count. (A `start()` that represents a credentials change should still supersede — but `restart()` bumps via the sign-out/sign-in cycle; a same-state re-entry is the case to guard.)

---

## 6. App wiring

`FilesNestApp` builds the two closures (Core stays PhotoKit-free):

```swift
assess: { onProgress in
    let library = PhotosAssetLibrary()
    let scan = try await library.resources(in: .all, onProgress: onProgress)   // Counting…
    guard let url = urlStore.load(),
          (try await credStore.basicCredentials()) != nil else {
        // signed out: no server side; treat everything as pending
        let a = Assessment(backedUp: 0, pending: scan.count, resourceTotal: scan.count)
        stateStore.saveAssessment(a); return a
    }
    let client = ServerClient(baseURL: url, credentials: credStore)
    var server: [UploadRecord] = []
    var cursor: String? = nil
    repeat { let page = try await client.listUploads(cursor: cursor); server += page.items; cursor = page.nextCursor } while cursor != nil
    let plan = SyncPlanner.plan(library: scan, server: server, range: .all)
    let a = Assessment(backedUp: server.filter { $0.status == .complete }.count,
                       pending: plan.uploads.count,
                       resourceTotal: scan.count)
    stateStore.saveAssessment(a)
    return a
},
cachedAssessment: { stateStore.loadAssessment() }
```

`UserDefaultsSyncStateStore` implements `load/saveAssessment` via `JSONEncoder`/`Decoder`.

## 7. `PanelView`

- **Hero (`.counting`):** determinate ring `fraction = total > 0 ? done/total : 0`; while `total == 0` show the indeterminate `ProgressView` (as scanning does today). Title "Counting…"; subtitle `total > 0 ? "\(done.formatted()) of \(total.formatted())" : "Scanning library…"`.
- **Pending tile at rest:** `summary.pending.map { "\($0)" } ?? "—"`; `.orange` when `> 0`. During `.syncing`/`.paused` keep the live status-derived pending (unchanged).
- Handle `.counting` in every `switch` (`glyph`, `title`, `subtitle`, `ringFraction`, `ringColor`, plus `isSignedOut`/derived helpers). `.counting` is signed-in, so tiles render numbers (seeded/cached or "—").
- The `swiftui-expert` skill is invoked when authoring the hero (state flow + macOS menu-bar rendering).

---

## 8. Testing

Headless `swift test` for Core; the panel is manual-verify.

### 8.1 Core
- **assess drives counting → assessed:** an injected `assess` that emits progress `(3,10),(10,10)` then returns `Assessment(backedUp:5,pending:7,resourceTotal:12)` yields observed `.counting(3,10)` then `.watching` with `summary.pending == 7` and `backedUp == 5`.
- **cache seeds before counting:** `cachedAssessment` returning `Assessment(9,4,20)` → the summary shows `backedUp 9 / pending 4` *before* the assess result arrives.
- **supersession:** pause / syncNow / sign-out during `.counting` cancels the assess child; a late `.assessFinished` for the old generation is dropped (summary unchanged / superseded).
- **no-restart guard:** a second `start()` while `.counting` does not spawn a duplicate assess (assert the injected assess runs once — `Counter`).
- **finishSync from report:** after a `perform` returning `skipped:3, uploaded:[A], failed:[F]`, summary is `backedUp 4 / pending 1` with no assess invocation.
- **planner-diff correctness:** an integration-style test with a fake library + fake server where the plan has N uploads asserts `assessed pending == N`.
- **throttling:** `PhotosAssetLibrary` progress is emitted at most every K assets (or on the last) so a 46k enumeration does not post 46k commands — asserted at the app layer or by a throttle helper unit test.

### 8.2 App (manual verification)
Cold launch → "Counting…" ticks `done` toward the asset total, then Pending shows the exact backlog. Warm launch → cached Backed up/Pending appear instantly, then refresh. Pause/Sync Now during counting interrupts it. Documented in a `docs/plans/…-verification.md` checklist.

---

## 9. Deferred / out of scope (tracked)

1. **Determinate progress on the sync's own scan.** `resources(in:onProgress:)` now supports it; `SyncCoordinator` passes `nil` for now to keep "Scanning library…" distinct from launch "Counting…". Cheap to wire later.
2. **Live idle updates:** `PHPhotoLibraryChangeObserver` to keep the count fresh while the app sits open, + an auto-sync scheduler on change. This is the next slice; it also removes warm-launch cache staleness.
3. **Double-scan avoidance:** ~~Sync Now right after launch counting re-scans~~ **Done in this slice** via `CachingAssetLibrary` — a TTL-memoized (`60s`) `AssetLibrary` decorator shared by the assess pass and `SyncCoordinator`, so a Sync Now within the window reuses the launch count's scan. The TTL is a stopgap: the observer slice (§9.2) replaces it with change-based invalidation (`CachingAssetLibrary.invalidate()` already exists for that). Remaining gap: a photo taken within the TTL window won't appear until it expires.
4. **`resourceTotal` display** (e.g. "63,201 of 70,444 backed up") — cached but not shown yet.

---

## 10. Open items (resolve during implementation)

1. **Progress throttling constant (§8).** Pick K (e.g. every 250 assets, plus a final emit) so the hero animates without flooding the command stream. Tune during manual verification.
2. **assess failure UX (§5).** On auth-denied/network error, fall back to `.watching` and keep cached/"—" pending (logged). Confirm this is preferable to surfacing `.error` for a *count* failure (a sync failure still surfaces `.error` normally).
