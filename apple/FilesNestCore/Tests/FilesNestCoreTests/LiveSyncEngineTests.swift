import Testing
import Foundation
@testable import FilesNestCore

@Suite struct LiveSyncEngineTests {

    // MARK: - Helpers

    func creds(_ present: Bool) -> StaticCredentialStore {
        StaticCredentialStore(present ? .init(username: "u", password: "p") : nil)
    }

    func emptyReport() -> SyncReport { SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0) }

    /// Waits until a status matching `pred` is observed (current-value-first stream).
    func awaitStatus(_ e: any SyncEngine, _ pred: @escaping @Sendable (SyncStatus) -> Bool) async -> SyncStatus {
        for await s in e.statusStream() where pred(s) { return s }
        return .signedOut
    }

    /// Waits until a summary matching `pred` is observed (current-value-first stream).
    func awaitSummary(_ e: any SyncEngine, _ pred: @escaping @Sendable (SyncSummary) -> Bool) async -> SyncSummary {
        for await s in e.summaryStream() where pred(s) { return s }
        return .empty
    }

    func isWatching(_ s: SyncStatus) -> Bool { if case .watching = s { return true }; return false }
    func isPaused(_ s: SyncStatus) -> Bool { if case .paused = s { return true }; return false }
    func isSyncing(_ s: SyncStatus) -> Bool { if case .syncing = s { return true }; return false }
    func isError(_ s: SyncStatus) -> Bool { if case .error = s { return true }; return false }

    // MARK: - start / signed-out

    @Test func startWithoutCredentialsIsSignedOut() async {
        let engine = LiveSyncEngine(credentials: creds(false), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() })
        await engine.start(); await engine.settle()
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
    }

    @Test func startWithCredentialsIsWatchingWithStoredLastSync() async {
        let state = InMemorySyncStateStore()
        let d = Date(timeIntervalSince1970: 1_700_000_000)
        state.saveLastSyncStarted(d)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, _ in self.emptyReport() })
        await engine.start(); await engine.settle()
        #expect(await awaitStatus(engine, isWatching) == .watching(lastSync: d))
    }

    // MARK: - summary sourcing

    @Test func summaryStartsEmpty() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() })
        #expect(await awaitSummary(engine) { _ in true } == .empty)
    }

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

    @Test func summaryReflectsReportAfterSync() async {
        let uploaded = ResourceKey(localIdentifier: "A", kind: .photo)
        let f = FailedItem(key: ResourceKey(localIdentifier: "B", kind: .photo), filename: "B.jpg", reason: "boom")
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [uploaded], deleted: [], failed: [f], skipped: 3)
        })
        await engine.start()
        await engine.syncNow()
        let sum = await awaitSummary(engine) { $0.backedUp == 4 && !$0.failed.isEmpty }   // 3 skipped + 1 uploaded
        #expect(sum.failed == [f])
        #expect(sum.pending == 1)              // pending == upload-failure count, straight from the report
    }

    @Test func pendingAfterSyncCountsUploadFailuresOnly() async {
        let up = FailedItem(key: ResourceKey(localIdentifier: "U", kind: .photo), filename: "U.jpg", reason: "x", kind: .upload)
        let del = FailedItem(key: ResourceKey(localIdentifier: "D", kind: .photo), filename: "D", reason: "y", kind: .delete)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in SyncReport(uploaded: [], deleted: [], failed: [up, del], skipped: 2) })
        await engine.start()
        await engine.syncNow()
        let sum = await awaitSummary(engine) { $0.failed.count == 2 }
        #expect(sum.pending == 1)              // only the upload failure — a failed delete isn't pending upload
        #expect(sum.backedUp == 2)             // skipped(2) + uploaded(0)
    }

    @Test func assessFailureFallsBackToWatching() async {
        struct Boom: Error {}
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _ in throw Boom() })
        await engine.start()
        #expect(isWatching(await awaitStatus(engine, isWatching)))
        #expect(await awaitSummary(engine) { _ in true } == .empty)   // no cache → stays empty
    }

    @Test func syncNowDuringCountingSupersedesTheCount() async {
        let counting = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _ in await counting.open(); await release.wait()
                                                    return Assessment(backedUp: 0, pending: 99, resourceTotal: 0) })
        await engine.start()
        await counting.wait()                       // assess is running
        await engine.syncNow()                      // cancels the count, starts a sync
        _ = await awaitStatus(engine, isWatching)   // empty-report sync completes → watching
        await release.open()
        await engine.settle()                       // process (and drop) the stale assessFinished
        #expect(await awaitSummary(engine) { _ in true }.pending != 99)   // stale assessed dropped by the gate
    }

    @Test func startWhileCountingDoesNotRestartAssess() async {
        let counting = Gate(); let release = Gate()
        let assessRuns = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _ in await assessRuns.inc(); await counting.open(); await release.wait()
                                                    return Assessment(backedUp: 0, pending: 0, resourceTotal: 0) })
        await engine.start()
        await counting.wait()
        await engine.start()                        // re-entry while counting: no duplicate assess
        await engine.settle()
        await release.open()
        await engine.settle()
        #expect(await assessRuns.value == 1)
    }

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

    @Test func restartWhilePausedDoesNotAutoSync() async {
        let performCalls = Counter()
        let firstStarted = Gate(); let hold = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
                                        let n = await performCalls.incAndGet()
                                        if n == 1 { await firstStarted.open(); await hold.wait() }
                                        return self.emptyReport()
                                    },
                                    assess: { _ in Assessment(backedUp: 0, pending: 5, resourceTotal: 5) })
        await engine.start()
        await firstStarted.wait()                           // launch auto-sync of the 5 pending (option A) has started
        #expect(await performCalls.value == 1)
        await engine.pause()                                // user pauses
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await hold.open()                                   // cancelled sync's perform returns (dropped by generation)
        await engine.start()                                // Settings save → restart while paused
        _ = await awaitStatus(engine, isWatching)           // reconciles to watching…
        await engine.settle()
        #expect(await performCalls.value == 1)              // …but does NOT auto-upload while paused
    }

    @Test func restartWhilePausedWithCoalescedChangeDoesNotAutoSync() async {
        let performCalls = Counter()
        let firstStarted = Gate(); let hold = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
                                        let n = await performCalls.incAndGet()
                                        if n == 1 { await firstStarted.open(); await hold.wait() }
                                        return self.emptyReport()
                                    },
                                    assess: { _ in Assessment(backedUp: 0, pending: 5, resourceTotal: 5) })
        await engine.start()
        await firstStarted.wait()                           // launch auto-sync of the 5 pending (option A), stalled
        await engine.pause()                                // user pauses
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await engine.libraryDidChange()                     // a change arrives WHILE paused → coalesced
        await engine.settle()
        await hold.open()                                   // cancelled sync's perform returns (dropped by generation)
        await engine.start()                                // Settings save → restart while paused
        _ = await awaitStatus(engine, isWatching)
        await engine.settle(); await engine.settle(); await engine.settle()
        #expect(await performCalls.value == 1)              // the coalesced-during-pause change must NOT upload on restart
    }

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
        let firstStarted = Gate(); let hold = Gate(); let secondStarted = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
                                        let n = await performCalls.incAndGet()
                                        if n == 1 { await firstStarted.open(); await hold.wait() }
                                        if n == 2 { await secondStarted.open() }
                                        return self.emptyReport()
                                    },
                                    assess: { _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
        await engine.start()
        await firstStarted.wait()                           // sync #1 running (stalled on hold)
        await engine.libraryDidChange()                     // change mid-sync → coalesced
        await engine.settle()
        await hold.open()                                   // sync #1 finishes → drain → count → sync #2
        await secondStarted.wait()                          // deterministically: the follow-up sync started
        #expect(await performCalls.value == 2)
        _ = await awaitStatus(engine, isWatching)           // sync #2 completes
        await engine.settle(); await engine.settle()
        #expect(await performCalls.value == 2)              // no third sync — the coalesced flag was consumed once
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

    // MARK: - sync status flow

    @Test func syncEmitsProgressThenWatching() async {
        let state = InMemorySyncStateStore()
        let started = Date(timeIntervalSince1970: 1_700_000_500)
        let p0 = SyncProgress(completed: 0, total: 2, currentItemName: "A.jpg", bytesRemaining: nil)
        let p1 = SyncProgress(completed: 1, total: 2, currentItemName: "B.jpg", bytesRemaining: nil)
        let engine = LiveSyncEngine(credentials: creds(true), state: state, perform: { _, onProgress in
            state.saveLastSyncStarted(started)
            onProgress(p0); onProgress(p1)
            return self.emptyReport()
        })
        await engine.start(); await engine.settle()
        var it = engine.statusStream().makeAsyncIterator()   // subscribe before syncNow
        await engine.syncNow()
        var collected: [SyncStatus] = []
        while let s = await it.next() {
            collected.append(s)
            if case .watching(let ls) = s, ls == started { break }
        }
        #expect(collected.contains(.syncing(p0)))
        #expect(collected.contains(.syncing(p1)))
        #expect(collected.last == .watching(lastSync: started))
    }

    @Test func partialFailuresStillEndWatching() async {
        let key = ResourceKey(localIdentifier: "X", kind: .photo)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [], deleted: [], failed: [FailedItem(key: key, filename: "X.jpg", reason: "boom")], skipped: 0)
        })
        await engine.start()
        await engine.syncNow()
        _ = await awaitSummary(engine) { !$0.failed.isEmpty }   // sync completed
        #expect(isWatching(await awaitStatus(engine, isWatching)))
    }

    @Test func wholeSyncThrowSetsError() async {
        struct Boom: Error {}
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in throw Boom() })
        await engine.start()
        await engine.syncNow()
        #expect(isError(await awaitStatus(engine, isError)))
    }

    // MARK: - re-entrancy & pause gating

    @Test func reentrantSyncNowIsIgnoredWhileSyncing() async {
        let calls = Counter()
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await calls.inc()
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 1)
        })
        await engine.start()
        await engine.syncNow()
        await started.wait()             // first sync's child is running
        await engine.syncNow()           // reentrant → ignored (syncChild != nil)
        await engine.settle()            // ensure the reentrant syncNow was processed
        #expect(await calls.value == 1)
        await release.open()
        _ = await awaitSummary(engine) { $0.backedUp == 1 }   // first sync completes
        #expect(await calls.value == 1)
    }

    @Test func pauseBlocksSyncNowResumeReturnsToWatching() async {
        let calls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await calls.inc(); return self.emptyReport() })
        await engine.start()
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await engine.syncNow()           // ignored while paused
        await engine.resume()
        #expect(await awaitStatus(engine, isWatching) == .watching(lastSync: nil))
        #expect(await calls.value == 0)  // resume observed ⇒ the paused syncNow was processed and ignored
    }

    // MARK: - cancellation / supersede

    @Test func pauseCancelsInFlightSync() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            onProgress(SyncProgress(completed: 0, total: 100, currentItemName: "x", bytesRemaining: nil))
            while true { try Task.checkCancellation(); await Task.yield() }
        })
        await engine.start()
        await engine.syncNow()
        _ = await awaitStatus(engine) { if case .syncing(let p) = $0 { return p.total == 100 }; return false }
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
    }

    @Test func resumeThenSyncNowStartsNewSyncAfterPause() async {
        let started = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await started.open()
            while true { try Task.checkCancellation(); await Task.yield() }   // runs until cancelled
        })
        await engine.start()
        await engine.syncNow()
        await started.wait()                                   // first sync running
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await engine.resume()
        #expect(await awaitStatus(engine, isWatching) == .watching(lastSync: nil))
        // Sync Now after resume must start a new sync (status → syncing), not be ignored.
        var it = engine.statusStream().makeAsyncIterator()
        await engine.syncNow()
        var sawSyncing = false
        while let s = await it.next() { if case .syncing = s { sawSyncing = true; break } }
        #expect(sawSyncing)
    }

    @Test func backedUpClimbsDuringSync() async {
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            onProgress(SyncProgress(completed: 2, total: 5, currentItemName: "x", bytesRemaining: nil))
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0)
        },
                                    assess: { _ in Assessment(backedUp: 10, pending: 0, resourceTotal: 10) })
        await engine.start()
        _ = await awaitSummary(engine) { $0.backedUp == 10 }   // base from the launch count
        await engine.syncNow()
        await started.wait()
        #expect(await awaitSummary(engine) { $0.backedUp == 12 }.backedUp == 12)   // 10 base + 2 completed
        await release.open()
    }

    @Test func pausePreservesRemainingCount() async {
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            onProgress(SyncProgress(completed: 3, total: 10, currentItemName: "x", bytesRemaining: nil))
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0)
        })
        await engine.start()
        await engine.syncNow()
        _ = await awaitStatus(engine) { if case .syncing(let p) = $0 { return p.completed == 3 }; return false }
        await engine.pause()
        #expect(await awaitStatus(engine, isPaused) == .paused(pending: 7))   // 10 - 3 remaining
        await release.open()
    }

    @Test func doublePausePreservesRemaining() async {
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            onProgress(SyncProgress(completed: 3, total: 10, currentItemName: "x", bytesRemaining: nil))
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0)
        })
        await engine.start()
        await engine.syncNow()
        _ = await awaitStatus(engine) { if case .syncing(let p) = $0 { return p.completed == 3 }; return false }
        await engine.pause()
        #expect(await awaitStatus(engine, isPaused) == .paused(pending: 7))
        await engine.pause()                  // second pause must not recompute/zero the remaining
        await engine.settle()
        #expect(await awaitStatus(engine, isPaused) == .paused(pending: 7))
        await release.open()
    }

    @Test func restartWhilePausedClearsRemaining() async {
        // A Settings save (appModel.restart -> start) from a paused state reconciles to watching;
        // a later idle Pause must show 0, not the stale paused remaining.
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            onProgress(SyncProgress(completed: 3, total: 10, currentItemName: "x", bytesRemaining: nil))
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0)
        })
        await engine.start()
        await engine.syncNow()
        await started.wait()
        await engine.pause()
        #expect(await awaitStatus(engine, isPaused) == .paused(pending: 7))
        await release.open()
        await engine.start()             // restart reconciles the paused run to watching
        _ = await awaitStatus(engine) { if case .watching = $0 { return true }; return false }
        await engine.pause()
        #expect(await awaitStatus(engine, isPaused) == .paused(pending: 0))   // no stale remaining
    }

    @Test func completionAfterPauseIsDropped() async {
        // perform ignores cancellation and completes; its result must not revive the sync.
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 5)
        })
        await engine.start()
        await engine.syncNow()
        await started.wait()
        await engine.pause()
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await release.open()
        await engine.settle()            // process (and drop) the stale completion
        #expect(await awaitSummary(engine) { _ in true } == .empty)   // finishSync did not run
        #expect(isPaused(await awaitStatus(engine, isPaused)))
    }

    @Test func signOutDuringSyncStaysSignedOut() async {
        let started = Gate(); let release = Gate()
        let creds = MutableCreds(.init(username: "u", password: "p"))
        let engine = LiveSyncEngine(credentials: creds, state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 5)
        },
                                    assess: { _ in Assessment(backedUp: 9, pending: 0, resourceTotal: 9) })
        await engine.start()
        await engine.syncNow()
        await started.wait()
        creds.set(nil)
        await engine.start()             // sign-out supersedes the in-flight run
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
        await release.open()
        await engine.settle()            // process (and drop) the stale completion
        #expect(await awaitSummary(engine) { _ in true } == .empty)
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
    }

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

    @Test func startDuringSyncDoesNotStrandTheEngine() async {
        let calls = Counter()
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await calls.inc()
            await started.open()
            await release.wait()
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 1)
        })
        await engine.start()
        await engine.syncNow()
        await started.wait()             // sync 1 running
        await engine.start()             // restart (e.g. Settings save) during the active sync
        await engine.settle()
        await release.open()
        _ = await awaitSummary(engine) { $0.backedUp == 1 }   // sync 1 completed → child cleared

        // A subsequent syncNow must still be accepted (not permanently ignored).
        var it = engine.statusStream().makeAsyncIterator()
        await engine.syncNow()
        var sawSyncing = false
        while let s = await it.next() { if case .syncing = s { sawSyncing = true; break } }
        #expect(sawSyncing)   // accepted, not permanently ignored (the strand bug)
    }

    // MARK: - Integration: engine driving a real SyncCoordinator (no UI, no PhotoKit)

    /// End-to-end through the real coordinator/uploader/ServerClient (via the in-memory
    /// FakeServer + FakeAssetLibrary): a sync uploads the library, tiles reflect the server
    /// count, and a re-sync is idempotent. Replaces most of the manual click-through.
    @Test func integrationRealCoordinatorSyncUpdatesTilesAndIsIdempotent() async {
        let server = FakeServer(host: "engine-int.test")
        let a = AssetResource(key: ResourceKey(localIdentifier: "IA", kind: .photo),
                              filename: "IA.jpg", creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)
        let b = AssetResource(key: ResourceKey(localIdentifier: "IB", kind: .photo),
                              filename: "IB.jpg", creationDate: Date(timeIntervalSince1970: 2), bundleID: nil)
        let lib = FakeAssetLibrary(items: [a, b], error: nil)
        let client = server.client()
        let uploader = AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 200, blobSize: 100))
        let coordinator = SyncCoordinator(client: client, library: lib, uploader: uploader,
                                          state: InMemorySyncStateStore())
        let engine = LiveSyncEngine(
            credentials: creds(true),
            state: InMemorySyncStateStore(),
            perform: { range, onProgress in try await coordinator.sync(range: range, onProgress: onProgress) },
            assess: { progress in
                let scan = try await lib.resources(in: .all, onProgress: progress.report)
                var records: [UploadRecord] = []
                var cursor: String? = nil
                repeat {
                    let page = try await client.listUploads(cursor: cursor)
                    records += page.items
                    cursor = page.nextCursor
                } while cursor != nil
                let plan = SyncPlanner.plan(library: scan, server: records, range: .all)
                return Assessment(backedUp: records.filter { $0.status == .complete }.count,
                                  pending: plan.uploads.count,
                                  resourceTotal: scan.count)
            })

        await engine.start()
        await engine.syncNow()
        let sum = await awaitSummary(engine) { $0.backedUp == 2 }   // both uploaded & complete on the server
        #expect(sum.failed.isEmpty)
        #expect(isWatching(await awaitStatus(engine, isWatching)))

        // Re-sync actually runs (re-issues server requests) and is idempotent: subscribe first,
        // observe a real .syncing → .watching cycle, and confirm the count stays 2.
        var it = engine.statusStream().makeAsyncIterator()
        let eventsBefore = server.events.count
        await engine.syncNow()
        var sawSyncing = false
        while let s = await it.next() {
            if case .syncing = s { sawSyncing = true }
            else if sawSyncing, case .watching = s { break }
        }
        #expect(server.events.count > eventsBefore)                 // the re-sync hit the server
        #expect(await awaitSummary(engine) { _ in true }.backedUp == 2)
    }
}

/// Counts `perform` invocations across concurrency.
actor Counter {
    private(set) var value = 0
    func inc() { value += 1 }
    func incAndGet() -> Int { value += 1; return value }
}

/// A pending value a test can flip between assess calls.
actor IntBox {
    private(set) var value: Int
    init(_ v: Int) { value = v }
    func set(_ v: Int) { value = v }
}

/// Credential store whose value can change between calls (for sign-out tests).
final class MutableCreds: CredentialStore, @unchecked Sendable {
    private let lock = NSLock()
    private var creds: BasicCredentials?
    init(_ c: BasicCredentials?) { creds = c }
    func basicCredentials() async throws -> BasicCredentials? { read() }
    private func read() -> BasicCredentials? { lock.lock(); defer { lock.unlock() }; return creds }
    func set(_ c: BasicCredentials?) { lock.lock(); creds = c; lock.unlock() }
}

/// One-shot async gate: `wait()` suspends until `open()` (idempotent).
actor Gate {
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private var opened = false
    func wait() async {
        if opened { return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func open() {
        opened = true
        for w in waiters { w.resume() }
        waiters.removeAll()
    }
}
