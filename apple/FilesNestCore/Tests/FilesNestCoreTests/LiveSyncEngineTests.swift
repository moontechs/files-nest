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

    @Test func startRefreshesBackedUpFromServer() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    refreshBackedUp: { 7 })
        await engine.start()
        #expect(await awaitSummary(engine) { $0.backedUp == 7 } == SyncSummary(backedUp: 7, failed: []))
    }

    @Test func summaryPublishedAfterSync() async {
        let uploaded = ResourceKey(localIdentifier: "A", kind: .photo)
        let f = FailedItem(key: ResourceKey(localIdentifier: "B", kind: .photo), filename: "B.jpg", reason: "boom")
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [uploaded], deleted: [], failed: [f], skipped: 3)
        },
                                    refreshBackedUp: { 42 })
        await engine.start()
        await engine.syncNow()
        let sum = await awaitSummary(engine) { !$0.failed.isEmpty }
        #expect(sum.backedUp == 42)          // live server count, not skipped+uploaded
        #expect(sum.failed == [f])
    }

    @Test func syncBackedUpFallsBackToReportWhenRefreshFails() async {
        struct Boom: Error {}
        let uploaded = ResourceKey(localIdentifier: "A", kind: .photo)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [uploaded], deleted: [], failed: [], skipped: 3)
        },
                                    refreshBackedUp: { throw Boom() })
        await engine.start()
        await engine.syncNow()
        #expect(await awaitSummary(engine) { $0.backedUp == 4 } == SyncSummary(backedUp: 4, failed: []))  // 3+1
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
                                    refreshBackedUp: { 9 })
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
        let creds = MutableCreds(.init(username: "u", password: "p"))
        let engine = LiveSyncEngine(credentials: creds, state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    refreshBackedUp: { 9 })
        await engine.start()
        #expect(await awaitSummary(engine) { $0.backedUp == 9 } == SyncSummary(backedUp: 9, failed: []))
        creds.set(nil)
        await engine.start()
        #expect(await awaitStatus(engine) { $0 == .signedOut } == .signedOut)
        #expect(await awaitSummary(engine) { $0 == .empty } == .empty)
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
}

/// Counts `perform` invocations across concurrency.
actor Counter {
    private(set) var value = 0
    func inc() { value += 1 }
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
