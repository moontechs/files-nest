# Continuous Watching Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the macOS menu-bar agent automatically count and back up when the photo library changes (and catch up on launch), without a manual Sync Now.

**Architecture:** A `PHPhotoLibraryChangeObserver` in the app target debounces change bursts, invalidates the cached scan, and signals Core via a new `SyncEngine.libraryDidChange()`. `LiveSyncEngine` reacts with count-then-sync when idle, coalesces changes that arrive mid-run, and honors changes on resume. Everything stays `SyncRange.all` (incremental deferred), so the existing report-derived summary is unchanged.

**Tech Stack:** Swift 6 (strict concurrency), swift-testing (`import Testing`), PhotoKit (app target only), Foundation.

## Global Constraints

- `FilesNestCore` is **PhotoKit-free by construction** — no `import Photos` in the package. PhotoKit lives only in the app target `apple/macos/FilesNest`.
- Swift 6 language mode; no data races. `NSLock` never held across `await`.
- Tests use swift-testing (`import Testing`, `@Test`, `#expect`). Test fakes live under `Tests/FilesNestCoreTests/Support/`.
- The macOS app target uses **file-system-synchronized groups**: add `.swift` files by dropping them in the target folder `apple/macos/FilesNest/FilesNest/` — no `.pbxproj` edits.
- All commands below run from the repo subdir `files-nest/` (i.e. `/Users/paulohenriquesg/Projects/filesnest/files-nest`).
- Core test command: `cd apple/FilesNestCore && swift test` (add `--filter <Name>` for one test/suite).
- App build command: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
- Design doc: `docs/design/20260729-continuous-watching.md`. Ships as `Apple clients: Continuous watching (#13)`.

## File Map

- Modify `apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift` — add `libraryDidChange()` to the protocol.
- Modify `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift` — no-op `libraryDidChange()`.
- Modify `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift` — auto-sync chaining, `.libraryChanged` command + reaction, coalescing, drain.
- Modify `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift` — update 2 existing tests, add new tests.
- Modify `apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift` — assert no-op.
- Create `apple/macos/FilesNest/FilesNest/PhotoLibraryWatcher.swift` — the observer.
- Modify `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` — TTL backstop + wire watcher.
- Create `docs/plans/20260730-continuous-watching-verification.md` — manual-verify checklist.

---

### Task 1: `SyncEngine.libraryDidChange()` seam

Adds the protocol method both engines must satisfy. Ships with the stub's no-op so the package still builds.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift`

**Interfaces:**
- Produces: `func libraryDidChange() async` on `protocol SyncEngine` — a debounced host signal that the photo library changed. `StubSyncEngine` implements it as a no-op.

- [x] **Step 1: Add the protocol method**

In `SyncEngine.swift`, add to the protocol body (after `func syncNow() async`):

```swift
    func libraryDidChange() async   // a debounced host signal that the photo library changed
```

- [x] **Step 2: Add the stub no-op**

In `StubSyncEngine.swift`, add (e.g. after `syncNow()`):

```swift
    public func libraryDidChange() async {}   // the stub does not watch; no-op
```

- [x] **Step 3: Make `LiveSyncEngine` conform (so the package compiles)**

Adding a protocol requirement breaks `LiveSyncEngine`'s conformance. Wire the real enqueue now (its handler is a no-op until Task 3). In `LiveSyncEngine.swift`:

Add the `.libraryChanged` case to the private `Command` enum (e.g. after `case start, pause, resume, syncNow`):

```swift
        case libraryChanged
```

Add the public method in the "SyncEngine (enqueue only)" section:

```swift
    public func libraryDidChange() async { submit(.libraryChanged) }
```

Add a temporary arm in `handle(_:)` so the switch stays exhaustive (Task 3 fills it in):

```swift
        case .libraryChanged: break
```

- [x] **Step 4: Write the failing test**

In `StubSyncEngineTests.swift`, add a test that the no-op changes nothing. (Match the existing file's style; it already builds a `StubSyncEngine`.)

```swift
    @Test func libraryDidChangeIsANoOp() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.start()
        await engine.libraryDidChange()
        // still whatever start() produced; no crash, no state change
        var it = engine.statusStream().makeAsyncIterator()
        let s = await it.next()
        #expect(s != nil)
    }
```

- [x] **Step 5: Build & run the stub suite**

Run: `cd apple/FilesNestCore && swift test --filter StubSyncEngineTests`
Expected: PASS (the package compiles — both engines now conform).

- [x] **Step 6: Run the full Core suite to confirm green**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS (all existing tests still green; `.libraryChanged` is an inert no-op for now).

- [x] **Step 7: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift \
        apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift \
        apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift
git commit -m "Continuous watching: add SyncEngine.libraryDidChange seam"
```

---

### Task 2: Launch/idle auto-sync (option A)

When a count settles with pending work, the engine auto-syncs it. This makes launch (and Settings-restart) catch up automatically. It also introduces the count→sync chaining that Task 3's change reaction reuses.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: existing `doStart`, `beginCounting(gen:)`, `doSyncNow`, `.assessFinished` handling.
- Produces: `private func startIdleCount(autoSync: Bool)`; consumer-only `private var autoSyncAfterCount = false`. When an assess settles and `autoSyncAfterCount` is true and `Assessment.pending > 0`, the engine calls `doSyncNow()`.

- [x] **Step 1: Update the two existing tests that now trigger a launch auto-sync**

Under option A, any `start()` whose `assess` returns `pending > 0` will auto-sync. Two existing tests in `LiveSyncEngineTests.swift` assert on the post-count summary/status and must be updated to gate `perform` so the count result stays observable.

Replace `startCountsThenAssesses` with:

```swift
    @Test func startCountsThenAssesses() async {
        let hold = Gate()   // stall the launch auto-sync so the count summary is observable
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await hold.wait(); return self.emptyReport() },
                                    assess: { progress in
                                        progress(3, 10); progress(10, 10)
                                        return Assessment(backedUp: 5, pending: 7, resourceTotal: 12)
                                    })
        await engine.start()
        let sum = await awaitSummary(engine) { $0.pending == 7 }
        #expect(sum.backedUp == 5)                                    // count surfaced the assessment
        #expect(isSyncing(await awaitStatus(engine, isSyncing)))     // launch auto-syncs the pending work (option A)
        await hold.open()
    }
```

Replace `signOutClearsSummary` with:

```swift
    @Test func signOutClearsSummary() async {
        let hold = Gate()
        let creds = MutableCreds(.init(username: "u", password: "p"))
        let engine = LiveSyncEngine(credentials: creds, state: InMemorySyncStateStore(),
                                    perform: { _, _ in await hold.wait(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 9, pending: 2, resourceTotal: 11) })
        await engine.start()
        #expect(await awaitSummary(engine) { $0.backedUp == 9 } == SyncSummary(backedUp: 9, pending: 2, failed: []))
        creds.set(nil)
        await engine.start()
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
        #expect(await awaitSummary(engine) { $0 == .empty } == .empty)
        await hold.open()   // let the stranded (stale-generation) auto-sync return; its result is dropped
    }
```

- [x] **Step 2: Write the new failing tests**

Add to `LiveSyncEngineTests.swift`:

```swift
    // MARK: - launch auto-sync (option A)

    @Test func launchAutoSyncsWhenCountFindsPending() async {
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 0, pending: 1, resourceTotal: 1) })
        await engine.start()
        _ = await awaitStatus(engine, isSyncing)            // count found pending → auto-sync started
        _ = await awaitStatus(engine, isWatching)           // sync completed
        #expect(await performCalls.value == 1)
    }

    @Test func launchDoesNotSyncWhenNothingPending() async {
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 5, pending: 0, resourceTotal: 5) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)           // count settles, no sync
        await engine.settle()
        #expect(await performCalls.value == 0)
    }
```

- [x] **Step 3: Run to verify the new tests fail**

Run: `cd apple/FilesNestCore && swift test --filter launchAutoSyncsWhenCountFindsPending`
Expected: FAIL (no auto-sync yet — `performCalls` stays 0, never reaches `.syncing`).

- [x] **Step 4: Implement the chaining**

In `LiveSyncEngine.swift`:

Add the consumer-only flag near the other consumer state (after `syncBaseBackedUp`):

```swift
    private var autoSyncAfterCount = false   // whether the in-flight count should chain into a sync when it settles
```

Add the shared helper (place it right before `beginCounting`):

```swift
    /// Bump the generation and start an off-consumer count. If `autoSync` and the count finds
    /// pending work, `.assessFinished` chains into a sync (count-then-upload).
    private func startIdleCount(autoSync: Bool) {
        guard signedIn else { return }
        generation &+= 1
        lastProgress = nil
        autoSyncAfterCount = autoSync
        beginCounting(gen: generation)
    }
```

Replace `doStart`'s idle branch body. The current code is:

```swift
        if !isSyncingStatus && !isCountingStatus {
            generation &+= 1
            lastProgress = nil                         // reconciling to idle; drop any paused-run remaining
            if let cached = cachedAssessment?() {      // warm launch: show last-known instantly
                setSummary(SyncSummary(backedUp: cached.backedUp, pending: cached.pending, failed: currentSummary.failed))
            }
            beginCounting(gen: generation)             // off-consumer scan → exact pending
        }
```

Change it to (seed stays here; gen bump + lastProgress move into `startIdleCount`):

```swift
        if !isSyncingStatus && !isCountingStatus {
            if let cached = cachedAssessment?() {      // warm launch: show last-known instantly
                setSummary(SyncSummary(backedUp: cached.backedUp, pending: cached.pending, failed: currentSummary.failed))
            }
            startIdleCount(autoSync: true)             // launch/restart catch-up (option A)
        }
```

Update the `.assessFinished` handler. The current code is:

```swift
        case .assessFinished(let gen, let a):
            if gen == generation {
                assessChild = nil
                if let a { setSummary(SyncSummary(backedUp: a.backedUp, pending: a.pending, failed: currentSummary.failed)) }
                setStatus(.watching(lastSync: lastSync))
            }
```

Change it to:

```swift
        case .assessFinished(let gen, let a):
            if gen == generation {
                assessChild = nil
                if let a { setSummary(SyncSummary(backedUp: a.backedUp, pending: a.pending, failed: currentSummary.failed)) }
                setStatus(.watching(lastSync: lastSync))
                let shouldSync = autoSyncAfterCount && (a?.pending ?? 0) > 0
                autoSyncAfterCount = false
                if shouldSync { doSyncNow() }
            }
```

- [x] **Step 5: Run the new tests**

Run: `cd apple/FilesNestCore && swift test --filter launchAutoSyncsWhenCountFindsPending`
then: `cd apple/FilesNestCore && swift test --filter launchDoesNotSyncWhenNothingPending`
Expected: PASS both.

- [x] **Step 6: Run the full Core suite**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS — including the two updated tests. (If `startCountsThenAssesses` or `signOutClearsSummary` fail, re-check the Step 1 rewrites.)

- [x] **Step 7: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "Continuous watching: auto-sync pending work after a count (option A)"
```

---

### Task 3: `.libraryChanged` reaction — count/sync, coalescing, drain

Wires the `.libraryChanged` command (already a no-op arm from Task 1) into the real state machine: idle → count-then-sync; mid-run → coalesce; paused → honor on resume; signed out → ignore. Drains coalesced changes when a run finishes or the engine resumes.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `startIdleCount(autoSync:)` and the `.assessFinished`→`doSyncNow` chaining from Task 2.
- Produces: consumer-only `private var pendingLibraryChange = false`; `private func drainPendingChangeIfAny()`. `.libraryChanged` reaction (see table). Drain is called from `finishSync` and `doResume`; `doStart`'s signed-out branch resets both flags.

- [x] **Step 1: Add the test-support box, then write the failing tests**

At the bottom of `LiveSyncEngineTests.swift` (near `Counter`), add a small mutable box so `assess` can return different `pending` before and after a change:

```swift
/// A pending value a test can flip between assess calls.
actor IntBox {
    private(set) var value: Int
    init(_ v: Int) { value = v }
    func set(_ v: Int) { value = v }
}
```

Add these tests (new MARK section):

```swift
    // MARK: - continuous watching (libraryDidChange)

    @Test func changeWhileIdleCountsThenSyncs() async {
        let box = IntBox(0)                      // launch finds nothing → no launch sync
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)          // launch count (pending 0) → watching, no sync
        await engine.settle()
        #expect(await performCalls.value == 0)

        await box.set(1)                                    // now a change would find work
        var it = engine.statusStream().makeAsyncIterator()
        await engine.libraryDidChange()
        var sawSyncing = false
        while let s = await it.next() { if case .syncing = s { sawSyncing = true; break } }
        #expect(sawSyncing)                                 // change → count → sync
        _ = await awaitStatus(engine, isWatching)           // sync completed
        #expect(await performCalls.value == 1)              // exactly the change-triggered sync
    }

    @Test func changeWhileSyncingCoalescesOneFollowUp() async {
        let box = IntBox(1)                      // launch finds work → launch auto-sync #1
        let performCalls = Counter()
        let hold = Gate()                        // stall only the first sync
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
                                        let n = await performCalls.incAndGet()
                                        if n == 1 { await hold.wait() }
                                        return self.emptyReport()
                                    },
                                    assess: { _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
        await engine.start()
        _ = await awaitStatus(engine, isSyncing)            // sync #1 running (stalled on hold)
        await engine.libraryDidChange()                     // change mid-sync → coalesced
        await engine.settle()
        await hold.open()                                   // sync #1 finishes → drain → count → sync #2
        _ = await awaitStatus(engine, isWatching)
        // settle a couple of command round-trips so sync #2's finish is processed
        await engine.settle(); await engine.settle()
        #expect(await performCalls.value == 2)              // exactly one follow-up sync, no loop
    }

    @Test func changeWhilePausedIsHeldUntilResume() async {
        let box = IntBox(0)                      // launch finds nothing
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await box.set(1)
        await engine.libraryDidChange()                     // change while paused → held, no sync
        await engine.settle()
        #expect(await performCalls.value == 0)
        await engine.resume()                               // resume drains the held change → count → sync
        _ = await awaitStatus(engine, isSyncing)
        _ = await awaitStatus(engine, isWatching)
        #expect(await performCalls.value == 1)
    }

    @Test func changeWhileSignedOutIsIgnored() async {
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(false), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 0, pending: 9, resourceTotal: 9) })
        await engine.start()
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
        await engine.libraryDidChange()
        await engine.settle()
        #expect(await performCalls.value == 0)
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
    }

    @Test func changeWithNothingNewDoesNotSyncOrLoop() async {
        let assessCalls = Counter()
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in await assessCalls.inc(); return Assessment(backedUp: 3, pending: 0, resourceTotal: 3) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)           // launch count, no sync
        await engine.libraryDidChange()                     // change → count → pending 0 → no sync
        _ = await awaitSummary(engine) { $0.backedUp == 3 }
        await engine.settle()
        #expect(await performCalls.value == 0)              // never synced
        #expect(await assessCalls.value == 2)              // launch + one change count; no runaway loop
    }

    @Test func libraryDidChangeBeforeStartDoesNothing() async {
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _ in Assessment(backedUp: 0, pending: 1, resourceTotal: 1) })
        await engine.libraryDidChange()                     // before start(): not signed in yet → ignored
        await engine.settle()
        #expect(await performCalls.value == 0)
    }
```

Add a helper to the `Counter` actor so `changeWhileSyncingCoalescesOneFollowUp` can read the count atomically:

```swift
    func incAndGet() -> Int { value += 1; return value }
```

- [x] **Step 2: Run to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter "changeWhileIdleCountsThenSyncs"`
Expected: FAIL (`.libraryChanged` is still the inert `break` arm from Task 1; no sync happens).

- [x] **Step 3: Implement the reaction**

In `LiveSyncEngine.swift`:

Add the consumer-only flag next to `autoSyncAfterCount`:

```swift
    private var pendingLibraryChange = false   // a change arrived mid-run; drain when the run finishes
```

Replace the temporary `.libraryChanged` arm (`case .libraryChanged: break`) in `handle(_:)` with:

```swift
        case .libraryChanged:
            guard signedIn else { break }                 // ignore while signed out / before start
            switch currentStatus {
            case .watching, .error:
                startIdleCount(autoSync: true)            // idle → count, then sync if pending
            case .syncing, .counting:
                pendingLibraryChange = true               // coalesce; drained when the run finishes
            case .paused:
                pendingLibraryChange = true               // honored on resume (never upload while paused)
            case .signedOut:
                break
            }
```

Add the drain helper (place near `startIdleCount`):

```swift
    /// If a library change was coalesced while a run was in flight, start a fresh count now
    /// (which chains into a sync if anything is pending). Consumer-only.
    private func drainPendingChangeIfAny() {
        guard pendingLibraryChange else { return }
        pendingLibraryChange = false
        startIdleCount(autoSync: true)
    }
```

In `finishSync`, add a drain as the last line (after `setStatus(.watching(lastSync: lastSync))`):

```swift
        drainPendingChangeIfAny()
```

In `doResume`, add a drain as the last line (after `setStatus(.watching(lastSync: lastSync))`):

```swift
        drainPendingChangeIfAny()
```

Update the `.assessFinished` handler's last line (from Task 2) to also drain when the count did **not** chain a sync — otherwise a change coalesced *during a count* that finds nothing pending is stranded (no `finishSync` is coming to drain it). Change:

```swift
                if shouldSync { doSyncNow() }
```

to:

```swift
                if shouldSync { doSyncNow() } else { drainPendingChangeIfAny() }
```

In `doStart`'s signed-out branch, reset both flags. The current branch clears `lastProgress` before `setStatus(.signedOut)`; add alongside it:

```swift
            pendingLibraryChange = false
            autoSyncAfterCount = false
```

Note: do **not** drain in the `.failed` handler or in `doPause` — a persistent error must not spin a retry loop, and a paused change must survive until resume.

- [x] **Step 4: Run the new tests**

Run each:
```
cd apple/FilesNestCore && swift test --filter "changeWhileIdleCountsThenSyncs"
cd apple/FilesNestCore && swift test --filter "changeWhileSyncingCoalescesOneFollowUp"
cd apple/FilesNestCore && swift test --filter "changeWhilePausedIsHeldUntilResume"
cd apple/FilesNestCore && swift test --filter "changeWhileSignedOutIsIgnored"
cd apple/FilesNestCore && swift test --filter "changeWithNothingNewDoesNotSyncOrLoop"
cd apple/FilesNestCore && swift test --filter "libraryDidChangeBeforeStartDoesNothing"
```
Expected: PASS all.

- [x] **Step 5: Run the full Core suite (hammer for flakes)**

Run: `cd apple/FilesNestCore && swift test && swift test && swift test`
Expected: PASS all three runs (the command loop is deterministic via `settle()`; repeat guards against ordering flakes).

- [x] **Step 6: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "Continuous watching: react to library-change signal with count/sync + coalescing"
```

---

### Task 4: App-target watcher + composition wiring + verification checklist

Adds the PhotoKit observer, switches the cache to change-based invalidation with a TTL backstop, and documents the manual-verify pass (the app is not unit-tested).

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/PhotoLibraryWatcher.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Create: `docs/plans/20260730-continuous-watching-verification.md`

**Interfaces:**
- Consumes: `SyncEngine.libraryDidChange()` (Task 1), `CachingAssetLibrary.invalidate()` (existing).
- Produces: `final class PhotoLibraryWatcher: NSObject, PHPhotoLibraryChangeObserver` with `init(library:engine:debounce:)` and `func startObserving()`, retained by `FilesNestApp`.

- [x] **Step 1: Create the watcher**

Create `apple/macos/FilesNest/FilesNest/PhotoLibraryWatcher.swift`:

```swift
import Photos
import FilesNestCore

/// Watches the photo library and, after a burst settles, refreshes the cached scan and
/// nudges the engine to count + back up. PhotoKit lives here (app target); Core stays
/// PhotoKit-free and only learns "the library changed" via `engine.libraryDidChange()`.
final class PhotoLibraryWatcher: NSObject, PHPhotoLibraryChangeObserver {
    private let library: CachingAssetLibrary
    private let engine: any SyncEngine
    private let debounce: Duration
    private var debounceTask: Task<Void, Never>?   // MainActor-isolated below

    init(library: CachingAssetLibrary, engine: any SyncEngine, debounce: Duration = .seconds(2)) {
        self.library = library
        self.engine = engine
        self.debounce = debounce
        super.init()
    }

    /// Register for change notifications. Harmless before photo-library authorization —
    /// it simply won't fire until access is granted (the app requests auth on first scan).
    func startObserving() { PHPhotoLibrary.shared().register(self) }

    // Called by PhotoKit on an arbitrary queue.
    nonisolated func photoLibraryDidChange(_ changeInstance: PHChange) {
        Task { @MainActor in self.scheduleFlush() }
    }

    @MainActor private func scheduleFlush() {
        debounceTask?.cancel()   // coalesce a burst into one flush after quiescence
        debounceTask = Task { [library, engine, debounce] in
            try? await Task.sleep(for: debounce)
            if Task.isCancelled { return }
            await library.invalidate()        // fresh scan next; app owns invalidation
            await engine.libraryDidChange()   // then notify Core
        }
    }

    deinit { PHPhotoLibrary.shared().unregisterChangeObserver(self) }
}
```

- [x] **Step 2: Wire it into the composition root**

In `FilesNestApp.swift`:

Change the library construction to add the TTL backstop:

```swift
        let library    = CachingAssetLibrary(wrapping: PhotosAssetLibrary(), ttl: 300)
```

After the `engine` is built (and before `let appModel = AppModel(engine: engine)`), create and start the watcher, and retain it on the app struct. Add a stored property:

```swift
    private let watcher: PhotoLibraryWatcher
```

At the end of `init()` (after `engine` exists), assign and start it:

```swift
        let watcher = PhotoLibraryWatcher(library: library, engine: engine)
        watcher.startObserving()
        self.watcher = watcher
```

(Assign `self.watcher` before the `_model`/`_settings` StateObject assignments if the compiler complains about definite initialization ordering; a stored `let` must be set before `init` returns.)

- [x] **Step 3: Build the app**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
Expected: BUILD SUCCEEDED.

- [x] **Step 4: Write the manual-verify checklist**

Create `docs/plans/20260730-continuous-watching-verification.md`:

```markdown
# Continuous Watching — manual verification

Prereqs: signed in (server URL + credentials set), library backed up to a steady
state (Pending 0, `.watching`).

- [x] **Add one photo.** Within ~2–3s the panel shows Counting → then a short sync,
      Backed-up climbs by 1, returns to Watching, Pending 0.
- [x] **Import a burst** (e.g. AirDrop 20 photos). A *single* coalesced count+sync
      runs after the burst settles (not 20 separate syncs).
- [x] **Add a photo mid-sync** (add during a large sync). After the current sync
      finishes, a follow-up count+sync picks up the new photo.
- [x] **Pause, then add a photo.** Nothing syncs while paused. On Resume, the held
      change is counted and synced.
- [x] **Relaunch with a backlog** (delete a server item or add photos while the app
      is closed, then launch). The app counts and auto-syncs the backlog on launch.
- [x] **No feedback loop.** After a sync completes it stays at Watching — it does not
      re-trigger itself from its own upload activity.
```

- [x] **Step 5: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/PhotoLibraryWatcher.swift \
        apple/macos/FilesNest/FilesNest/FilesNestApp.swift \
        docs/plans/20260730-continuous-watching-verification.md
git commit -m "Continuous watching: PhotoKit observer + change-based cache invalidation"
```

- [x] **Step 6: Perform the manual verification**

Run the app and walk the checklist above, ticking each item. Record any surprises in the verification doc before opening the PR.

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** Task 1 = seam (§4.1); Task 2 = launch auto-sync option A (§2, §4.2 chaining); Task 3 = `.libraryChanged` reaction, coalescing, drain (§4.2 table + drain); Task 4 = watcher + TTL backstop + wiring + manual checklist (§5, §7.2).
- **Deferred (do NOT implement here):** incremental `.dates` range, determinate progress on the sync's own scan, `resourceTotal` display, auto-sync policy knobs (§8).
- **Type consistency:** `startIdleCount(autoSync:)`, `drainPendingChangeIfAny()`, `pendingLibraryChange`, `autoSyncAfterCount`, `Command.libraryChanged`, `libraryDidChange()` are used identically across Tasks 1–3. `CachingAssetLibrary(wrapping:ttl:)` and `.invalidate()` already exist.
- **Everything stays `SyncRange.all`** — `finishSync` is unchanged; no range-carrying commands.
```
