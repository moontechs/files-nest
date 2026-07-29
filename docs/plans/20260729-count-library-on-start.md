# Count Library on Start — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (or subagent-driven-development) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On launch, run the per-resource library scan in the background behind a determinate "Counting N of 46,039" state, then show the exact at-rest Pending (a `SyncPlanner` dry-run). Warm launches show cached counts instantly and recount.

**Architecture:** All logic in `FilesNestCore` (pure Foundation). The engine gains an `assess` pass (cancellable child, generation-gated, mirroring the sync child) that computes an `Assessment`; the app injects the PhotoKit scan + server diff. Design: `docs/design/20260729-count-library-on-start.md`.

**Tech Stack:** Swift 6, swift-testing (`import Testing`), PhotoKit (app target only), SwiftUI (panel).

## Global Constraints

- Swift 6 language mode. `NSLock` must never be held across an `await` (use synchronous helpers).
- `FilesNestCore` stays pure Foundation — no PhotoKit / SwiftUI in `Sources/`.
- swift-testing only; fakes in `Tests/FilesNestCoreTests/Support/`.
- macOS app target is **file-system-synchronized** — drop `.swift` files into the target folder; no `.pbxproj` edits.
- Core test command: `cd /Users/paulohenriquesg/Projects/filesnest/files-nest/apple/FilesNestCore && swift test` (add `--filter <Suite>` per task; wrap long runs with `perl -e 'alarm 240; exec @ARGV' swift test`).
- App build command: `cd /Users/paulohenriquesg/Projects/filesnest/files-nest && xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`.
- **The macOS app target will not compile between Task 3 and Task 6** (PanelView needs the `.counting` case). Core tests are the gate for Tasks 1–5; the app build is Task 6's gate. This is expected.
- Standing rule: run a Codex review before finishing/merging (Task 7).

---

### Task 1: `Assessment` type + `SyncStateStore` cache

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/Assessment.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncStateStore.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncStateStoreTests.swift`

**Interfaces:**
- Produces: `struct Assessment { backedUp: Int; pending: Int; resourceTotal: Int }` (Sendable, Equatable, Codable); `SyncStateStore.loadAssessment() -> Assessment?` / `saveAssessment(_:)`.

- [ ] **Step 1: Write failing cache tests** — append to `SyncStateStoreTests`:

```swift
@Test func assessmentRoundTripsCodable() throws {
    let a = Assessment(backedUp: 63_201, pending: 7_243, resourceTotal: 70_444)
    let data = try JSONEncoder().encode(a)
    #expect(try JSONDecoder().decode(Assessment.self, from: data) == a)
}

@Test func userDefaultsCachesAssessment() {
    let suite = UserDefaults(suiteName: "scc.assess.\(UUID().uuidString)")!
    let store = UserDefaultsSyncStateStore(defaults: suite)
    #expect(store.loadAssessment() == nil)
    let a = Assessment(backedUp: 5, pending: 7, resourceTotal: 12)
    store.saveAssessment(a)
    #expect(store.loadAssessment() == a)
}

@Test func inMemoryCachesAssessment() {
    let store = InMemorySyncStateStore()
    #expect(store.loadAssessment() == nil)
    let a = Assessment(backedUp: 9, pending: 4, resourceTotal: 20)
    store.saveAssessment(a)
    #expect(store.loadAssessment() == a)
}
```

- [ ] **Step 2: Run tests, verify they fail** — `swift test --filter SyncStateStoreTests` → FAIL (`Assessment` / `loadAssessment` undefined).

- [ ] **Step 3: Create `Assessment.swift`:**

```swift
import Foundation

/// A snapshot of backup state computed by a full library assessment (scan + server diff).
/// Cached so a warm launch shows numbers instantly while a fresh count runs.
public struct Assessment: Sendable, Equatable, Codable {
    public let backedUp: Int      // server records with status == .complete
    public let pending: Int       // SyncPlanner.plan(...).uploads.count
    public let resourceTotal: Int // library resources enumerated

    public init(backedUp: Int, pending: Int, resourceTotal: Int) {
        self.backedUp = backedUp
        self.pending = pending
        self.resourceTotal = resourceTotal
    }
}
```

- [ ] **Step 4: Extend the protocol + both stores.** In `SyncStateStore.swift` add to the protocol:

```swift
    func loadAssessment() -> Assessment?
    func saveAssessment(_ assessment: Assessment)
```

Add to `UserDefaultsSyncStateStore` (new key + JSON):

```swift
    private let assessmentKey = "com.filesnest.sync.assessment"

    public func loadAssessment() -> Assessment? {
        guard let data = defaults.data(forKey: assessmentKey) else { return nil }
        return try? JSONDecoder().decode(Assessment.self, from: data)
    }

    public func saveAssessment(_ assessment: Assessment) {
        if let data = try? JSONEncoder().encode(assessment) { defaults.set(data, forKey: assessmentKey) }
    }
```

Add to `InMemorySyncStateStore` (new property behind the existing lock):

```swift
    private var _assessment: Assessment?
    func loadAssessment() -> Assessment? { lock.lock(); defer { lock.unlock() }; return _assessment }
    func saveAssessment(_ assessment: Assessment) { lock.lock(); defer { lock.unlock() }; _assessment = assessment }
```

- [ ] **Step 5: Run tests, verify pass** — `swift test --filter SyncStateStoreTests` → PASS.

- [ ] **Step 6: Commit** — `git add` the four files; `git commit -m "feat(core): Assessment type + SyncStateStore assessment cache"`.

---

### Task 2: `SyncSummary.pending` field

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncSummary.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift` (all `SyncSummary(...)` sites)
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift` (assertions), `SyncValueTypeTests.swift` if it constructs `SyncSummary`

**Interfaces:**
- Produces: `SyncSummary { backedUp: Int; pending: Int?; failed: [FailedItem] }`, `init(backedUp:pending:failed:)`, `.empty` with `pending: nil`.
- Consumes: nothing new.

- [ ] **Step 1: Update `SyncSummary`:**

```swift
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int
    public let pending: Int?          // exact at-rest backlog; nil = never counted
    public let failed: [FailedItem]

    public init(backedUp: Int, pending: Int?, failed: [FailedItem]) {
        self.backedUp = backedUp
        self.pending = pending
        self.failed = failed
    }

    public static let empty = SyncSummary(backedUp: 0, pending: nil, failed: [])
}
```

- [ ] **Step 2: Fix `LiveSyncEngine` call sites** (add `pending:`):
  - Live climb (`.progress` handler): `SyncSummary(backedUp: syncBaseBackedUp + p.completed, pending: currentSummary.pending, failed: currentSummary.failed)` (preserve the last known pending during a climb).
  - `.summaryRefreshed` handler (still present until Task 4): `SyncSummary(backedUp: backedUp, pending: currentSummary.pending, failed: currentSummary.failed)`.
  - `finishSync`: `SyncSummary(backedUp: report.skipped + report.uploaded.count, pending: report.failed.count, failed: report.failed)` — the final, correct at-rest pending after an `.all` sync.

- [ ] **Step 3: Fix `StubSyncEngine`** — `setSummary(SyncSummary(backedUp: 1_240, pending: 900, failed: []))` (canned non-nil so previews show a Pending number).

- [ ] **Step 4: Fix test assertions** — every `SyncSummary(...)` in `LiveSyncEngineTests` gains `pending:`. Specifically:
  - `.empty` comparisons are unchanged (still `== .empty`).
  - `startRefreshesBackedUpFromServer`: `== SyncSummary(backedUp: 7, pending: nil, failed: [])` (refresh doesn't set pending yet — Task 4 changes this test).
  - The fallback test: `== SyncSummary(backedUp: 4, pending: nil, failed: [])`.
  - `signOutClearsSummary`: `== SyncSummary(backedUp: 9, pending: nil, failed: [])`.
  - Any test asserting the post-sync summary now also asserts `pending` matches `report.failed.count`.

- [ ] **Step 5: Run full Core suite** — `swift test` → PASS (compile + green).

- [ ] **Step 6: Commit** — `git commit -m "feat(core): SyncSummary.pending (exact at-rest backlog; finishSync fills it from the report)"`.

---

### Task 3: `SyncStatus.counting` + `AssetLibrary` progress hook

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncStatus.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/AssetLibrary.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeAssetLibrary.swift`
- Modify: `apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift` (new signature + throttled progress)
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncStatusTests.swift`

**Interfaces:**
- Produces: `SyncStatus.counting(done: Int, total: Int)`; `AssetLibrary.resources(in:onProgress:)` requirement + `resources(in:)` convenience extension.
- Consumes: nothing new. `SyncCoordinator.sync` keeps calling `library.resources(in: range)` (resolves to the extension).

- [ ] **Step 1: Add the `.counting` case** to `SyncStatus`:

```swift
    case counting(done: Int, total: Int)   // launch scan in progress
```

- [ ] **Step 2: Write a failing equality test** in `SyncStatusTests`:

```swift
@Test func countingEquatable() {
    #expect(SyncStatus.counting(done: 3, total: 10) == .counting(done: 3, total: 10))
    #expect(SyncStatus.counting(done: 3, total: 10) != .counting(done: 4, total: 10))
}
```

Run `swift test --filter SyncStatusTests` → FAIL until the case exists (Step 1), then PASS.

- [ ] **Step 3: Change the `AssetLibrary` protocol + add convenience:**

```swift
public protocol AssetLibrary: Sendable {
    func resources(in range: SyncRange,
                   onProgress: (@Sendable (_ done: Int, _ total: Int) -> Void)?) async throws -> [AssetResource]
}

public extension AssetLibrary {
    /// Convenience for callers that don't need progress (e.g. SyncCoordinator's scan).
    func resources(in range: SyncRange) async throws -> [AssetResource] {
        try await resources(in: range, onProgress: nil)
    }
}
```

- [ ] **Step 4: Update `FakeAssetLibrary`** to the two-arg requirement (emit a single deterministic progress tick so tests can observe the hook if needed):

```swift
func resources(in range: SyncRange,
               onProgress: (@Sendable (Int, Int) -> Void)?) async throws -> [AssetResource] {
    recordRange(range)                 // sync helper: NSLock must not be held across an await
    if let error { throw error }
    onProgress?(items.count, items.count)
    return items
}
```

- [ ] **Step 5: Update `PhotosAssetLibrary`** — new signature; capture `total = assets.count`; emit throttled progress inside `enumerateObjects`:

```swift
func resources(in range: SyncRange,
               onProgress: (@Sendable (Int, Int) -> Void)? = nil) async throws -> [AssetResource] {
    try await ensureAuthorized()
    let cancel = CancelFlag()
    return try await withTaskCancellationHandler {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<[AssetResource], Error>) in
            DispatchQueue.global(qos: .userInitiated).async {
                let options = PHFetchOptions()
                options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
                if case .dates(let r) = range {
                    options.predicate = NSPredicate(format: "creationDate >= %@ AND creationDate <= %@",
                                                    r.lowerBound as NSDate, r.upperBound as NSDate)
                }
                let assets = PHAsset.fetchAssets(with: options)
                let total = assets.count
                var out: [AssetResource] = []
                assets.enumerateObjects { asset, idx, stop in
                    if cancel.isSet { stop.pointee = true; return }
                    let isLive = asset.mediaSubtypes.contains(.photoLive)
                    let created = asset.creationDate ?? .distantPast
                    for resource in PHAssetResource.assetResources(for: asset) {
                        guard let kind = Self.mapType(resource.type) else { continue }
                        out.append(AssetResource(
                            key: ResourceKey(localIdentifier: asset.localIdentifier, kind: kind),
                            filename: resource.originalFilename,
                            creationDate: created,
                            bundleID: isLive ? asset.localIdentifier : nil))
                    }
                    // Throttle: emit every 250 assets and on the last one (design §10, open item — tune during verify).
                    if let onProgress, (idx % 250 == 0 || idx == total - 1) { onProgress(idx + 1, total) }
                }
                if cancel.isSet { continuation.resume(throwing: CancellationError()); return }
                continuation.resume(returning: out)
            }
        }
    } onCancel: { cancel.set() }
}
```

(Keep the existing `#if DEBUG` logging lines.)

- [ ] **Step 6: Run Core suite** — `swift test` → PASS (SyncCoordinator tests still green via the extension). App target intentionally not built yet.

- [ ] **Step 7: Commit** — `git commit -m "feat(core): SyncStatus.counting + AssetLibrary progress hook"`.

---

### Task 4: `LiveSyncEngine` assess pass

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `Assessment` (Task 1), `SyncStatus.counting` (Task 3), `SyncSummary.pending` (Task 2).
- Produces: `LiveSyncEngine.init(..., assess:, cachedAssessment:, now:)` replacing `refreshBackedUp:`. `assess: (@Sendable (_ onProgress: @Sendable (Int, Int) -> Void) async throws -> Assessment)?`; `cachedAssessment: (@Sendable () -> Assessment?)?`.

- [ ] **Step 1: Write failing engine tests** (swap `refreshBackedUp:` for the new seams, add the new coverage). Key tests:

```swift
@Test func startCountsThenAssesses() async {
    let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                perform: { _, _ in self.emptyReport() },
                                assess: { onProgress in
                                    onProgress(3, 10); onProgress(10, 10)
                                    return Assessment(backedUp: 5, pending: 7, resourceTotal: 12)
                                })
    await engine.start()
    _ = await awaitStatus(engine) { if case .counting = $0 { return true }; return false }
    let sum = await awaitSummary(engine) { $0.pending == 7 }
    #expect(sum.backedUp == 5)
    #expect(await awaitStatus(engine) { if case .watching = $0 { return true }; return false } != nil)
}

@Test func cachedAssessmentSeedsBeforeCounting() async {
    let gate = Gate()
    let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                perform: { _, _ in self.emptyReport() },
                                assess: { _ in await gate.wait(); return Assessment(backedUp: 1, pending: 1, resourceTotal: 1) },
                                cachedAssessment: { Assessment(backedUp: 9, pending: 4, resourceTotal: 20) })
    await engine.start()
    #expect(await awaitSummary(engine) { $0.pending == 4 }.backedUp == 9)   // seeded before assess returns
    await gate.open()
}

@Test func syncNowDuringCountingSupersedesTheCount() async {
    let counting = Gate(); let release = Gate()
    let assessRuns = Counter()
    let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                perform: { _, _ in self.emptyReport() },
                                assess: { _ in assessRuns.inc(); await counting.open(); await release.wait()
                                                return Assessment(backedUp: 0, pending: 99, resourceTotal: 0) })
    await engine.start()
    await counting.wait()
    await engine.syncNow()                      // cancels the count
    _ = await awaitStatus(engine) { if case .syncing = $0 { return true }; if case .watching = $0 { return true }; return false }
    await release.open()
    await engine.settle()
    #expect(await awaitSummary(engine) { _ in true }.pending != 99)   // stale assessed dropped by the gate
}

@Test func startWhileCountingDoesNotRestartAssess() async {
    let counting = Gate(); let release = Gate()
    let assessRuns = Counter()
    let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                perform: { _, _ in self.emptyReport() },
                                assess: { _ in assessRuns.inc(); await counting.open(); await release.wait()
                                                return Assessment(backedUp: 0, pending: 0, resourceTotal: 0) })
    await engine.start()
    await counting.wait()
    await engine.start()                        // re-entry while counting: no duplicate assess
    await engine.settle()
    await release.open()
    await engine.settle()
    #expect(assessRuns.value == 1)
}
```

Also **rewrite** the migrated tests: `startRefreshesBackedUpFromServer` → uses `assess:` returning `Assessment(backedUp: 7, pending: 2, resourceTotal: 9)` and asserts `awaitSummary { $0.pending == 2 }.backedUp == 7`; the throw-fallback test uses `assess: { _ in throw Boom() }` and asserts the summary keeps the seeded/`.empty` values and status settles to `.watching`; the integration test's `refreshBackedUp` closure becomes an `assess` closure returning an `Assessment` (backedUp from server completes, pending from `SyncPlanner`, resourceTotal from the scan count).

- [ ] **Step 2: Run tests, verify they fail** — `swift test --filter LiveSyncEngineTests` → FAIL (compile: `assess:` param unknown).

- [ ] **Step 3: Swap the seam** — in `LiveSyncEngine` replace the `refreshBackedUp` property + init param with:

```swift
    private let assess: (@Sendable (_ onProgress: @Sendable (Int, Int) -> Void) async throws -> Assessment)?
    private let cachedAssessment: (@Sendable () -> Assessment?)?
```

and the init params (keep `now:` last):

```swift
                assess: (@Sendable (_ onProgress: @Sendable (Int, Int) -> Void) async throws -> Assessment)? = nil,
                cachedAssessment: (@Sendable () -> Assessment?)? = nil,
```

with `self.assess = assess; self.cachedAssessment = cachedAssessment`.

- [ ] **Step 4: Replace the command + add state.** In `enum Command` remove `.summaryRefreshed` and add:

```swift
        case counting(gen: UInt64, done: Int, total: Int)
        case assessFinished(gen: UInt64, Assessment?)   // nil = scan failed → leave .counting, keep summary
```

Add consumer state near `syncChild`: `private var assessChild: Task<Void, Never>?`. In `deinit` also `assessChild?.cancel()`.

- [ ] **Step 5: Handlers.** Replace the `.summaryRefreshed` handler with:

```swift
        case .counting(let gen, let done, let total):
            if gen == generation { setStatus(.counting(done: done, total: total)) }
        case .assessFinished(let gen, let a):
            if gen == generation {
                assessChild = nil
                if let a { setSummary(SyncSummary(backedUp: a.backedUp, pending: a.pending, failed: currentSummary.failed)) }
                setStatus(.watching(lastSync: lastSync))
            }
```

- [ ] **Step 6: `doStart` idle branch → count.** Replace the `if !isSyncingStatus { … scheduleBackedUpRefresh }` block with:

```swift
        if !isSyncingStatus && !isCountingStatus {
            generation &+= 1
            lastProgress = nil
            if let cached = cachedAssessment?() {
                setSummary(SyncSummary(backedUp: cached.backedUp, pending: cached.pending, failed: currentSummary.failed))
            }
            beginCounting(gen: generation)
        }
```

Add helpers:

```swift
    private var isCountingStatus: Bool { if case .counting = currentStatus { return true }; return false }

    private func beginCounting(gen: UInt64) {
        guard let assess else { setStatus(.watching(lastSync: lastSync)); return }
        setStatus(.counting(done: 0, total: 0))
        assessChild = Task { [assess, submit] in
            do {
                let a = try await assess { done, total in submit(.counting(gen: gen, done: done, total: total)) }
                submit(.assessFinished(gen: gen, a))
            } catch is CancellationError {
                // superseded (pause/syncNow/sign-out) already set the terminal status
            } catch {
                submit(.assessFinished(gen: gen, nil))
            }
        }
    }
```

- [ ] **Step 7: Supersession.** In the sign-out branch of `doStart`, and in `doPause`, `doResume`, `doSyncNow`, add `assessChild?.cancel(); assessChild = nil` alongside the existing `syncChild` handling (each already bumps `generation`, so late `.counting`/`.assessFinished` are gated out). Remove `finishSync`'s `scheduleBackedUpRefresh(gen:)` call and delete the `scheduleBackedUpRefresh` method entirely (`finishSync` already sets the summary with `pending` from the report — Task 2).

- [ ] **Step 8: Run tests** — `swift test --filter LiveSyncEngineTests` → PASS; then full `swift test` → PASS.

- [ ] **Step 9: Commit** — `git commit -m "feat(core): LiveSyncEngine assess pass (counting state + exact pending)"`.

---

### Task 5: App wiring — `FilesNestApp` assess + cache closures

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`

**Interfaces:**
- Consumes: `LiveSyncEngine(assess:cachedAssessment:)` (Task 4), `SyncStateStore` cache (Task 1), `SyncPlanner`, `PhotosAssetLibrary.resources(in:onProgress:)`.

- [ ] **Step 1: Replace the `refreshBackedUp:` closure** in the `LiveSyncEngine(...)` init with `assess:` + `cachedAssessment:`:

```swift
            assess: { onProgress in
                let scan = try await PhotosAssetLibrary().resources(in: .all, onProgress: onProgress)   // Counting…
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    let a = Assessment(backedUp: 0, pending: scan.count, resourceTotal: scan.count)
                    stateStore.saveAssessment(a); return a
                }
                let client = ServerClient(baseURL: url, credentials: credStore)
                var server: [UploadRecord] = []
                var cursor: String? = nil
                repeat {
                    let page = try await client.listUploads(cursor: cursor)
                    server += page.items
                    cursor = page.nextCursor
                } while cursor != nil
                let plan = SyncPlanner.plan(library: scan, server: server, range: .all)
                let a = Assessment(backedUp: server.filter { $0.status == .complete }.count,
                                   pending: plan.uploads.count,
                                   resourceTotal: scan.count)
                stateStore.saveAssessment(a)
                return a
            },
            cachedAssessment: { stateStore.loadAssessment() })
```

(Confirm `UploadRecord`, `ServerClient.listUploads`, `SyncPlanner`, `.all` are the exact names in use; adjust if the API differs.)

- [ ] **Step 2: Verify the type of `stateStore`** — it must be the `UserDefaultsSyncStateStore` instance already created in `init()` (the same one passed as `state:`), so the cache and last-sync share storage. No new store.

- [ ] **Step 3: Build** — app target won't fully build until Task 6 (PanelView). If desired, defer the build check to Task 6. Otherwise verify no *new* errors in `FilesNestApp.swift` by inspection.

- [ ] **Step 4: Commit** — `git commit -m "feat(app): assess closure (scan + server diff → exact pending) + assessment cache"`.

---

### Task 6: `PanelView` counting hero + Pending tile

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/PanelView.swift`
- Create: `apple/macos/FilesNest/FilesNest/CountLibraryVerification` note is a plan doc, not code — see Step 6.

**Interfaces:**
- Consumes: `SyncStatus.counting`, `SyncSummary.pending`.

- [ ] **Step 1: Invoke the SwiftUI skill** — announce and load `swiftui-expert:swiftui-expert-skill` for the hero/state changes.

- [ ] **Step 2: Handle `.counting` in every `SyncStatus` switch** in `PanelView`:
  - `glyph`: `.counting` → `""` (ring shows a spinner/progress, no glyph).
  - `title`: `.counting` → `"Counting…"`.
  - `subtitle`: `.counting(let done, let total)` → `total > 0 ? "\(done.formatted()) of \(total.formatted())" : "Scanning library…"`.
  - `ringColor`: `.counting` → `.blue`.
  - `ringFraction`: `.counting(let done, let total)` → `total > 0 ? CGFloat(done)/CGFloat(total) : 0`.

- [ ] **Step 3: Hero determinate ring while counting.** Extend `isScanning`-style handling: when `.counting` with `total == 0`, show the indeterminate `ProgressView` (as scanning does); when `total > 0`, show the trimmed ring at `ringFraction`. Add a computed `isCounting` and include it wherever `isScanning` drives the spinner.

- [ ] **Step 4: Pending tile from the summary.** Replace `pendingText`'s at-rest branch:

```swift
    private var pendingText: String {
        guard !isSignedOut else { return "—" }
        switch model.status {
        case .syncing, .paused: return "\(pending)"          // exact for the active run
        default: return model.summary.pending.map { "\($0)" } ?? "—"   // exact at-rest count, or — until first assess
        }
    }
```

Update the `pending` computed used for tile color similarly (`default:` → `model.summary.pending ?? 0`).

- [ ] **Step 5: Disable Pause while counting** — the Pause/Resume button: `.disabled(isSignedOut || isCounting)` (a count isn't pausable work). Sync Now stays enabled (it supersedes the count).

- [ ] **Step 6: Write the manual-verification checklist** — create `docs/plans/20260729-count-library-on-start-verification.md` covering: cold launch shows "Counting… N of M" ticking then the exact Pending; warm launch shows cached Backed up/Pending instantly then refreshes; Sync Now during counting interrupts and starts a sync; sign-out during counting → signed-out; Pause disabled during counting.

- [ ] **Step 7: Build the app** — run the app build command → **BUILD SUCCEEDED**.

- [ ] **Step 8: Commit** — `git commit -m "feat(app): counting hero + exact at-rest Pending tile"`.

---

### Task 7: Verify, review, finish

- [ ] **Step 1: Hammer the Core suite** — `for i in 1 2 3; do perl -e 'alarm 240; exec @ARGV' swift test; done` from `apple/FilesNestCore` → all green, no flakes.
- [ ] **Step 2: Build the app** — app build command → BUILD SUCCEEDED.
- [ ] **Step 3: Push the branch** — `git push -u origin apple/count-library-on-start`.
- [ ] **Step 4: Codex review** (standing rule) — hand the user a scoped `codex exec '…'` command covering: the assess child's cancellation/supersession vs the sync child, generation gating of `.counting`/`.assessFinished`, `NSLock` never across `await`, the `!isSyncingStatus && !isCountingStatus` guard, cache read/write correctness, and the app assess closure's unit consistency (backedUp = complete records, pending = `plan.uploads.count`, both per-resource). Address findings; re-run tests.
- [ ] **Step 5: Manual UI verification** — run the app; walk the Task 6 Step 6 checklist; capture that cold+warm launch behave as designed.
- [ ] **Step 6: Finish** — use superpowers:finishing-a-development-branch → create PR titled `Apple clients: Count library on start (#N)` with a summary; merge on approval; update `active-codebase` memory (mark this slice merged; note which §9 deferrals remain).

---

## Self-Review

- **Spec coverage:** SyncStatus.counting (T3), SyncSummary.pending (T2), Assessment+cache (T1), AssetLibrary progress (T3), LiveSyncEngine assess pass incl. supersession/finishSync (T4), app assess/cache (T5), PanelView hero+tile (T6), tests throughout, deferrals untouched (§9 of design). Covered.
- **Placeholder scan:** none — every code step has concrete code; the throttle constant (250) and count-failure→watching are design open items (§10) with concrete defaults chosen here.
- **Type consistency:** `Assessment(backedUp:pending:resourceTotal:)`, `SyncSummary(backedUp:pending:failed:)`, `SyncStatus.counting(done:total:)`, `assess:(_ onProgress:)`, `.assessFinished(gen:_:)` used identically across tasks.
