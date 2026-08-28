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

    @Test func startWithCredentialsButUnreadyDestinationIsSignedOut() async {
        let engine = LiveSyncEngine(
            credentials: creds(true),
            state: InMemorySyncStateStore(),
            perform: { _, _ in self.emptyReport() },
            isReady: { false })

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
                                    assess: { _, progress in
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
                                    assess: { _, _ in await gate.wait(); return Assessment(backedUp: 1, pending: 1, resourceTotal: 1) },
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

    @Test func assessFailureNeedsAttention() async {
        struct Boom: Error {}
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _, _ in throw Boom() })
        await engine.start()
        #expect(isError(await awaitStatus(engine, isError)))
        #expect(await awaitSummary(engine) { _ in true } == .empty)   // no cache → stays empty
    }

    @Test func syncNowDuringCountingSupersedesTheCount() async {
        let counting = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _, _ in await counting.open(); await release.wait()
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
                                    assess: { _, _ in await assessRuns.inc(); await counting.open(); await release.wait()
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 1, resourceTotal: 1) })
        await engine.start()
        _ = await awaitStatus(engine, isSyncing)            // count found pending → auto-sync started
        _ = await awaitStatus(engine, isWatching)           // sync completed
        #expect(await performCalls.value == 1)
    }

    @Test func launchDoesNotSyncWhenNothingPending() async {
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _, _ in Assessment(backedUp: 5, pending: 0, resourceTotal: 5) })
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 5, resourceTotal: 5) })
        await engine.start()
        await firstStarted.wait()                           // launch auto-sync of the 5 pending (option A) has started
        #expect(await performCalls.value == 1)
        await engine.pause()                                // user pauses
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await hold.open()                                   // cancelled sync's perform returns (dropped by generation)
        await engine.start()                                // Settings save → restart while paused
        _ = await awaitStatus(engine, isWatching)           // reconciles to watching…
        await engine.settle()
        #expect(await awaitStatus(engine) { _ in true } == .watching(lastSync: nil))   // stays watching (pre-fix: chained → .syncing)
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 5, resourceTotal: 5) })
        await engine.start()
        await firstStarted.wait()                           // launch auto-sync of the 5 pending (option A), stalled
        await engine.pause()                                // user pauses
        #expect(isPaused(await awaitStatus(engine, isPaused)))
        await engine.libraryDidChange()                     // a change arrives WHILE paused → coalesced
        await engine.settle()
        await hold.open()                                   // cancelled sync's perform returns (dropped by generation)
        await engine.start()                                // Settings save → restart while paused
        _ = await awaitStatus(engine, isWatching)
        await engine.settle()
        // Pre-fix the else-drain synchronously flips status to .counting for the follow-up count;
        // asserting it stays .watching catches that deterministically (not just via the perform count).
        #expect(await awaitStatus(engine) { _ in true } == .watching(lastSync: nil))
        #expect(await performCalls.value == 1)              // the coalesced-during-pause change must NOT upload on restart
    }

    // MARK: - continuous watching (libraryDidChange)

    @Test func changeWhileIdleCountsThenSyncs() async {
        let box = IntBox(0)                      // launch finds nothing → no launch sync
        let performCalls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await performCalls.inc(); return self.emptyReport() },
                                    assess: { _, _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: await box.value, resourceTotal: 0) })
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 9, resourceTotal: 9) })
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
                                    assess: { _, _ in await assessCalls.inc(); return Assessment(backedUp: 3, pending: 0, resourceTotal: 3) })
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
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 1, resourceTotal: 1) })
        await engine.libraryDidChange()                     // before start(): not signed in yet → ignored
        await engine.settle()
        #expect(await performCalls.value == 0)
    }

    // MARK: - incremental range

    @Test func libraryChangeUsesModifiedSinceAfterACleanSync() async {
        // The incremental anchor is the start of the last CLEAN sync (in-memory), not the
        // persisted lastSyncStarted. A clean .all launch sync (at now == `anchor`) sets it.
        let anchor = Date(timeIntervalSince1970: 5_000)
        let recorded = RangeBox()
        let pending = IntBox(1)   // launch finds work → a clean .all launch sync sets the anchor
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { range, _ in await recorded.add(range); return self.emptyReport() },
                                    assess: { range, _ in await recorded.add(range)
                                                          return Assessment(backedUp: 0, pending: await pending.value, resourceTotal: 0) },
                                    now: { anchor })
        var it0 = engine.statusStream().makeAsyncIterator()
        await engine.start()
        var sawSyncing0 = false                             // launch: .all count → .all sync (clean) → anchor set
        while let s = await it0.next() {
            if case .syncing = s { sawSyncing0 = true }
            else if sawSyncing0, case .watching = s { break }
        }
        #expect(await recorded.all.allSatisfy { $0 == .all })   // launch scanned + synced .all
        #expect(!(await recorded.all.isEmpty))

        await recorded.clear()
        var it = engine.statusStream().makeAsyncIterator()
        await engine.libraryDidChange()                     // change → incremental count + sync
        var sawSyncing = false
        while let s = await it.next() {
            if case .syncing = s { sawSyncing = true }
            else if sawSyncing, case .watching = s { break }
        }
        let want = SyncRange.modifiedSince(anchor.addingTimeInterval(-60))
        let ranges = await recorded.all
        #expect(!ranges.isEmpty)
        #expect(ranges.allSatisfy { $0 == want })          // both the count and the sync used .modifiedSince(anchor-60)
    }

    @Test func failedSyncDoesNotAdvanceIncrementalAnchorSoNextChangeIsAll() async {
        // A launch .all sync that throws must NOT leave a stale anchor: the next change must
        // fall back to .all (covering the backlog), not an incremental window that skips it.
        struct Boom: Error {}
        let recorded = RangeBox()
        let state = InMemorySyncStateStore()
        let when = Date(timeIntervalSince1970: 5_000)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    // Mirror the real coordinator: persist lastSyncStarted at the sync's START,
                                    // then fail. The fix must NOT anchor on that persisted value.
                                    perform: { range, _ in await recorded.add(range); state.saveLastSyncStarted(when); throw Boom() },
                                    assess: { range, _ in await recorded.add(range)
                                                          return Assessment(backedUp: 0, pending: 1, resourceTotal: 1) },
                                    now: { when })
        await engine.start()
        _ = await awaitStatus(engine, isError)             // launch .all count → sync throws → error, anchor unmoved
        await recorded.clear()
        var it = engine.statusStream().makeAsyncIterator()  // current = stale .error; wait for the CHANGE cycle
        await engine.libraryDidChange()                     // from .error a change starts a fresh cycle…
        var sawCounting = false
        while let s = await it.next() {
            if case .counting = s { sawCounting = true }    // the change's count started
            else if sawCounting, case .error = s { break }  // …and its .all sync threw
        }
        let ranges = await recorded.all
        #expect(!ranges.isEmpty)
        #expect(ranges.allSatisfy { $0 == .all })          // fell back to .all — no stale incremental window
    }

    @Test func manualSyncNowUsesAll() async {
        let state = InMemorySyncStateStore()
        state.saveLastSyncStarted(Date(timeIntervalSince1970: 1_000_000))
        let recorded = RangeBox()
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { range, _ in await recorded.add(range); return self.emptyReport() })
        await engine.start(); await engine.settle()         // no assess → straight to watching
        var it = engine.statusStream().makeAsyncIterator()
        await engine.syncNow()
        var sawSyncing = false                              // wait for the sync to actually run and complete
        while let s = await it.next() {
            if case .syncing = s { sawSyncing = true }
            else if sawSyncing, case .watching = s { break }
        }
        #expect(await recorded.all == [.all])               // manual Sync Now is always full
    }

    @Test func incrementalSyncKeepsWholeLibraryBackedUp() async {
        // A clean .all launch sync grounds whole-library backedUp (63000) AND sets the anchor,
        // so the change runs incrementally; its finishSync must keep backedUp at base + uploaded.
        let up = ResourceKey(localIdentifier: "NEW", kind: .photo)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { range, _ in
                                        if case .modifiedSince = range { return SyncReport(uploaded: [up], deleted: [], failed: [], skipped: 0) }
                                        return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 63_000)   // .all launch: clean baseline
                                    },
                                    assess: { _, _ in Assessment(backedUp: 63_000, pending: 1, resourceTotal: 70_000) },
                                    now: { Date(timeIntervalSince1970: 5_000) })
        await engine.start()
        _ = await awaitSummary(engine) { $0.backedUp == 63_000 }   // launch: assess grounds 63000; clean .all sync keeps it, sets anchor
        _ = await awaitStatus(engine, isWatching)
        await engine.libraryDidChange()                             // incremental count(63000, pending 1) → .modifiedSince sync uploads 1
        let sum = await awaitSummary(engine) { $0.backedUp == 63_001 }
        #expect(sum.backedUp == 63_001)   // base 63000 + 1 uploaded — NOT collapsed to report.skipped+uploaded (=1)
    }

    @Test func summaryCarriesResourceTotalAcrossCountAndSync() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _, _ in Assessment(backedUp: 5, pending: 0, resourceTotal: 42) })
        await engine.start()
        let sum = await awaitSummary(engine) { $0.resourceTotal == 42 }   // the count surfaced the whole-library total
        #expect(sum.backedUp == 5)
        var it = engine.statusStream().makeAsyncIterator()                // wait for the sync's .syncing→.watching so
        await engine.syncNow()                                            // finishSync provably ran (a drop would show)
        var sawSyncing = false
        while let s = await it.next() {
            if case .syncing = s { sawSyncing = true }
            else if sawSyncing, case .watching = s { break }
        }
        #expect(await awaitSummary(engine) { _ in true }.resourceTotal == 42)   // survived finishSync
    }

    // MARK: - reconcile (Settings save → forced full .all)

    @Test func reconcileWhileSyncingSupersedesWithFreshAll() async {
        let performCalls = Counter()
        let recorded = RangeBox()
        let firstStarted = Gate(); let hold = Gate(); let secondStarted = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { range, _ in
                                        let n = await performCalls.incAndGet()
                                        await recorded.add(range)
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
        #expect(await recorded.all == [.all, .all])   // both the launch and the superseding sync used .all
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
                                    assess: { _, _ in Assessment(backedUp: 10, pending: 0, resourceTotal: 10) })
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
                                    assess: { _, _ in Assessment(backedUp: 9, pending: 0, resourceTotal: 9) })
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
                                    assess: { _, _ in Assessment(backedUp: 9, pending: 2, resourceTotal: 11) })
        await engine.start()
        #expect(await awaitSummary(engine) { $0.backedUp == 9 } == SyncSummary(backedUp: 9, pending: 2, failed: [], resourceTotal: 11))
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
            assess: { _, progress in
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

    // MARK: - persisted resume / fast launch

    @Test func launchWithSavedListResumesBeforeCounting() async {
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let order = OrderRecorder()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in order.mark("perform"); return self.emptyReport() },
            resume: { _, _ in
                order.mark("resume")
                return SyncReport(uploaded: [ResourceKey(localIdentifier: "A", kind: .photo)],
                                  deleted: [], failed: [], skipped: 0)
            },
            assess: { _, _ in order.mark("assess"); return Assessment(backedUp: 1, pending: 0, resourceTotal: 1) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)   // settle through resume -> reconcile
        await engine.settle()
        // The saved list uploaded first; the reconcile count ran only after it.
        #expect(order.marks.first == "resume")
        #expect(order.marks.contains("assess"))
    }

    @Test func resumeWithSavedListAndNoChangeFastPathsWithoutVerificationScan() async {
        let state = InMemorySyncStateStore()
        let hold = Gate()   // stall the resume upload so the `.syncing` fast-path state is observable
        let assessCalls = Counter()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in self.emptyReport() },
            resume: { _, _ in await hold.wait(); return self.emptyReport() },
            assess: { _, _ in
                await assessCalls.inc()
                return Assessment(backedUp: 0, pending: 0, resourceTotal: 0)
            })
        // Get to paused, then seed a saved list and resume.
        await engine.start(); await engine.settle()
        await engine.pause(); await engine.settle()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        await engine.resume()
        // Fast-path drives `.syncing` (the non-fast-path would go to `.watching`).
        #expect(isSyncing(await awaitStatus(engine, isSyncing)))
        await hold.open()
        _ = await awaitStatus(engine, isWatching)
        #expect(await assessCalls.value == 1) // launch only; Resume did not re-enumerate the library
    }

    @Test func resumeWithPendingChangeUploadsSavedPlanThenChecksIncrementally() async {
        let state = InMemorySyncStateStore()
        let resumeCalls = Counter()
        let ranges = RangeBox()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in self.emptyReport() },
            resume: { _, _ in
                await resumeCalls.inc()
                return self.emptyReport()
            },
            assess: { range, _ in
                await ranges.add(range)
                return Assessment(backedUp: 0, pending: 0, resourceTotal: 0)
            })
        await engine.start(); await engine.settle()
        await ranges.clear()
        await engine.pause(); await engine.settle()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        await engine.libraryDidChange(); await engine.settle()   // coalesced during the pause
        await engine.resume()
        #expect(isWatching(await awaitStatus(engine, isWatching)))
        #expect(await resumeCalls.value == 1)
        guard case .modifiedSince = await ranges.all.first else {
            Issue.record("the post-resume check must be incremental, not a whole-library scan")
            return
        }
    }

    @Test func signOutClearsSavedList() async {
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let store = MutableCreds(.init(username: "u", password: "p"))
        let engine = LiveSyncEngine(credentials: store, state: state,
                                    perform: { _, _ in self.emptyReport() })
        await engine.start(); await engine.settle()
        store.set(nil)
        await engine.reconcile(); await engine.settle()   // reconcile re-reads creds → signed out
        #expect(state.loadRemainingUploads().isEmpty)
    }

    @Test func reconcileClearsSavedList() async {
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, _ in self.emptyReport() },
                                    assess: { _, _ in Assessment(backedUp: 0, pending: 0, resourceTotal: 0) })
        await engine.start(); await engine.settle()
        await engine.reconcile(); await engine.settle()   // config/server change → re-ground from scratch
        #expect(state.loadRemainingUploads().isEmpty)
    }

    /// A fast-path upload that FAILS must not leave the engine routing later, normal
    /// syncs through the resume-finish handler — that would drop the report's summary.
    @Test func failedFastPathDoesNotMisrouteALaterSync() async {
        struct Boom: Error {}
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in
                SyncReport(uploaded: [ResourceKey(localIdentifier: "B", kind: .photo)],
                           deleted: [], failed: [], skipped: 4)
            },
            resume: { _, _ in throw Boom() },
            assess: { _, _ in Assessment(backedUp: 99, pending: 0, resourceTotal: 99) })

        await engine.start()
        _ = await awaitStatus(engine, isError)      // the fast-path upload failed
        await engine.syncNow()
        _ = await awaitStatus(engine, isWatching)   // the normal sync completed
        await engine.settle()

        // finishSync sourced the summary from the report (4 skipped + 1 uploaded), rather than
        // finishResumeUpload kicking off another count (which would show the assess value).
        #expect(await awaitSummary(engine) { _ in true }.backedUp == 5)
    }

    /// Pause at "2 of 5" then Resume must keep counting from 2, not restart at 0 — the
    /// resumed run only knows about the files IT uploads, so the engine offsets its
    /// progress by what the interrupted run already did.
    @Test func resumedProgressContinuesInsteadOfRestartingAtZero() async {
        let state = InMemorySyncStateStore()
        let hold = Gate()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, onProgress in
                onProgress(SyncProgress(completed: 2, total: 5, currentItemName: "b.jpg", bytesRemaining: nil))
                await hold.wait()
                return self.emptyReport()
            },
            resume: { _, onProgress in
                // The resumed run sees only the 3 remaining files.
                onProgress(SyncProgress(completed: 1, total: 3, currentItemName: "c.jpg", bytesRemaining: nil))
                await Gate().wait()          // stay in .syncing so the status is observable
                return self.emptyReport()
            },
            assess: { _, _ in Assessment(backedUp: 0, pending: 5, resourceTotal: 5) })

        await engine.start()
        _ = await awaitStatus(engine, isSyncing)
        _ = await awaitStatus(engine) { if case .syncing(let p) = $0 { return p.completed == 2 }; return false }
        await engine.pause(); await engine.settle()
        state.saveRemainingUploads((0..<3).map {
            AssetResource(key: ResourceKey(localIdentifier: "r\($0)", kind: .photo), filename: "r\($0).jpg",
                          creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)
        })
        await engine.resume()

        // 2 already done + 1 in this run = 3 of 5 — the counter continues.
        let p = await awaitStatus(engine) { if case .syncing(let q) = $0 { return q.completed > 0 }; return false }
        guard case .syncing(let shown) = p else { Issue.record("expected .syncing"); return }
        #expect(shown.completed == 3)
        #expect(shown.total == 5)
        await hold.open()
    }

    /// The count chained after a fast-path upload is a VERIFY pass. It must say so, or it
    /// reads as "the counter started over" right after the upload finished.
    @Test func reconcileAfterAResumedUploadIsMarkedAsVerification() async {
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let hold = Gate()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in self.emptyReport() },
            resume: { _, _ in self.emptyReport() },
            assess: { _, _ in await hold.wait(); return Assessment(backedUp: 1, pending: 0, resourceTotal: 1) })

        await engine.start()
        let s = await awaitStatus(engine) { if case .counting = $0 { return true }; return false }
        guard case .counting(_, _, let purpose) = s else { Issue.record("expected .counting"); return }
        #expect(purpose == .verify)
        await hold.open()
    }

    /// A plain launch count is a survey, not a verification.
    @Test func launchCountIsMarkedAsSurvey() async {
        let hold = Gate()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: InMemorySyncStateStore(),
            perform: { _, _ in self.emptyReport() },
            assess: { _, _ in await hold.wait(); return Assessment(backedUp: 0, pending: 0, resourceTotal: 0) })

        await engine.start()
        let s = await awaitStatus(engine) { if case .counting = $0 { return true }; return false }
        guard case .counting(_, _, let purpose) = s else { Issue.record("expected .counting"); return }
        #expect(purpose == .survey)
        await hold.open()
    }

    @Test func launchWithEmptySavedListCountsAsBefore() async {
        let engine = LiveSyncEngine(
            credentials: creds(true), state: InMemorySyncStateStore(),
            perform: { _, _ in self.emptyReport() },
            resume: { _, _ in
                Issue.record("resume must not run without a saved list")
                return self.emptyReport()
            },
            assess: { _, _ in Assessment(backedUp: 0, pending: 0, resourceTotal: 0) })
        await engine.start(); await engine.settle()
        #expect(isWatching(await awaitStatus(engine, isWatching)))
    }
}

/// Records the order in which fake closures ran, for ordering assertions.
final class OrderRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _marks: [String] = []
    var marks: [String] { lock.lock(); defer { lock.unlock() }; return _marks }
    func mark(_ s: String) { lock.lock(); _marks.append(s); lock.unlock() }
}

/// Counts `perform` invocations across concurrency.
actor Counter {
    private(set) var value = 0
    func inc() { value += 1 }
    func incAndGet() -> Int { value += 1; return value }
}

/// Records the ranges a fake perform/assess was called with.
actor RangeBox {
    private(set) var all: [SyncRange] = []
    func add(_ r: SyncRange) { all.append(r) }
    func clear() { all.removeAll() }
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
