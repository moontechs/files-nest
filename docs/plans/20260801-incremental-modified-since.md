# Incremental sync range (`.modifiedSince`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make change-triggered counts and syncs incremental — scanning only items modified since the last sync (`modificationDate`) — while launch, restart, and manual Sync Now stay full `.all`.

**Architecture:** Replace the dead, creationDate-based `SyncRange.dates` with `.modifiedSince(Date)` (upload-only, `modificationDate` predicate). The engine picks the range by trigger and threads it through the count→sync chain; `finishSync` becomes range-aware so a windowed sync keeps whole-library `backedUp`. Done as add → migrate → remove so the build stays green.

**Tech Stack:** Swift 6 (strict concurrency), swift-testing, PhotoKit (app target only), Foundation.

## Global Constraints

- `FilesNestCore` is **PhotoKit-free**; PhotoKit only in `apple/macos/FilesNest`.
- Swift 6 language mode; `NSLock` never held across `await`.
- Tests use swift-testing (`import Testing`, `@Test`, `#expect`); fakes under `Tests/FilesNestCoreTests/Support/`.
- App target uses **file-system-synchronized groups** — add `.swift` files by dropping them in `apple/macos/FilesNest/FilesNest/`, no `.pbxproj` edits.
- Commands run from `/Users/paulohenriquesg/Projects/filesnest/files-nest`.
- Core tests: `cd apple/FilesNestCore && swift test` (add `--filter <Name>` for one).
- App build: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
- Design doc: `docs/design/20260801-incremental-modified-since.md`. Ships as `Apple clients: Incremental sync range (#15)`.

## File Map

- `apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift` — add `.modifiedSince`, later remove `.dates`.
- `apple/FilesNestCore/Sources/FilesNestCore/SyncPlanner.swift` — handle `.modifiedSince` (no deletes); later drop `.dates`.
- `apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift` — `.modifiedSince` predicate; later drop `.dates`.
- `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift` — range-aware assess seam, range selection, range-aware `finishSync`, `incrementalRange`.
- `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` — range-aware `assess` closure.
- Tests: `SyncPlannerTests`, `SyncCoordinatorTests`, `SyncValueTypeTests`, `CachingAssetLibraryTests`, `LiveSyncEngineTests`.
- `docs/plans/20260801-incremental-modified-since-verification.md` — manual checklist.

---

### Task 1: Add `.modifiedSince` to the range, planner, and adapter

Add the new case **alongside** `.dates` (adding an enum case does not break the existing `if case` handlers). Incremental is upload-only.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncPlanner.swift`
- Modify: `apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncPlannerTests.swift`

**Interfaces:**
- Produces: `SyncRange.modifiedSince(Date)`. `SyncPlanner.plan(..., range: .modifiedSince(d))` returns the same uploads as `.all` but `deletes == []`. `PhotosAssetLibrary` filters `modificationDate >= d`.

- [ ] **Step 1: Write the failing planner tests**

In `SyncPlannerTests.swift`, add (near the range-scoping tests):

```swift
    @Test func modifiedSinceUploadsMissingButNeverDeletes() {
        // A server record absent from the (windowed) library must NOT be deleted under .modifiedSince.
        let janRec = rec("JAN", status: .complete, id: "J1", date: "2024-01-10T12:00:00Z")
        let plan = SyncPlanner.plan(library: [], server: [janRec], range: .modifiedSince(date("2024-01-01T00:00:00Z")))
        #expect(plan.deletes.isEmpty)
    }

    @Test func modifiedSinceStillPlansUploadsForScannedItems() {
        // Upload side is identical to .all: a scanned library item not on the server is an upload.
        let res = AssetResource(key: ResourceKey(localIdentifier: "NEW", kind: .photo),
                                filename: "NEW.jpg", creationDate: date("2024-03-01T00:00:00Z"), bundleID: nil)
        let plan = SyncPlanner.plan(library: [res], server: [], range: .modifiedSince(date("2024-01-01T00:00:00Z")))
        #expect(plan.uploads.map { $0.resource.key.encoded } == [res.key.encoded])
        #expect(plan.deletes.isEmpty)
    }
```

- [ ] **Step 2: Add the enum case**

In `SyncRange.swift`:

```swift
public enum SyncRange: Sendable, Equatable {
    case all
    case dates(ClosedRange<Date>)
    case modifiedSince(Date)   // incremental, upload-only; matched against modificationDate
}
```

- [ ] **Step 3: Run tests to verify they fail to compile / fail**

Run: `cd apple/FilesNestCore && swift test --filter modifiedSince`
Expected: FAIL (planner does not yet special-case `.modifiedSince`; the delete side would treat the absent record like `.all` and delete it → `modifiedSinceUploadsMissingButNeverDeletes` fails).

- [ ] **Step 4: Handle `.modifiedSince` in the planner**

In `SyncPlanner.swift`, replace the delete-side block. Current:

```swift
        // Delete side — server records absent from the library.
        var deletes: [PlannedDelete] = []
        for rec in server where !libraryKeys.contains(rec.localIdentifier) {
            switch rec.status {
            case .deleted, .completing:
                continue // already gone / mid-move — leave alone
            case .uploading, .complete, .backendLost:
                if case .dates(let window) = range {
                    guard let d = parseDate(rec.creationDate), window.contains(d) else { continue }
                }
                if let key = try? ResourceKey(parsing: rec.localIdentifier) {
                    deletes.append(PlannedDelete(uploadID: rec.id, key: key))
                }
            }
        }
```

Change to (incremental produces no deletes; `.dates` scoping preserved for now):

```swift
        // Delete side — server records absent from the library. Incremental (.modifiedSince) is
        // upload-only: a windowed scan can't tell a deletion from an out-of-window asset, so it
        // never deletes; deletions reconcile on the next .all.
        var deletes: [PlannedDelete] = []
        if case .modifiedSince = range {
            // no deletes
        } else {
            for rec in server where !libraryKeys.contains(rec.localIdentifier) {
                switch rec.status {
                case .deleted, .completing:
                    continue // already gone / mid-move — leave alone
                case .uploading, .complete, .backendLost:
                    if case .dates(let window) = range {
                        guard let d = parseDate(rec.creationDate), window.contains(d) else { continue }
                    }
                    if let key = try? ResourceKey(parsing: rec.localIdentifier) {
                        deletes.append(PlannedDelete(uploadID: rec.id, key: key))
                    }
                }
            }
        }
```

- [ ] **Step 5: Add the `.modifiedSince` predicate to the adapter**

In `PhotosAssetLibrary.swift`, extend the range→predicate mapping. Current:

```swift
                    if case .dates(let r) = range {
                        options.predicate = NSPredicate(format: "creationDate >= %@ AND creationDate <= %@",
                                                        r.lowerBound as NSDate, r.upperBound as NSDate)
                    }
```

Change to:

```swift
                    if case .dates(let r) = range {
                        options.predicate = NSPredicate(format: "creationDate >= %@ AND creationDate <= %@",
                                                        r.lowerBound as NSDate, r.upperBound as NSDate)
                    } else if case .modifiedSince(let d) = range {
                        options.predicate = NSPredicate(format: "modificationDate >= %@", d as NSDate)
                    }
```

- [ ] **Step 6: Run planner tests + full Core suite + app build**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS (new `.modifiedSince` tests pass; all existing tests still pass — `.dates` untouched).
Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
Expected: BUILD SUCCEEDED.

- [ ] **Step 7: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncPlanner.swift \
        apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncPlannerTests.swift
git commit -m "Incremental range: add .modifiedSince (upload-only) to range, planner, adapter"
```

---

### Task 2: Make the `assess` seam range-aware (mechanical, no behavior change)

The count must be able to scan a windowed range. Give `assess` a `SyncRange` parameter; every caller keeps passing `.all`, so behavior is unchanged. This is a pure signature refactor.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `SyncRange.modifiedSince` (Task 1).
- Produces: `assess: (@Sendable (SyncRange, AssessProgress) async throws -> Assessment)?`; `beginCounting(gen:range:)`. Callers pass `.all` (Task 3 threads real ranges).

- [ ] **Step 1: Change the `assess` seam type**

In `LiveSyncEngine.swift`, the stored property (line ~47):

```swift
    private let assess: (@Sendable (_ range: SyncRange, _ progress: AssessProgress) async throws -> Assessment)?
```

and the `init` parameter (line ~74):

```swift
                assess: (@Sendable (_ range: SyncRange, _ progress: AssessProgress) async throws -> Assessment)? = nil,
```

- [ ] **Step 2: Thread a range through `beginCounting`**

Change `beginCounting(gen:)` to `beginCounting(gen:range:)`:

```swift
    private func beginCounting(gen: UInt64, range: SyncRange) {
        guard let assess else { setStatus(.watching(lastSync: lastSync)); return }
        setStatus(.counting(done: 0, total: 0))
        assessChild = Task { [assess, submit] in
            do {
                let progress = AssessProgress { done, total in submit(.counting(gen: gen, done: done, total: total)) }
                let a = try await assess(range, progress)
                submit(.assessFinished(gen: gen, a))
            } catch is CancellationError {
            } catch {
                submit(.assessFinished(gen: gen, nil))
            }
        }
    }
```

And in `startIdleCount`, pass `.all` for now (line ~294): `beginCounting(gen: generation, range: .all)`.

- [ ] **Step 3: Update the composition-root `assess` closure**

In `FilesNestApp.swift`, change the `assess` closure to take `range` and use it for the scan, preserving the full `resourceTotal` for windowed scans. Current opens `assess: { progress in` and does `let scan = try await library.resources(in: .all, onProgress: progress.report)`. Change the signature and the two range-dependent lines:

```swift
            assess: { range, progress in
                let scan = try await library.resources(in: range, onProgress: progress.report)
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    let a = Assessment(backedUp: 0, pending: scan.count, resourceTotal: scan.count)
                    stateStore.saveAssessment(a); return a
                }
                let client = ServerClient(baseURL: url, credentials: credStore)
                var records: [UploadRecord] = []
                var cursor: String? = nil
                repeat {
                    let page = try await client.listUploads(cursor: cursor)
                    records += page.items
                    cursor = page.nextCursor
                } while cursor != nil
                let plan = SyncPlanner.plan(library: scan, server: records, range: range)
                let resourceTotal = (range == .all)
                    ? scan.count
                    : (stateStore.loadAssessment()?.resourceTotal ?? scan.count)   // don't clobber full total with a window size
                let a = Assessment(backedUp: records.filter { $0.status == .complete }.count,
                                   pending: plan.uploads.count,
                                   resourceTotal: resourceTotal)
                stateStore.saveAssessment(a)
                return a
            },
```

(The signed-out early-return keeps `resourceTotal: scan.count` — signed-out is only reached with `.all` in practice, and there is no prior server truth to preserve.)

- [ ] **Step 4: Migrate every test `assess` closure to the two-arg form**

In `LiveSyncEngineTests.swift`, every `assess:` closure gains a leading range parameter (unused). Apply mechanically:
- `assess: { _ in` → `assess: { _, _ in`
- `assess: { progress in` → `assess: { _, progress in`
- `assess: { _ in await gate.wait(); ... }` → `assess: { _, _ in await gate.wait(); ... }` (same rule)

There are ~18 occurrences (the `grep -n "assess: {"` sites). None use the range yet.

- [ ] **Step 5: Run full Core suite + app build**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS — identical behavior; every count still scans `.all`.
Run: `xcodebuild ... build`
Expected: BUILD SUCCEEDED.

- [ ] **Step 6: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/macos/FilesNest/FilesNest/FilesNestApp.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "Incremental range: make the assess seam range-aware (no behavior change)"
```

---

### Task 3: Engine range selection + range-aware `finishSync`

Now wire the actual behavior: change-triggered cycles run incremental; launch/restart/Sync Now run `.all`; `finishSync` keeps whole-library `backedUp` for windowed syncs.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `beginCounting(gen:range:)` (Task 2), `incrementalRange()`.
- Produces: `autoSyncRange: SyncRange?` (replaces `autoSyncAfterCount`); `startIdleCount(range:autoSync:)`; `doSyncNow(range:)`; `currentSyncRange`; `incrementalRange()`. Change cycles use `.modifiedSince(lastSyncStarted − 60s)`; launch/restart/syncNow use `.all`.

- [ ] **Step 1: Write the failing tests**

Add to `LiveSyncEngineTests.swift`:

```swift
    // MARK: - incremental range

    @Test func libraryChangeUsesModifiedSinceLaunchUsesAll() async {
        let state = InMemorySyncStateStore()
        let base = Date(timeIntervalSince1970: 1_000_000)
        state.saveLastSyncStarted(base)
        let recorded = RangeBox()
        let pending = IntBox(0)   // launch finds nothing → no launch sync; keeps the recorded ranges clean
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { range, _ in await recorded.add(range); return self.emptyReport() },
                                    assess: { range, _ in await recorded.add(range)
                                                          return Assessment(backedUp: 0, pending: await pending.value, resourceTotal: 0) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)          // launch count (.all), no sync
        await engine.settle()
        #expect(await recorded.all == [.all])              // launch scanned .all

        await recorded.clear()
        await pending.set(1)
        await engine.libraryDidChange()                     // change → incremental count + sync
        _ = await awaitStatus(engine, isSyncing)
        _ = await awaitStatus(engine, isWatching)
        await engine.settle()
        let want = SyncRange.modifiedSince(base.addingTimeInterval(-60))
        let ranges = await recorded.all
        #expect(!ranges.isEmpty)
        #expect(ranges.allSatisfy { $0 == want })          // both the count and the sync used .modifiedSince(base-60)
    }

    @Test func manualSyncNowUsesAll() async {
        let state = InMemorySyncStateStore()
        state.saveLastSyncStarted(Date(timeIntervalSince1970: 1_000_000))
        let recorded = RangeBox()
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { range, _ in await recorded.add(range); return self.emptyReport() })
        await engine.start(); await engine.settle()         // no assess → straight to watching
        await engine.syncNow()
        _ = await awaitStatus(engine, isWatching)
        await engine.settle()
        #expect(await recorded.all == [.all])               // manual Sync Now is always full
    }

    @Test func incrementalSyncKeepsWholeLibraryBackedUp() async {
        let state = InMemorySyncStateStore()
        state.saveLastSyncStarted(Date(timeIntervalSince1970: 1_000_000))
        let pending = IntBox(0)
        let up = ResourceKey(localIdentifier: "NEW", kind: .photo)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, _ in SyncReport(uploaded: [up], deleted: [], failed: [], skipped: 0) },
                                    assess: { _, _ in Assessment(backedUp: 63_000, pending: await pending.value, resourceTotal: 70_000) })
        await engine.start()
        _ = await awaitSummary(engine) { $0.backedUp == 63_000 }   // launch count grounds whole-library backedUp; pending 0 → no sync
        _ = await awaitStatus(engine, isWatching)
        await pending.set(1)
        await engine.libraryDidChange()                             // incremental count(63000, pending 1) → incremental sync(uploaded 1)
        let sum = await awaitSummary(engine) { $0.backedUp == 63_001 }
        #expect(sum.backedUp == 63_001)   // base 63000 + 1 uploaded — NOT collapsed to report.skipped+uploaded (=1)
    }
```

Add the range-recording helper near `IntBox` at the bottom of the file:

```swift
/// Records the ranges a fake perform/assess was called with.
actor RangeBox {
    private(set) var all: [SyncRange] = []
    func add(_ r: SyncRange) { all.append(r) }
    func clear() { all.removeAll() }
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter manualSyncNowUsesAll`
Expected: PASS already (syncNow is `.all` today) — this one guards against regressions.
Run: `cd apple/FilesNestCore && swift test --filter libraryChangeUsesModifiedSinceLaunchUsesAll`
Expected: FAIL — the change cycle currently scans `.all` (recorded ranges are `.all`, not `.modifiedSince`). `incrementalSyncKeepsWholeLibraryBackedUp` hangs/times out (backedUp collapses to 1, never reaches 63001) — kill it after it stalls; the stall is the failure.

- [ ] **Step 3: Replace `autoSyncAfterCount` with `autoSyncRange`**

In `LiveSyncEngine.swift`:

Property (line ~58):

```swift
    private var autoSyncRange: SyncRange?      // range for the sync to chain after the in-flight count (nil = don't chain)
    private var currentSyncRange: SyncRange = .all   // range of the in-flight sync, for finishSync sourcing
```

(Delete the `autoSyncAfterCount` line.)

- [ ] **Step 4: Add `incrementalRange()` + the margin, and range-aware `startIdleCount`**

Add near `startIdleCount` (and its margin constant):

```swift
    private static let incrementalMargin: TimeInterval = 60   // clock-skew safety; re-scanning slightly wider is a no-op

    /// The window for a change-triggered cycle: everything modified since the last sync started
    /// (minus a small margin). `.all` when there is no prior sync yet.
    private func incrementalRange() -> SyncRange {
        guard let last = state.loadLastSyncStarted() else { return .all }
        return .modifiedSince(last.addingTimeInterval(-Self.incrementalMargin))
    }
```

Change `startIdleCount` to carry the range:

```swift
    private func startIdleCount(range: SyncRange, autoSync: Bool) {
        guard signedIn else { return }
        generation &+= 1
        lastProgress = nil
        autoSyncRange = autoSync ? range : nil
        beginCounting(gen: generation, range: range)
    }
```

- [ ] **Step 5: Range-aware `doSyncNow`**

Change `doSyncNow()` to take a range, pass it to `perform`, and record it:

```swift
    private func doSyncNow(range: SyncRange) {
        log("cmd syncNow (signedIn=\(signedIn) syncChild=\(syncChild != nil) status=\(currentStatus))")
        guard signedIn, syncChild == nil else { return }
        if case .paused = currentStatus { return }
        generation &+= 1
        assessChild?.cancel(); assessChild = nil
        let gen = generation
        lastProgress = nil
        currentSyncRange = range
        syncBaseBackedUp = currentSummary.backedUp
        setStatus(.syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)))
        syncChild = Task { [perform, submit] in
            do {
                let report = try await perform(range) { progress in submit(.progress(gen: gen, progress)) }
                submit(.finished(gen: gen, report))
            } catch is CancellationError {
            } catch {
                submit(.failed(gen: gen, message: String(describing: error)))
            }
        }
    }
```

- [ ] **Step 6: Range-aware `finishSync`**

Replace the `backedUp` derivation in `finishSync`:

```swift
        let pendingUploads = report.failed.filter { $0.kind == .upload }.count
        let backedUp: Int
        switch currentSyncRange {
        case .all:
            backedUp = report.skipped + report.uploaded.count           // full scan saw everything
        case .dates, .modifiedSince:
            backedUp = syncBaseBackedUp + report.uploaded.count          // baseline (from the count) + new uploads
        }
        setSummary(SyncSummary(backedUp: backedUp, pending: pendingUploads, failed: report.failed))
        setStatus(.watching(lastSync: lastSync))
        drainPendingChangeIfAny()
```

- [ ] **Step 7: Wire the call sites**

Update the handler and callers:

- `.syncNow` command (in `handle`): `case .syncNow: doSyncNow(range: .all)`
- `.assessFinished` (in `handle`): replace the tail with

  ```swift
                let range = autoSyncRange
                autoSyncRange = nil
                if let range, (a?.pending ?? 0) > 0 { doSyncNow(range: range) }
                else { drainPendingChangeIfAny() }
  ```

- `.libraryChanged` idle branch: `case .watching, .error: startIdleCount(range: incrementalRange(), autoSync: true)`
- `drainPendingChangeIfAny`: `startIdleCount(range: incrementalRange(), autoSync: true)`
- `doStart` idle branch: paused → `startIdleCount(range: .all, autoSync: false)`; else → `startIdleCount(range: .all, autoSync: true)`
- `doStart` signed-out branch: replace `autoSyncAfterCount = false` with `autoSyncRange = nil`

- [ ] **Step 8: Run the new tests, then the full suite ×3**

Run: `cd apple/FilesNestCore && swift test --filter "libraryChangeUsesModifiedSinceLaunchUsesAll"`
then `--filter incrementalSyncKeepsWholeLibraryBackedUp`, `--filter manualSyncNowUsesAll`
Expected: PASS all.
Run: `cd apple/FilesNestCore && swift test && swift test && swift test`
Expected: PASS (203 prior + 3 new = 206), 3× clean. Existing `.all` finishSync tests (`summaryReflectsReportAfterSync`, `pendingAfterSyncCountsUploadFailuresOnly`) still pass — `.all` sourcing unchanged.

- [ ] **Step 9: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "Incremental range: change cycles run .modifiedSince; range-aware finishSync"
```

---

### Task 4: Remove the dead `.dates` case

`.dates` (creationDate window with within-window deletes) is now superseded by `.modifiedSince` and constructed nowhere in the app. Remove it and migrate/retire its tests.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift`, `SyncPlanner.swift`
- Modify: `apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift`
- Modify tests: `SyncValueTypeTests`, `CachingAssetLibraryTests`, `SyncCoordinatorTests`, `SyncPlannerTests`

- [ ] **Step 1: Migrate the mechanical `.dates` test references to `.modifiedSince`**

- `SyncValueTypeTests.swift` `syncRangeEquatable`:

  ```swift
      @Test func syncRangeEquatable() {
          let d = Date(timeIntervalSince1970: 100)
          #expect(SyncRange.modifiedSince(d) == SyncRange.modifiedSince(d))
          #expect(SyncRange.all != SyncRange.modifiedSince(d))
      }
  ```

- `CachingAssetLibraryTests.swift` `differentRangeReScans` (line 49): replace the second scan's range

  ```swift
          _ = try await caching.resources(in: .modifiedSince(Date(timeIntervalSince1970: 5)), onProgress: nil)
  ```

- `SyncCoordinatorTests.swift` `coordinatorForwardsRangeToLibrary`: replace `let jan = …` + the sync call + assertion with a `.modifiedSince` bound

  ```swift
          let since = date("2024-01-01T00:00:00Z")
          _ = try await coord.sync(range: .modifiedSince(since))
          #expect(lib.requestedRanges == [.modifiedSince(since)])
  ```

- [ ] **Step 2: Retire the `.dates`-specific planner + coordinator delete-scoping tests**

These test creationDate within-window deletes, which no longer exist (`.modifiedSince` is upload-only; Task 1 already covers "no deletes"). In `SyncPlannerTests.swift` delete the three tests `datesRangeDoesNotDeleteRecordsOutsideWindow`, `datesRangeDeletesRecordsInsideWindow`, `datesRangeEndpointsAreInclusive`. Rewrite `nilCreationDateNeverDeletedUnderDatesButDeletedUnderAll` to keep only the `.all` assertion:

```swift
    @Test func nilCreationDateDeletedUnderAll() {
        let noDate = rec("NODATE", status: .complete, id: "N1", date: nil)
        #expect(SyncPlanner.plan(library: [], server: [noDate], range: .all).deletes.map(\.uploadID) == ["N1"])
    }
```

In `SyncCoordinatorTests.swift` rewrite `januarySyncDoesNotDeleteFebruaryBackup` as a `.modifiedSince` upload-only proof:

```swift
    @Test func incrementalSyncUploadsNewButDeletesNothing() async throws {
        let server = FakeServer(host: "sc-range.test")
        let febID = server.seed(localIdentifier: "FEB#photo", status: "complete",
                                creationDate: "2024-02-10T12:00:00Z")
        let report = try await makeCoordinator(
            server: server,
            library: [resource("JAN", date: "2024-01-15T09:00:00Z")])
            .sync(range: .modifiedSince(date("2024-01-01T00:00:00Z")))

        #expect(report.uploaded == [ResourceKey(localIdentifier: "JAN", kind: .photo)])
        #expect(report.deleted.isEmpty)                       // incremental never deletes
        #expect(server.record(id: febID)?.status == "complete")
    }
```

- [ ] **Step 3: Remove the `.dates` case and its handlers**

- `SyncRange.swift`: delete `case dates(ClosedRange<Date>)`.
- `SyncPlanner.swift`: in the delete side, remove the `if case .dates(let window) = range { … }` guard, and simplify the `else` to `if case .all = range`:

  ```swift
        var deletes: [PlannedDelete] = []
        if case .all = range {
            for rec in server where !libraryKeys.contains(rec.localIdentifier) {
                switch rec.status {
                case .deleted, .completing:
                    continue
                case .uploading, .complete, .backendLost:
                    if let key = try? ResourceKey(parsing: rec.localIdentifier) {
                        deletes.append(PlannedDelete(uploadID: rec.id, key: key))
                    }
                }
            }
        }
  ```

  If `parseDate` is now unused, remove it too (compiler will warn).
- `PhotosAssetLibrary.swift`: remove the `if case .dates(let r) = range { … } else` branch, leaving only the `.modifiedSince` predicate:

  ```swift
                    if case .modifiedSince(let d) = range {
                        options.predicate = NSPredicate(format: "modificationDate >= %@", d as NSDate)
                    }
  ```
- `LiveSyncEngine.swift` `finishSync`: the switch `case .dates, .modifiedSince:` becomes `case .modifiedSince:` (now exhaustive with `.all`).

- [ ] **Step 4: Run full Core suite ×3 + app build**

Run: `cd apple/FilesNestCore && swift test && swift test && swift test`
Expected: PASS, 3× clean (no remaining `.dates` references; `grep -rn "\.dates(" apple` returns nothing).
Run: `xcodebuild ... build`
Expected: BUILD SUCCEEDED.

- [ ] **Step 5: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncPlanner.swift \
        apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncValueTypeTests.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/CachingAssetLibraryTests.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncPlannerTests.swift
git commit -m "Incremental range: remove the dead creationDate-based .dates case"
```

---

### Task 5: Manual-verify checklist

**Files:**
- Create: `docs/plans/20260801-incremental-modified-since-verification.md`

- [ ] **Step 1: Write the checklist**

Create `docs/plans/20260801-incremental-modified-since-verification.md`:

```markdown
# Incremental sync range — manual verification

Prereqs: signed in; a **Limited Photos Library** (~10 selected) backed up to a
steady state (Pending 0, Watching). Watch the `🟢 FN library:` console log for the
range on each enumeration (`enumeration start (range=…)`).

- [ ] **Add a recent photo** to the selected set. The change enumeration logs
      `range=modifiedSince(…)` (not `.all`), counts + syncs it, Backed-up +1.
- [ ] **Add a photo with an OLD capture date** (import/AirDrop something taken long
      ago into the selected set). It is still caught — the window is on
      modificationDate, not creationDate — and backs up.
- [ ] **Edit an existing photo** (crop/adjust). Its modificationDate bumps → the
      incremental cycle re-uploads it.
- [ ] **Delete a backed-up photo** from the selection. The incremental cycle does
      NOT remove the server record (upload-only). Relaunch the app → the launch
      `.all` (`range=all` in the log) reconciles and deletes the server record.
- [ ] **Launch** always logs `range=all`; **Sync Now** always logs `range=all`.
- [ ] Backed-up / Pending tiles stay whole-library correct across incremental
      cycles (they do not collapse to a small window count).
```

- [ ] **Step 2: Commit**

```bash
git add docs/plans/20260801-incremental-modified-since-verification.md
git commit -m "Incremental range: manual-verify checklist"
```

- [ ] **Step 3: Perform the manual verification**

Run the app against a Limited Photos Library and walk the checklist, recording any surprises before opening the PR.

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** §3 range model = Task 1 (+ removal in Task 4); §4 planner = Task 1/4; §5 adapter = Task 1/4; §6 engine plumbing = Task 2 (seam) + Task 3 (selection); §7 summary = Task 3 (`finishSync`); §8 composition root = Task 2; §10 tests across Tasks 1/3/4; §11 deferred (server-side query, periodic sweep, resourceTotal display) — not implemented.
- **Green at every task:** Task 1 adds a case (non-breaking `if case`); Task 2 is a mechanical seam refactor; Task 3 flips behavior with the new tests; Task 4 removes the dead case last. No task leaves Core red.
- **`.all` behavior is unchanged** throughout: `finishSync`'s `.all` branch keeps `report.skipped + report.uploaded.count`, so existing summary tests pass untouched.
- **Type consistency:** `assess: (SyncRange, AssessProgress) -> Assessment`, `beginCounting(gen:range:)`, `startIdleCount(range:autoSync:)`, `doSyncNow(range:)`, `currentSyncRange`, `autoSyncRange`, `incrementalRange()`, `SyncRange.modifiedSince(Date)` are used identically across tasks.
```
