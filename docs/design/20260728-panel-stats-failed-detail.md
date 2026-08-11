# Design: Panel real stats + failed-items detail

**Date:** 2026-07-28
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (`SyncSummary`, `FailedItem.filename`, `SyncEngine.summaryStream`, engine impls) · `apple/macos/FilesNest` (`AppModel` wiring, `PanelView` tiles, new `FailedItemsView`)
**Depends on:**
- `docs/design/20260726-photos-library-real-syncnow.md` (`LiveSyncEngine`, `SyncReport`, merged #7)
- `docs/design/20260726-menubar-shell.md` (`SyncEngine` seam, `StubSyncEngine`, `AppModel`, `PanelView` slide infrastructure, merged #6)
- `docs/design/20260726-synccoordinator.md` (`SyncReport`, `FailedItem`, `SyncPlan.skipped`, merged #4)

---

## 1. Purpose

The panel's three stat tiles are hardcoded placeholders from the #6 mock (`tile("1,240", "Backed up")`, `pending` defaults to `3`, `"0"` Failed). Make them reflect real backup state, and make a non-zero **Failed** count actionable by revealing *which* files failed and *why* — data `SyncCoordinator` already produces in `SyncReport.failed` but `LiveSyncEngine` currently discards.

This is a UI/polish slice: no change to the sync engine's behavior, only surfacing data it already computes.

---

## 2. Scope

**In this slice:**

- `apple/FilesNestCore`:
  - `SyncSummary` value type (§4.1).
  - `FailedItem` gains `filename` (§4.2).
  - `SyncEngine.summaryStream()` (§5), implemented by `LiveSyncEngine` (real) and `StubSyncEngine` (canned).
- `apple/macos/FilesNest`:
  - `AppModel` publishes `summary` (§6).
  - `PanelView` tiles bound to real data; **Failed** tile tappable → slide-in (§6, §7).
  - `FailedItemsView` — new sub-screen listing failed `filename` + `reason` (§7).

**Deferred (out of scope):**

- Live idle **Pending** (photos taken but not yet synced) — requires library-change watching (`PHPhotoLibraryChangeObserver`), the next slice. Here, Pending is live-during-sync and `0` at rest.
- "Recent activity" list (the footer spot #6 left open).
- Per-item **retry** from the failed view.
- Distinguishing photos vs resources in counts — tiles count **resources/files** (a Live Photo = 2), matching what's on the server.

---

## 3. Decisions (with forks considered)

1. **Tiles reflect the last *completed* sync, not live mid-sync state.** During a sync the hero ring + current-item strip already communicate live progress; the tiles show the last completed summary and refresh when a sync finishes. This avoids needing mid-sync `skipped`/baseline values (which are only known once the plan runs). *Exception:* **Pending** is derived live from `.syncing` progress (§4.3), because it's cheap (`total − completed`) and makes the tile meaningful.

2. **A separate `summaryStream()`, not an extra `SyncStatus` case.** Summary is orthogonal to the state machine and must **persist across** `.syncing`/`.paused` (you still want "Backed up 1,240" visible while a new sync runs). Folding it into `SyncStatus` would churn every `switch`, make the summary vanish mid-sync, and conflate "what state am I in" with "how much is backed up." The cost is one additive protocol method with two conformances.

3. **`FailedItem` carries the `filename`.** The failed list must show human-readable names; `FailedItem` currently has only `key` (`localIdentifier#kind`, UUID-ish) + `reason`. The filename is available at failure time (`PlannedUpload.resource.filename`), so it's a cheap additive field that turns the list from cryptic to usable.

4. **Failed list is a slide-in sub-screen, not a popover.** Reuses the exact Settings pattern already in `PanelView` (`showingSettings` + `.move` transition, 320-wide, Back button). One consistent navigation idiom; no new presentation machinery.

---

## 4. Types (Core)

### 4.1 `SyncSummary`

```swift
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int          // library resources confirmed complete after the last sync
    public let failed: [FailedItem]   // items that failed in the last sync (empty = none)
    public init(backedUp: Int, failed: [FailedItem]) { … }

    public static let empty = SyncSummary(backedUp: 0, failed: [])
}
```

`failed.count` drives the Failed tile; the list drives `FailedItemsView`. `Pending` is **not** stored (derived live — §4.3).

### 4.2 `FailedItem` (+ `filename`)

```swift
public struct FailedItem: Sendable, Equatable {
    public let key: ResourceKey
    public let filename: String    // NEW — human-readable name for the failed-items list
    public let reason: String
    public init(key: ResourceKey, filename: String, reason: String) { … }
}
```

`SyncCoordinator` constructs `FailedItem` in two places (upload failure, delete failure). Both get `filename`:
- Upload failures: `item.resource.filename` (available).
- Delete failures: the planner's `PlannedDelete` carries `key` but no filename; use `del.key.encoded` as the filename fallback (a deleted server record has no library asset to name). Documented; deletes rarely fail and aren't the primary UX target.

### 4.3 Pending (derived, not stored)

`PanelView` computes Pending from `model.status`:
- `.syncing(p)` → `p.total - p.completed`
- otherwise → `0`

At rest after a completed sync, nothing is queued, so `0` is correct. A non-zero idle Pending (new photos not yet synced) is impossible without change-watching and is explicitly deferred (§2).

---

## 5. `SyncEngine.summaryStream()`

```swift
public protocol SyncEngine: Sendable {
    func statusStream() -> AsyncStream<SyncStatus>
    func summaryStream() -> AsyncStream<SyncSummary>   // NEW — current summary first, then each change
    func start() async
    func pause() async
    func resume() async
    func syncNow() async
}
```

Mirrors `statusStream`'s contract: each call returns an independent stream whose first element is the current summary. Implemented with the same proven `NSLock` + `[UUID: continuation]` pattern already in both engines.

- **`LiveSyncEngine`**: after `perform(.all, …)` returns a `SyncReport`, compute
  `SyncSummary(backedUp: report.skipped + report.uploaded.count, failed: report.failed)` and publish it. Initial summary is `.empty` (shown as "—" by the panel until the first sync — see §6). Failures still logged as today.
- **`StubSyncEngine`**: publishes a canned summary (e.g. after its fake `syncNow`) so previews/manual runs show plausible tiles. Keeps the stub a faithful UI driver.

**"—" vs 0:** the stream always carries a concrete `SyncSummary` (`.empty` before any sync) — no optionals. The panel distinguishes "no sync yet" from "0 backed up" purely by **status**: while `status == .signedOut` it renders "—" for Backed up/Pending; once signed in it renders the real numbers (including a genuine `0`). Pinned in §6/§10.

---

## 6. App wiring

`AppModel`:

```swift
@Published private(set) var status: SyncStatus = .signedOut
@Published private(set) var summary: SyncSummary = .empty
```

`begin()` starts a second `for await` loop over `engine.summaryStream()` assigning `self.summary` (same `@MainActor` Task pattern as the status loop).

`PanelView` tiles:
- **Backed up**: `status == .signedOut ? "—" : "\(summary.backedUp)"`.
- **Pending**: derived (§4.3); `status == .signedOut ? "—" : "\(pending)"`.
- **Failed**: `"\(summary.failed.count)"`, colored `.orange` when `> 0`; the tile is a `Button` (→ slide to `FailedItemsView`) only when `summary.failed.count > 0`, otherwise plain.

The existing hardcoded `tile("1,240", …)` / `pending`-defaults-to-3 code is replaced.

---

## 7. `FailedItemsView`

New view in the app target, presented via the panel's existing slide mechanism (a new `@State private var showingFailed` alongside `showingSettings`, same `.move`/animation):

- Header: Back button + "Failed items" title (mirrors `SettingsView`).
- A scrollable list of `summary.failed`: each row shows `filename` (bold, 1 line) and `reason` (caption, secondary, 2-line clamp).
- Empty state is unreachable (only entered when count > 0), but render a benign "No failures" fallback for safety.

`PanelView.body` gains a third branch in its `ZStack` (`showingFailed`), transitioning like Settings.

---

## 8. Testing

Headless `swift test` for Core; UI is manual-verify. Failure-injected, watched-to-fail-first.

### 8.1 Core

- **`LiveSyncEngine.summaryStream`**: after a `perform` returning a report with `skipped` + `uploaded` + `failed`, the published summary has `backedUp == skipped + uploaded.count` and `failed == report.failed`. Fresh subscriber gets the current summary first (mirrors the `statusStream` test). A `perform` with failures → summary carries them.
- **`SyncCoordinator` `FailedItem.filename`**: an upload failure yields `FailedItem` whose `filename` matches the resource's filename (extend `failedItemIsRecordedAndSyncContinues` to assert `filename`). A delete failure yields `filename == key.encoded` (extend `failedDeleteIsRecordedAndOthersContinue`).
- Update existing `FailedItem(key:reason:)` call sites (coordinator + `LiveSyncEngineTests`) to the new `init(key:filename:reason:)`.

### 8.2 App (manual verification)

- After a real sync: **Backed up** shows the synced count, **Pending** is 0 at rest and counts down during a sync, **Failed** is 0.
- Induce a failure if feasible (e.g., a permissions-revoked asset) → Failed tile turns orange and is tappable → slides to `FailedItemsView` showing filename + reason → Back returns to the dashboard.
- Signed-out state shows "—" tiles and a non-tappable Failed tile.

Documented in a checklist plan like `docs/plans/20260726-photos-library-real-syncnow-verification.md`.

---

## 9. Deferred / out of scope

- Live idle **Pending** (needs `PHPhotoLibraryChangeObserver` — next slice).
- "Recent activity" / history list.
- Per-item **retry** or "clear failures" from the failed view.
- Persisting the summary across app launches (it repopulates on the first sync; showing "—" until then is acceptable).

---

## 10. Open items (resolve during implementation)

1. **"—" vs 0 rendering (§5/§6).** Pinned: panel shows "—" for Backed up/Pending only while `status == .signedOut`; once signed in, real numbers (including a genuine 0). No optional gymnastics in the stream.
2. **Delete-failure filename (§4.2).** Confirmed `key.encoded` fallback; revisit only if a real delete-failure UX is ever needed.
