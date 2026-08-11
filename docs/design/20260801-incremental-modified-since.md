# Incremental sync range (`.modifiedSince`) — Apple clients #15

Status: approved design — ready for implementation plan.
Date: 2026-08-01.

## 1. Goal

Stop re-scanning the entire photo library on every library change. After the
continuous-watching slice (#13/#14), a debounced change runs a full `.all`
count + sync — a full PhotoKit enumeration (`PHAssetResource.assetResources(for:)`
per asset, several seconds on a ~70k library) even to back up a single new photo.

This slice makes **change-triggered** counts and syncs **incremental**: they scan
only items modified since the last sync, keyed on `modificationDate`. Full `.all`
syncs still run on launch, restart, and manual Sync Now — re-establishing the
baseline and reconciling deletions every launch.

## 2. Cadence (settled during brainstorming)

| Trigger | Range |
|---|---|
| Launch | `.all` (baseline + deletion reconcile + uploads) |
| Restart (Settings save) | `.all` |
| Manual Sync Now | `.all` |
| Library change (`.libraryChanged`) | `.modifiedSince(lastSyncStarted − margin)` |

Because launch is **always** `.all`, the full-library baseline is re-established
every launch, so incremental change-syncs chain safely from there.

## 3. Range model

Replace the prepared-but-unused, creationDate-based case:

```swift
public enum SyncRange: Sendable, Equatable {
    case all
    case modifiedSince(Date)   // upload-only; PhotoKit predicate on modificationDate >= bound
}
```

- `.dates(ClosedRange<Date>)` is **removed**. It is constructed nowhere in the app
  (only in tests) and its creationDate semantics are exactly the bug this slice
  avoids: an imported photo taken in 2020 has an old `creationDate` and would fall
  outside a `[lastSyncStarted … now]` creationDate window, so it would never back
  up. `modificationDate` is ≈ import/edit time, so imports and edits are caught.
- `.modifiedSince` is **upload-only**: a windowed local scan cannot reliably detect
  deletions (the deleted asset is simply absent, with no modification signal), so
  incremental never deletes. Deletions reconcile on the next `.all` (every launch).

### Why not window the server round-trip too

The dominant cost is the **local** PhotoKit enumeration; that is what this slice
windows. `SyncPlanner` still needs the server list to diff, and `listUploads` has
no date/id filter, so **server pagination stays full**. A server-side query is a
separate (server) slice — see §10. This is an honest scope boundary, not an
oversight.

## 4. `SyncPlanner`

```swift
static func plan(library: [AssetResource], server: [UploadRecord], range: SyncRange) -> SyncPlan
```

- `.all`: unchanged — uploads (iterate library, diff against server) **and** deletes
  (server records absent from the library).
- `.modifiedSince`: uploads exactly as for `.all` (iterate the windowed library,
  diff against the full server list), **`deletes = []`**.

The upload side is identical for both ranges; only the delete side is gated. Remove
the old `.dates` creationDate delete-scoping block.

## 5. `PhotosAssetLibrary` (app target)

```swift
if case .modifiedSince(let d) = range {
    options.predicate = NSPredicate(format: "modificationDate >= %@", d as NSDate)
}
```

`AssetResource.creationDate` still carries `creationDate` (used for on-disk
organization `user/year/month/day`); the predicate keys on `modificationDate`
independently. No upper bound (open-ended: everything modified since `d`).

## 6. Engine (`LiveSyncEngine`)

The range is chosen by trigger and threaded through the count → sync chain so the
count and its chained sync use the **same** range (the sync uploads exactly what
the count saw, and `CachingAssetLibrary` yields a cache hit between them).

- Replace consumer-only `autoSyncAfterCount: Bool` with **`autoSyncRange: SyncRange?`**
  (nil = do not chain a sync; non-nil = chain `doSyncNow` with this range).
- `startIdleCount(range: SyncRange, autoSync: Bool)` — the count uses `range`; if
  `autoSync`, `autoSyncRange = range`, else nil.
- `.assessFinished`:

  ```swift
  let range = autoSyncRange
  autoSyncRange = nil
  if let range, (a?.pending ?? 0) > 0 { doSyncNow(range: range) }
  else { drainPendingChangeIfAny() }
  ```

- `doSyncNow(range: SyncRange)` — passes `range` to `perform`, and records
  `currentSyncRange = range` for `finishSync`.
- Range selection:
  - `doStart` idle branch → `startIdleCount(range: .all, autoSync: !isPausedStatus)`
    (paused-restart still clears `pendingLibraryChange` and does not upload — #14).
  - `.libraryChanged` idle branch → `startIdleCount(range: incrementalRange(), autoSync: true)`.
  - `drainPendingChangeIfAny()` → `startIdleCount(range: incrementalRange(), autoSync: true)`
    (a coalesced change drains to an incremental cycle).
  - `syncNow()` (manual) → `doSyncNow(range: .all)`.
  - `incrementalRange()`:

    ```swift
    private static let incrementalMargin: TimeInterval = 60   // clock-skew safety; re-scan slightly wider is a no-op

    private func incrementalRange() -> SyncRange {
        guard let last = state.loadLastSyncStarted() else { return .all }
        return .modifiedSince(last.addingTimeInterval(-Self.incrementalMargin))
    }
    ```

The range is computed **once** per cycle at `startIdleCount` time (from the
pre-cycle `lastSyncStarted`) and carried to the chained sync via `autoSyncRange`,
so the count and the sync scan the identical window (cache hit between them). The
running sync's `saveLastSyncStarted(now())` only advances the bound for the *next*
cycle.

`autoSyncRange` is reset in `doStart`'s signed-out branch (mirrors the old
`autoSyncAfterCount` reset).

## 7. Summary correctness

The count pages the **full server list**, so `backedUp` = whole-library complete
count regardless of scan range. `finishSync` sources `backedUp` range-aware:

```swift
let backedUp: Int
switch currentSyncRange {
case .all:            backedUp = report.skipped + report.uploaded.count   // unchanged
case .modifiedSince:  backedUp = syncBaseBackedUp + report.uploaded.count   // baseline + new
}
```

- `.all` keeps the existing report-derived formula (existing tests intact).
- `.modifiedSince`: `report.skipped` counts only window-skips, so it would collapse
  `backedUp`; instead use `syncBaseBackedUp` (the whole-library count captured at
  sync start from the preceding count) + newly uploaded. This equals the live-climb's
  end value (`syncBaseBackedUp + progress.completed`), so the number is continuous.

Invariant this relies on: an incremental sync is **always preceded by a count** in
the same cycle (a `.libraryChanged` or drain always goes through `startIdleCount`
→ count → chained `doSyncNow`), and that count sets `backedUp` from the full server
list. So `syncBaseBackedUp` is whole-library-correct at incremental sync start.
Manual Sync Now is `.all` (report-derived), so it needs no preceding count.

`pending` = report upload-failures (unchanged). A windowed count's `pending =
plan.uploads.count` over the window, which equals whole-library pending at steady
state (older items are already backed up by the last `.all`).

## 8. Composition root (`FilesNestApp`)

The `assess` closure becomes range-aware:

```swift
assess: { range, progress in
    let scan = try await library.resources(in: range, onProgress: progress.report)
    // … full server pagination (unchanged) …
    let plan = SyncPlanner.plan(library: scan, server: records, range: range)
    let resourceTotal = (range == .all)
        ? scan.count
        : (stateStore.loadAssessment()?.resourceTotal ?? scan.count)   // don't clobber full total with a window size
    let a = Assessment(backedUp: records.filter { $0.status == .complete }.count,
                       pending: plan.uploads.count,
                       resourceTotal: resourceTotal)
    stateStore.saveAssessment(a); return a
}
```

The `perform` closure already forwards `range` to `coordinator.sync(range:)` — no
change there. `SyncCoordinator` is already range-agnostic (forwards `range` to the
library and planner, saves `lastSyncStarted`) — no change.

## 9. Safety net

Anything an incremental cycle misses — a local deletion, or a `modificationDate`
quirk on some asset — is reconciled by the next launch `.all`. Worst case is a
delay until next launch, never lost data. This is what makes `modificationDate`
windowing safe to ship.

## 10. Testing

Core, TDD:

- `SyncPlanner`: `.modifiedSince` produces the same uploads as `.all` for the same
  inputs but **empty deletes**; `.all` delete behavior unchanged.
- `LiveSyncEngine`: `incrementalRange()` derives `.modifiedSince(lastSyncStarted −
  margin)` (and `.all` when `lastSyncStarted` is nil); a `.libraryChanged` cycle
  runs the count + sync with a `.modifiedSince` range while launch/restart/Sync Now
  use `.all` (assert via a `perform`/`assess` that records the range it received);
  incremental `finishSync` yields `backedUp = syncBaseBackedUp + uploaded` (does not
  collapse), `.all` `finishSync` unchanged.
- `SyncCoordinator`: forwards a `.modifiedSince` range to the library and planner
  (extend existing range-forwarding tests).
- Migrate `.dates(...)` occurrences in `SyncPlannerTests`, `SyncCoordinatorTests`,
  `SyncValueTypeTests`, `CachingAssetLibraryTests` to `.modifiedSince(...)`.

App target (manual verify, checklist doc): with a Limited Photos Library, add a
recent photo (incremental catches it), import an old-dated photo (incremental
catches it via modificationDate), delete a photo (incremental ignores it; the next
launch `.all` reconciles the server record).

## 11. Deferred

1. **Server-side date/id query** so the server round-trip is windowed too (needs a
   server API change; server pagination stays full here).
2. **Periodic background full sweep** (so deletions reconcile without a relaunch).
3. **`resourceTotal` display** (still cached, not shown).
4. **Restart (Settings save) during an active run forces a full `.all` reconcile**
   (Codex #2, deferred). Today a `start()` while counting/syncing is a no-op (the
   pre-existing `!isSyncing && !isCounting` guard from #11), so a settings change
   mid-run isn't fully reconciled until the next launch `.all` or manual Sync Now.
   A proper fix distinguishes "restart" from "launch" (a new engine signal) and
   defers an `.all` until the active child settles — and must update the deliberate
   `startWhileCountingDoesNotRestartAssess` / `startDuringSyncDoesNotStrandTheEngine`
   tests. Its own small slice.

## 12. Ships as

`Apple clients: Incremental sync range (#15)` off `main`, with this design doc and
a plan in `files-nest/docs/plans/`.
