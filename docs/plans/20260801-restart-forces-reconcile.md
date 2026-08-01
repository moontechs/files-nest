# Restart Forces Reconcile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Settings-save forces a full `.all` reconcile with the new config, superseding any in-flight run — via a new `reconcile()` signal distinct from launch's `start()`.

**Architecture:** `SyncEngine.reconcile()` enqueues `Command.reconcile` → `doReconcile()`, which (unlike `doStart`) always cancels the in-flight run, resets the incremental anchor, and starts a fresh `.all` count+sync. `AppModel.restart()` calls it. `start()` stays launch-only, so its guard and tests are unchanged.

**Tech Stack:** Swift 6, swift-testing, Foundation (Core); SwiftUI (app target).

## Global Constraints

- `FilesNestCore` is PhotoKit-free; PhotoKit only in `apple/macos/FilesNest`.
- Swift 6 language mode; `NSLock` never held across `await`.
- Tests use swift-testing; fakes under `Tests/FilesNestCoreTests/Support/`.
- App target uses file-system-synchronized groups (drop `.swift` files, no `.pbxproj` edits).
- Commands run from `/Users/paulohenriquesg/Projects/filesnest/files-nest`.
- Core tests: `cd apple/FilesNestCore && swift test` (add `--filter <Name>`).
- App build: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
- Design doc: `docs/design/20260801-restart-forces-reconcile.md`. Ships as `Apple clients: Restart forces reconcile (#16)`.

## File Map

- `apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift` — add `reconcile()`.
- `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift` — `reconcile()` = `await start()`.
- `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift` — `Command.reconcile`, `reconcile()`, `doReconcile()`, `resetToSignedOut()` refactor.
- `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift` — new tests.
- `apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift` — stub test.
- `apple/macos/FilesNest/FilesNest/AppModel.swift` — `restart()` → `reconcile()`.
- `docs/plans/20260801-restart-forces-reconcile-verification.md` — manual checklist.

---

### Task 1: `reconcile()` — supersede + forced `.all` (Core)

**Files:**
- Modify: `SyncEngine.swift`, `StubSyncEngine.swift`, `LiveSyncEngine.swift`
- Test: `LiveSyncEngineTests.swift`, `StubSyncEngineTests.swift`

**Interfaces:**
- Produces: `SyncEngine.reconcile() async`. On `LiveSyncEngine`, reconcile supersedes any in-flight count/sync, resets `incrementalAnchor`, and starts a fresh `.all` count (autoSync true, or false while paused). `StubSyncEngine.reconcile()` = `await start()`.

- [ ] **Step 1: Add the protocol method + conformances (compile skeleton)**

In `SyncEngine.swift`, add to the protocol (after `libraryDidChange`):

```swift
    func reconcile() async   // a configuration change (Settings save): force a full reconcile now
```

In `StubSyncEngine.swift`, add (e.g. after `libraryDidChange`):

```swift
    public func reconcile() async { await start() }
```

In `LiveSyncEngine.swift`:
- Add the command case to the `Command` enum (after `case libraryChanged`):

```swift
        case reconcile
```

- Add the enqueue method (after `libraryDidChange`):

```swift
    public func reconcile() async { submit(.reconcile) }
```

- Add the handler arm in `handle(_:)` (after `case .start: await doStart()`):

```swift
        case .reconcile: await doReconcile()
```

- Add a temporary `doReconcile` that delegates to `doStart` so it compiles (Step 3 replaces it). Place it right after `doStart`:

```swift
    private func doReconcile() async { await doStart() }   // TEMP: Step 3 makes it supersede
```

- [ ] **Step 2: Run the full Core suite to confirm the skeleton is green**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS (reconcile currently behaves exactly like start; nothing regressed).

- [ ] **Step 3: Write the stub test**

In `StubSyncEngineTests.swift`, add:

```swift
    @Test func reconcileReconcilesCredentialsLikeStart() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.reconcile()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }
```

- [ ] **Step 4: Write the failing engine tests**

In `LiveSyncEngineTests.swift`, add a new MARK section:

```swift
    // MARK: - reconcile (Settings save → forced full .all)

    @Test func reconcileWhileSyncingSupersedesWithFreshAll() async {
        let performCalls = Counter()
        let firstStarted = Gate(); let hold = Gate(); let secondStarted = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
                                        let n = await performCalls.incAndGet()
                                        if n == 1 { await firstStarted.open(); await hold.wait() }
                                        if n == 2 { await secondStarted.open() }
                                        return self.emptyReport()
                                    },
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 1, resourceTotal: 1) })
        await engine.start()
        await firstStarted.wait()                 // launch .all sync #1 running (stalled)
        #expect(await performCalls.value == 1)
        await engine.reconcile()                  // Settings save mid-sync → supersede #1, fresh .all
        await secondStarted.wait()                // …a second sync actually starts (supersede confirmed)
        #expect(await performCalls.value == 2)
        await hold.open()                         // let the superseded #1 return (generation-dropped)
    }

    @Test func reconcileWhileCountingSupersedesTheCount() async {
        let assessRuns = Counter()
        let firstCounting = Gate(); let release = Gate(); let secondCounting = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _, _ in
                                        let n = await assessRuns.incAndGet()
                                        if n == 1 { await firstCounting.open(); await release.wait() }
                                        if n == 2 { await secondCounting.open() }
                                        return Assessment(backedUp: 0, pending: 0, resourceTotal: 0)
                                    })
        await engine.start()
        await firstCounting.wait()                // launch count #1 running (stalled in assess)
        await engine.reconcile()                  // supersede the count with a fresh .all count
        await secondCounting.wait()               // …a second assess runs (contrast: start() would NOT)
        #expect(await assessRuns.value == 2)
        await release.open()                      // let the superseded #1 return (generation-dropped)
    }

    @Test func reconcileWhilePausedRefreshesWithoutUploading() async {
        let performCalls = Counter()
        let firstStarted = Gate(); let hold = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
                                        let n = await performCalls.incAndGet()
                                        if n == 1 { await firstStarted.open(); await hold.wait() }
                                        return self.emptyReport()
                                    },
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 5, resourceTotal: 5) })
        await engine.start()
        await firstStarted.wait()                 // launch auto-sync (option A), stalled
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await hold.open()                         // cancelled #1 returns (dropped)
        await engine.reconcile()                  // Settings save while paused
        _ = await awaitStatus(engine, isWatching) // reconcile .all count settles to watching
        await engine.settle()
        #expect(await awaitStatus(engine) { _ in true } == .watching(lastSync: nil))   // stays watching (no sync)
        #expect(await performCalls.value == 1)    // did NOT upload while paused
    }

    @Test func reconcileResetsIncrementalAnchorSoNextChangeIsAll() async {
        let anchor = Date(timeIntervalSince1970: 5_000)
        let recorded = RangeBox()
        let pending = IntBox(1)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { range, _ in await recorded.add(range); return self.emptyReport() },
                                    assess: { range, _ in await recorded.add(range)
                                                          return Assessment(backedUp: 0, pending: await pending.value, resourceTotal: 0) },
                                    now: { anchor })
        // 1) Clean .all launch sync sets the anchor.
        var it0 = engine.statusStream().makeAsyncIterator()
        await engine.start()
        var s0 = false
        while let s = await it0.next() { if case .syncing = s { s0 = true }; if s0, case .watching = s { break } }
        // 2) Pause, then reconcile-while-paused: an .all count with NO sync → the anchor stays reset (nil).
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await engine.reconcile()
        _ = await awaitStatus(engine, isWatching)
        await engine.settle()
        // 3) A change must now scan .all — the reset anchor was not re-grounded (no sync ran).
        await recorded.clear()
        var it = engine.statusStream().makeAsyncIterator()
        await engine.libraryDidChange()
        var s1 = false
        while let s = await it.next() { if case .syncing = s { s1 = true }; if s1, case .watching = s { break } }
        let ranges = await recorded.all
        #expect(!ranges.isEmpty)
        #expect(ranges.allSatisfy { $0 == .all })   // anchor reset by reconcile → change fell back to .all
    }
```

- [ ] **Step 5: Run to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter "reconcileWhileSyncingSupersedesWithFreshAll"`
Expected: FAIL (hangs — the temp `doReconcile` == `doStart` no-ops during a sync, so the second sync never starts; kill it after it stalls). Same for `reconcileWhileCountingSupersedesTheCount` and `reconcileResetsIncrementalAnchorSoNextChangeIsAll`. (`reconcileWhilePausedRefreshesWithoutUploading` passes already — paused behavior is shared with `start()`.)

- [ ] **Step 6: Implement `doReconcile` + extract `resetToSignedOut`**

In `LiveSyncEngine.swift`, replace `doStart`'s signed-out branch with a call to a shared helper. The current branch is:

```swift
        guard creds != nil else {
            generation &+= 1                            // supersede any in-flight run
            signedIn = false
            syncChild?.cancel(); syncChild = nil
            assessChild?.cancel(); assessChild = nil
            lastProgress = nil                          // so a later idle Pause shows 0, not stale remaining
            pendingLibraryChange = false                // sign-out drops any coalesced change
            autoSyncRange = nil
            incrementalAnchor = nil                     // a fresh sign-in re-establishes the baseline via .all
            setStatus(.signedOut)
            setSummary(.empty)                          // drop stale failures
            return
        }
```

Change it to:

```swift
        guard creds != nil else { resetToSignedOut(); return }
```

Add the helper (place it right before `doStart`):

```swift
    /// Supersede any in-flight run and drop all reconciliation state, then publish signed-out.
    private func resetToSignedOut() {
        generation &+= 1                            // supersede any in-flight run
        signedIn = false
        syncChild?.cancel(); syncChild = nil
        assessChild?.cancel(); assessChild = nil
        lastProgress = nil                          // so a later idle Pause shows 0, not stale remaining
        pendingLibraryChange = false                // sign-out drops any coalesced change
        autoSyncRange = nil
        incrementalAnchor = nil                     // a fresh sign-in re-establishes the baseline via .all
        setStatus(.signedOut)
        setSummary(.empty)                          // drop stale failures
    }
```

Replace the temporary `doReconcile` with the real one:

```swift
    /// A Settings save (config change). Unlike `doStart`, always supersede any in-flight run —
    /// the config may repoint the app (e.g. new server), so old-config work must stop — and
    /// re-establish the incremental anchor via a forced `.all`.
    private func doReconcile() async {
        let creds = try? await credentials.basicCredentials()
        guard creds != nil else { resetToSignedOut(); return }
        signedIn = true
        syncChild?.cancel(); syncChild = nil        // stop old-config work
        assessChild?.cancel(); assessChild = nil
        incrementalAnchor = nil                     // config may have changed → re-ground via the forced .all
        if let cached = cachedAssessment?() {
            setSummary(SyncSummary(backedUp: cached.backedUp, pending: cached.pending, failed: currentSummary.failed))
        }
        if isPausedStatus {
            pendingLibraryChange = false
            startIdleCount(range: .all, autoSync: false)   // paused: refresh Pending, do NOT upload (#14 parity)
        } else {
            startIdleCount(range: .all, autoSync: true)    // force full reconcile + upload with new config
        }
    }
```

- [ ] **Step 7: Run the new tests, then the full suite ×3**

Run each: `cd apple/FilesNestCore && swift test --filter "reconcileWhileSyncingSupersedesWithFreshAll"` (then the other three reconcile tests, and `reconcileReconcilesCredentialsLikeStart`).
Expected: PASS all.
Run: `cd apple/FilesNestCore && swift test && swift test && swift test`
Expected: PASS, 3× clean. `startWhileCountingDoesNotRestartAssess` and `startDuringSyncDoesNotStrandTheEngine` still pass (start() unchanged).

- [ ] **Step 8: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift \
        apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift \
        apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift
git commit -m "Restart forces reconcile: reconcile() supersedes an active run with a forced .all"
```

---

### Task 2: Wire the app + manual checklist

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/AppModel.swift`
- Create: `docs/plans/20260801-restart-forces-reconcile-verification.md`

**Interfaces:**
- Consumes: `SyncEngine.reconcile()` (Task 1).

- [ ] **Step 1: Rewire `restart()` to `reconcile()`**

In `AppModel.swift`, change:

```swift
    /// Re-reconcile after credentials change (Settings save).
    func restart() { Task { await engine.start() } }
```

to:

```swift
    /// Force a full reconcile after a Settings save (config change) — supersedes any active run.
    func restart() { Task { await engine.reconcile() } }
```

- [ ] **Step 2: Build the app**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
Expected: BUILD SUCCEEDED.

- [ ] **Step 3: Write the manual-verify checklist**

Create `docs/plans/20260801-restart-forces-reconcile-verification.md`:

```markdown
# Restart forces reconcile — manual verification

Prereqs: signed in; a photo library backed up to a steady state. Watch the console
(`🟢 FN library: enumeration start (range=…)`, `🟣 FN engine`).

- [ ] **Save Settings mid-sync.** Trigger a change so a sync is running, then open
      Settings and Save. The in-flight run is superseded and a fresh `range=all`
      count+sync starts immediately (not deferred).
- [ ] **Change the server URL mid-sync** (point at a second, empty server) and Save.
      The new server receives the full backup (`range=all`), not just a window.
- [ ] **Save Settings while paused.** Reconcile refreshes Pending (`range=all` count)
      but does NOT upload; status stays paused-then-watching without a sync.
- [ ] **Save Settings while idle.** Behaves like before (an `.all` count + catch-up).
- [ ] After a reconcile, the next library change first scans `range=all` (anchor
      reset), then returns to `range=modifiedSince(…)` once a clean sync re-grounds it.
```

- [ ] **Step 4: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/AppModel.swift \
        docs/plans/20260801-restart-forces-reconcile-verification.md
git commit -m "Restart forces reconcile: route AppModel.restart() to reconcile() + checklist"
```

- [ ] **Step 5: Manual verification**

Run the app and walk the checklist, recording surprises before opening the PR.

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** §3 seam = Task 1 Step 1 + Task 2; §4 `doReconcile`/`resetToSignedOut` = Task 1 Step 6; §5 anchor reset = `incrementalAnchor = nil` in `doReconcile` + `reconcileResetsIncrementalAnchorSoNextChangeIsAll`; §6 tests = Task 1 Steps 3–4; §7 checklist = Task 2 Step 3.
- **`start()` is unchanged** — the two `start()`-during-run tests stay green; only `reconcile()` supersedes.
- **Type consistency:** `reconcile()`, `Command.reconcile`, `doReconcile()`, `resetToSignedOut()` used identically across steps. Reuses existing helpers (`startIdleCount(range:autoSync:)`, `incrementalAnchor`, `isPausedStatus`) and test fakes (`Counter.incAndGet`, `Gate`, `IntBox`, `RangeBox`).
```
