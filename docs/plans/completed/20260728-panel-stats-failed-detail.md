# Panel Real Stats + Failed-Items Detail — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the panel's hardcoded stat tiles with real data from the last sync, and make the Failed tile reveal which files failed and why.

**Architecture:** Surface data the sync already computes: add `filename` to `FailedItem`, add a `SyncSummary` value type, publish it from the engines via a new `SyncEngine.summaryStream()` (parallel to `statusStream()`), and bind the panel tiles + a new slide-in `FailedItemsView` to it.

**Tech Stack:** Swift 6, swift-testing (`import Testing`), Foundation, SwiftUI (`MenuBarExtra`).

**Design doc:** `docs/design/20260728-panel-stats-failed-detail.md`

## Global Constraints

- Swift 6 language mode; **zero** concurrency warnings. `NSLock` never held across `await`.
- `FilesNestCore` is pure Foundation/Security — no PhotoKit/SwiftUI. PhotoKit/SwiftUI live only in the app target.
- All Core work reachable by `swift test`. SwiftUI is manual-verify.
- Every test failure-injected and **watched to fail first**.
- App target uses file-system-synchronized groups: drop `.swift` files into `apple/macos/FilesNest/FilesNest/` — no `.pbxproj` hand-edits.
- Core test dir: `apple/FilesNestCore` (`swift test`). App build (run from `apple/macos/FilesNest`): `xcodebuild -project FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`.
- All commands assume the working directory prefix `/Users/paulohenriquesg/Projects/filesnest/files-nest`.
- Branch: `apple/panel-ux-polish` (already created). PR title: `Apple clients: panel real stats + failed-items detail (#N)`.
- Commit style: `feat:` / `test:` / `docs:`.

---

### Task 1: `FailedItem.filename`

Add a human-readable `filename` to `FailedItem` so the failed-items list can show real names instead of `localIdentifier#kind`. Populate it at both failure sites in the coordinator.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift` (add `filename` to `FailedItem`)
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift` (two `FailedItem(...)` call sites)
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift` (assert filename)
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift` (fix the one `FailedItem(...)` call site)

**Interfaces:**
- Consumes: `PlannedUpload.resource.filename`, `PlannedDelete.key.encoded`, existing `SyncCoordinatorTests` harness (`makeCoordinator`, `FakeServer`, `resource(_:)` whose filename is `"IMG.jpg"`).
- Produces: `FailedItem(key: ResourceKey, filename: String, reason: String)`. Upload failures set `filename = resource.filename`; delete failures set `filename = key.encoded`.

- [x] **Step 1: Extend the failing tests to assert filename**

In `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`, find `failedItemIsRecordedAndSyncContinues()` and add, after the existing `#expect(Set(report.failed.map { $0.key.localIdentifier }) == ["A", "B"])` line:

```swift
        #expect(report.failed.allSatisfy { $0.filename == "IMG.jpg" })
```

Find `failedDeleteIsRecordedAndOthersContinue()` and add, after `#expect(report.failed.map { $0.key.localIdentifier } == ["GONE1"])`:

```swift
        #expect(report.failed.map(\.filename) == ["GONE1#photo"])
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter SyncCoordinatorTests 2>&1 | tail -20`
Expected: **compile failure** — `FailedItem` has no member `filename`.

- [x] **Step 3: Add `filename` to `FailedItem`**

In `apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift`, replace the `FailedItem` struct with:

```swift
public struct FailedItem: Sendable, Equatable {
    public let key: ResourceKey
    public let filename: String   // human-readable; the failed-items UI renders this
    public let reason: String

    public init(key: ResourceKey, filename: String, reason: String) {
        self.key = key
        self.filename = filename
        self.reason = reason
    }
}
```

- [x] **Step 4: Populate `filename` at both coordinator failure sites**

In `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift`, replace the upload-failure line:

```swift
                failed.append(FailedItem(key: item.resource.key, reason: String(describing: error)))
```

with:

```swift
                failed.append(FailedItem(key: item.resource.key,
                                         filename: item.resource.filename,
                                         reason: String(describing: error)))
```

And the delete-failure line:

```swift
                failed.append(FailedItem(key: del.key, reason: String(describing: error)))
```

with:

```swift
                failed.append(FailedItem(key: del.key,
                                         filename: del.key.encoded,
                                         reason: String(describing: error)))
```

- [x] **Step 5: Fix the `LiveSyncEngineTests` call site so the suite compiles**

In `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`, in `partialFailuresStillEndWatching()`, replace:

```swift
            SyncReport(uploaded: [], deleted: [], failed: [FailedItem(key: key, reason: "boom")], skipped: 0)
```

with:

```swift
            SyncReport(uploaded: [], deleted: [], failed: [FailedItem(key: key, filename: "X.jpg", reason: "boom")], skipped: 0)
```

- [x] **Step 6: Run the full Core suite to verify pass**

Run: `cd apple/FilesNestCore && swift test 2>&1 | tail -6`
Expected: all pass, zero warnings.

- [x] **Step 7: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "feat: add filename to FailedItem for the failed-items UI"
```

---

### Task 2: `SyncSummary` + `SyncEngine.summaryStream()`

Add the summary value type and a parallel stream on `SyncEngine`. `LiveSyncEngine` publishes a real summary after each sync; `StubSyncEngine` publishes a canned one.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncSummary.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift` (add protocol method)
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift` (impl + publish)
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift` (impl + canned)
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift` (summary tests)

**Interfaces:**
- Consumes: `SyncReport.skipped`, `SyncReport.uploaded`, `SyncReport.failed`, `FailedItem` (with `filename` from Task 1).
- Produces: `SyncSummary(backedUp: Int, failed: [FailedItem])` + `SyncSummary.empty`; `SyncEngine.summaryStream() -> AsyncStream<SyncSummary>` (yields current summary first, then each change). `LiveSyncEngine` publishes `SyncSummary(backedUp: report.skipped + report.uploaded.count, failed: report.failed)` after each successful sync.

- [x] **Step 1: Write the failing summary tests**

Append to `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift` (inside the `LiveSyncEngineTests` struct, after the last `@Test`):

```swift
    func firstSummary(_ engine: any SyncEngine) async -> SyncSummary {
        var it = engine.summaryStream().makeAsyncIterator()
        return await it.next()!
    }

    @Test func summaryStartsEmpty() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() })
        #expect(await firstSummary(engine) == .empty)
    }

    @Test func summaryPublishedAfterSync() async {
        let uploaded = ResourceKey(localIdentifier: "A", kind: .photo)
        let f = FailedItem(key: ResourceKey(localIdentifier: "B", kind: .photo),
                           filename: "B.jpg", reason: "boom")
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [uploaded], deleted: [], failed: [f], skipped: 3)
        })
        await engine.start()
        await engine.syncNow()
        let summary = await firstSummary(engine)
        #expect(summary.backedUp == 4)          // skipped 3 + uploaded 1
        #expect(summary.failed == [f])
    }
```

- [x] **Step 2: Run to verify failure**

Run: `cd apple/FilesNestCore && swift test --filter LiveSyncEngineTests 2>&1 | tail -20`
Expected: **compile failure** — `SyncSummary` unknown / `summaryStream` not a member of `SyncEngine`.

- [x] **Step 3: Add the `SyncSummary` type**

Create `apple/FilesNestCore/Sources/FilesNestCore/SyncSummary.swift`:

```swift
import Foundation

/// A snapshot of backup state after the last completed sync, observed by the panel.
/// `Pending` is not stored here — the panel derives it live from `.syncing` progress
/// (design §4.3).
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int          // library resources confirmed complete after the last sync
    public let failed: [FailedItem]   // items that failed in the last sync (empty = none)

    public init(backedUp: Int, failed: [FailedItem]) {
        self.backedUp = backedUp
        self.failed = failed
    }

    public static let empty = SyncSummary(backedUp: 0, failed: [])
}
```

- [x] **Step 4: Add `summaryStream()` to the protocol**

In `apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift`, add the method inside the protocol, after `statusStream()`:

```swift
    /// The current summary followed by every change. Each call returns an
    /// independent stream whose first element is the current summary.
    func summaryStream() -> AsyncStream<SyncSummary>
```

- [x] **Step 5: Implement in `LiveSyncEngine`**

In `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`:

Add stored state next to the existing `status`/`continuations` declarations:

```swift
    private var summary: SyncSummary = .empty
    private var summaryContinuations: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]
```

Add the stream + setter right after the existing `statusStream()` / `set(_:)` methods:

```swift
    public func summaryStream() -> AsyncStream<SyncSummary> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(summary)         // current summary first
            summaryContinuations[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.summaryContinuations[id] = nil; self.lock.unlock()
            }
        }
    }

    private func setSummary(_ newSummary: SyncSummary) {
        lock.lock()
        summary = newSummary
        let conts = Array(summaryContinuations.values)
        lock.unlock()
        for c in conts { c.yield(newSummary) }
    }
```

In `syncNow()`, publish the summary on success. Replace:

```swift
            let report = try await perform(.all) { [weak self] progress in
                self?.set(.syncing(progress))
            }
            if !report.failed.isEmpty { logFailures(report.failed) }
            set(.watching(lastSync: lastSync))
```

with:

```swift
            let report = try await perform(.all) { [weak self] progress in
                self?.set(.syncing(progress))
            }
            if !report.failed.isEmpty { logFailures(report.failed) }
            setSummary(SyncSummary(backedUp: report.skipped + report.uploaded.count,
                                   failed: report.failed))
            set(.watching(lastSync: lastSync))
```

- [x] **Step 6: Implement in `StubSyncEngine`**

In `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift`:

Add stored state next to `status`/`continuations`:

```swift
    private var summary: SyncSummary = .empty
    private var summaryContinuations: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]
```

Add the stream + setter after the existing `statusStream()` / `set(_:)`:

```swift
    public func summaryStream() -> AsyncStream<SyncSummary> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(summary)
            summaryContinuations[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.summaryContinuations[id] = nil; self.lock.unlock()
            }
        }
    }

    private func setSummary(_ newSummary: SyncSummary) {
        lock.lock()
        summary = newSummary
        let conts = Array(summaryContinuations.values)
        lock.unlock()
        for c in conts { c.yield(newSummary) }
    }
```

In `syncNow()`, at the very end (after the final `set(.watching(lastSync: stampLastSync()))` inside the `autoComplete` path), publish a canned summary so previews show plausible tiles. Replace:

```swift
        set(.watching(lastSync: stampLastSync()))
    }
```

with:

```swift
        set(.watching(lastSync: stampLastSync()))
        setSummary(SyncSummary(backedUp: 1_240, failed: []))
    }
```

- [x] **Step 7: Run the summary tests + full suite**

Run: `cd apple/FilesNestCore && swift test 2>&1 | tail -8`
Expected: all pass (incl. `summaryStartsEmpty`, `summaryPublishedAfterSync`), zero warnings.

- [x] **Step 8: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncSummary.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift \
        apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "feat: SyncSummary + SyncEngine.summaryStream()"
```

---

### Task 3: Panel wiring + `FailedItemsView` + verification

Bind the tiles to real data, make the Failed tile open a slide-in list, and document the manual checklist. App target → build + manual-verify.

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/AppModel.swift` (publish `summary`)
- Modify: `apple/macos/FilesNest/FilesNest/PanelView.swift` (tiles + Failed navigation)
- Create: `apple/macos/FilesNest/FilesNest/FailedItemsView.swift`
- Create: `docs/plans/20260728-panel-stats-failed-detail-verification.md`

**Interfaces:**
- Consumes: `SyncEngine.summaryStream()`, `SyncSummary`, `FailedItem` (`key`, `filename`, `reason`), `SyncStatus`, existing `PanelView` slide infra (`slide`, `showingSettings`), `SettingsView` pattern.
- Produces: `AppModel.summary: SyncSummary`; `FailedItemsView(items:onDone:)`.

- [x] **Step 1: Publish `summary` from `AppModel`**

In `apple/macos/FilesNest/FilesNest/AppModel.swift`, add a published property after the `status` line:

```swift
    @Published private(set) var summary: SyncSummary = .empty
```

Add a task handle next to `streamTask`:

```swift
    private var summaryTask: Task<Void, Never>?
```

In `begin()`, inside the `guard streamTask == nil` block (after the existing `streamTask = Task { … }`), add:

```swift
        summaryTask = Task { [engine] in
            for await s in engine.summaryStream() { self.summary = s }
        }
```

- [x] **Step 2: Replace the hardcoded tiles in `PanelView`**

In `apple/macos/FilesNest/FilesNest/PanelView.swift`, replace the `tiles` computed property:

```swift
    private var tiles: some View {
        HStack(spacing: 8) {
            tile("1,240", "Backed up", .primary)
            tile("\(pending)", "Pending", pending > 0 ? .orange : .primary)
            tile("0", "Failed", .primary)
        }.padding(.horizontal, 12).padding(.bottom, 8)
    }
```

with:

```swift
    private var tiles: some View {
        HStack(spacing: 8) {
            tile(backedUpText, "Backed up", .primary)
            tile(pendingText, "Pending", pending > 0 ? .orange : .primary)
            failedTile
        }.padding(.horizontal, 12).padding(.bottom, 8)
    }

    @ViewBuilder private var failedTile: some View {
        let count = model.summary.failed.count
        if count > 0 {
            Button { withAnimation(slide) { showingFailed = true } } label: {
                tile("\(count)", "Failed", .orange)
            }.buttonStyle(.plain)
        } else {
            tile("0", "Failed", .primary)
        }
    }

    private var backedUpText: String { isSignedOut ? "—" : "\(model.summary.backedUp)" }
    private var pendingText: String { isSignedOut ? "—" : "\(pending)" }
```

Then replace the existing `pending` computed property:

```swift
    private var pending: Int { if case let .paused(p) = model.status { return p }; return 3 }
```

with (Pending is live-during-sync, 0 at rest — design §4.3):

```swift
    private var pending: Int {
        if case let .syncing(p) = model.status { return max(0, p.total - p.completed) }
        return 0
    }
```

- [x] **Step 3: Add the `showingFailed` state and slide branch**

In `PanelView`, add next to `@State private var showingSettings = false`:

```swift
    @State private var showingFailed = false
```

Replace the `body`'s `ZStack` contents:

```swift
        ZStack {
            if showingSettings {
                SettingsView(model: settings, onDone: { withAnimation(slide) { showingSettings = false } })
                    .transition(.move(edge: .trailing))
            } else {
                dashboard
                    .transition(.move(edge: .leading))
            }
        }
        .frame(width: 320)
        .clipped()
        .animation(slide, value: showingSettings)
```

with:

```swift
        ZStack {
            if showingSettings {
                SettingsView(model: settings, onDone: { withAnimation(slide) { showingSettings = false } })
                    .transition(.move(edge: .trailing))
            } else if showingFailed {
                FailedItemsView(items: model.summary.failed,
                                onDone: { withAnimation(slide) { showingFailed = false } })
                    .transition(.move(edge: .trailing))
            } else {
                dashboard
                    .transition(.move(edge: .leading))
            }
        }
        .frame(width: 320)
        .clipped()
        .animation(slide, value: showingSettings)
        .animation(slide, value: showingFailed)
```

- [x] **Step 4: Create `FailedItemsView`**

Create `apple/macos/FilesNest/FilesNest/FailedItemsView.swift`:

```swift
import SwiftUI
import FilesNestCore

/// Slide-in list of items that failed in the last sync (filename + reason).
/// Mirrors SettingsView's in-panel navigation (Back button, 320-wide).
struct FailedItemsView: View {
    let items: [FailedItem]
    var onDone: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Button { onDone() } label: { Label("Back", systemImage: "chevron.left") }
                    .buttonStyle(.link)
                Spacer()
            }
            Text("Failed items").font(.headline)

            if items.isEmpty {
                Text("No failures").font(.caption).foregroundStyle(.secondary)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        ForEach(items, id: \.key.encoded) { item in
                            VStack(alignment: .leading, spacing: 2) {
                                Text(item.filename).font(.caption).bold().lineLimit(1)
                                Text(item.reason).font(.caption2)
                                    .foregroundStyle(.secondary).lineLimit(2)
                            }.frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                }.frame(maxHeight: 220)
            }
        }
        .padding(16).frame(width: 320)
    }
}
```

- [x] **Step 5: Build the app**

Run: `cd apple/macos/FilesNest && xcodebuild -project FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build 2>&1 | grep -iE "error:|BUILD SUCCEEDED|BUILD FAILED" | head`
Expected: **BUILD SUCCEEDED** (the new file is auto-included via the file-system-synchronized group).

- [x] **Step 6: Write the manual verification checklist**

Create `docs/plans/20260728-panel-stats-failed-detail-verification.md`:

```markdown
# Manual verification: panel real stats + failed-items detail

SwiftUI panel behavior is manual-verify. Run the dev-signed app (Cmd+R) against a live server.

## Tiles
- [x] Signed out: Backed up and Pending show "—"; Failed shows "0" and is not tappable.
- [x] After Sign in + a successful Sync Now: Backed up shows the synced count, Pending is 0 at rest, Failed is 0.
- [x] During a sync: Pending counts down (total − completed) alongside the ring; it returns to 0 when done.

## Failed items
- [x] Induce a failure if feasible (e.g. revoke access to one asset, or point at a server that rejects one item). Failed tile turns orange and becomes tappable.
- [x] Tap Failed → slides to "Failed items" showing each filename + reason.
- [x] Back returns to the dashboard with the left/right slide.

## Regression
- [x] Settings slide still works (Settings ⇄ dashboard) and is unaffected by the new Failed slide.
```

- [x] **Step 7: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/AppModel.swift \
        apple/macos/FilesNest/FilesNest/PanelView.swift \
        apple/macos/FilesNest/FilesNest/FailedItemsView.swift \
        docs/plans/20260728-panel-stats-failed-detail-verification.md
git commit -m "feat: real panel stat tiles + failed-items slide-in view"
```

- [x] **Step 8: Perform the manual verification**

Work through `docs/plans/20260728-panel-stats-failed-detail-verification.md` (Cmd+R, dev-signed) and record the outcome in the PR description.

---

## Self-Review

**Spec coverage:**
- §4.1 `SyncSummary` → Task 2. §4.2 `FailedItem.filename` → Task 1. §4.3 Pending derived → Task 3 Step 2.
- §5 `summaryStream()` + engine impls → Task 2. §6 `AppModel.summary` + tile binding + "—" while signed out → Task 3 Steps 1–2. §7 `FailedItemsView` + slide branch → Task 3 Steps 3–4.
- §8.1 Core tests (summary math, filename) → Tasks 1 & 2. §8.2 manual verification → Task 3 Steps 6/8.
- §3.2 separate stream (not `SyncStatus`) → Task 2 (protocol method, enum untouched).

**Placeholder scan:** No TBD/TODO; every code step shows complete code; commands include expected output.

**Type consistency:** `FailedItem(key:filename:reason:)`, `SyncSummary(backedUp:failed:)` + `.empty`, `SyncEngine.summaryStream() -> AsyncStream<SyncSummary>`, `AppModel.summary`, `FailedItemsView(items:onDone:)`, and `SyncReport(uploaded:deleted:failed:skipped:)` are consistent across all tasks and match the existing sources.
