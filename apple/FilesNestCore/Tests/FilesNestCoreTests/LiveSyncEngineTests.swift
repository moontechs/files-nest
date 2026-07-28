import Testing
import Foundation
@testable import FilesNestCore

@Suite struct LiveSyncEngineTests {

    // Reads the current status from a fresh stream (current-status-first).
    func firstStatus(_ engine: any SyncEngine) async -> SyncStatus {
        var it = engine.statusStream().makeAsyncIterator()
        return await it.next()!
    }

    func creds(_ present: Bool) -> StaticCredentialStore {
        StaticCredentialStore(present ? .init(username: "u", password: "p") : nil)
    }

    func emptyReport() -> SyncReport {
        SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0)
    }

    @Test func startWithoutCredentialsIsSignedOut() async {
        let engine = LiveSyncEngine(credentials: creds(false), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() })
        await engine.start()
        #expect(await firstStatus(engine) == .signedOut)
    }

    @Test func startWithCredentialsIsWatchingWithStoredLastSync() async {
        let state = InMemorySyncStateStore()
        let d = Date(timeIntervalSince1970: 1_700_000_000)
        state.saveLastSyncStarted(d)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, _ in self.emptyReport() })
        await engine.start()
        #expect(await firstStatus(engine) == .watching(lastSync: d))
    }

    @Test func syncNowIgnoredWhenSignedOut() async {
        let calls = Counter()
        let engine = LiveSyncEngine(credentials: creds(false), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await calls.inc(); return self.emptyReport() })
        await engine.start()
        await engine.syncNow()
        #expect(await firstStatus(engine) == .signedOut)
        #expect(await calls.value == 0)
    }

    @Test func syncNowEmitsSyncingProgressThenWatching() async {
        let state = InMemorySyncStateStore()
        let started = Date(timeIntervalSince1970: 1_700_000_500)
        let p0 = SyncProgress(completed: 0, total: 2, currentItemName: "A.jpg", bytesRemaining: nil)
        let p1 = SyncProgress(completed: 1, total: 2, currentItemName: "B.jpg", bytesRemaining: nil)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, onProgress in
            state.saveLastSyncStarted(started)   // simulate the coordinator stamping start
            onProgress(p0); onProgress(p1)
            return self.emptyReport()
        })
        await engine.start()                      // → .watching(nil)

        // Subscribe BEFORE syncNow; the build closure runs synchronously and buffers
        // the current status (.watching(nil)) first. Default buffering is unbounded,
        // so no yields are dropped before we drain them.
        var it = engine.statusStream().makeAsyncIterator()
        await engine.syncNow()

        var got: [SyncStatus] = []
        for _ in 0..<5 { got.append(await it.next()!) }
        #expect(got == [
            .watching(lastSync: nil),
            .syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)),
            .syncing(p0),
            .syncing(p1),
            .watching(lastSync: started),
        ])
    }

    @Test func partialFailuresStillEndWatching() async {
        let key = ResourceKey(localIdentifier: "X", kind: .photo)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [], deleted: [], failed: [FailedItem(key: key, filename: "X.jpg", reason: "boom")], skipped: 0)
        })
        await engine.start()
        await engine.syncNow()
        guard case .watching = await firstStatus(engine) else { Issue.record("expected .watching"); return }
    }

    @Test func wholeSyncThrowSetsError() async {
        struct Boom: Error {}
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in throw Boom() })
        await engine.start()
        await engine.syncNow()
        guard case .error = await firstStatus(engine) else { Issue.record("expected .error"); return }
    }

    @Test func reentrantSyncNowIsIgnoredWhileSyncing() async {
        let calls = Counter()
        let inside = Gate()      // opened once perform is running
        let release = Gate()     // test opens it to let perform finish
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await calls.inc()
            await inside.open()
            await release.wait()
            return self.emptyReport()
        })
        await engine.start()

        let first = Task { await engine.syncNow() }
        await inside.wait()          // first sync is now inside perform (status .syncing)
        await engine.syncNow()       // second call must be ignored (guard)
        #expect(await calls.value == 1)

        await release.open()
        await first.value
        #expect(await calls.value == 1)
    }

    @Test func pauseBlocksSyncNowResumeReturnsToWatching() async {
        let calls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await calls.inc(); return self.emptyReport() })
        await engine.start()
        await engine.pause()
        guard case .paused = await firstStatus(engine) else { Issue.record("expected .paused"); return }
        await engine.syncNow()
        #expect(await calls.value == 0)
        await engine.resume()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }

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
        },
                                    refreshBackedUp: { 42 })
        await engine.start()
        await engine.syncNow()
        let summary = await firstSummary(engine)
        #expect(summary.backedUp == 42)         // live server count, not skipped+uploaded
        #expect(summary.failed == [f])
    }

    @Test func startRefreshesBackedUpFromServer() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    refreshBackedUp: { 7 })
        await engine.start()
        #expect(await firstSummary(engine) == SyncSummary(backedUp: 7, failed: []))
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
        #expect(await firstSummary(engine) == SyncSummary(backedUp: 4, failed: []))  // 3 + 1 fallback
    }

    @Test func pauseCancelsInFlightSync() async {
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            onProgress(SyncProgress(completed: 0, total: 100, currentItemName: "x", bytesRemaining: nil))
            while true { try Task.checkCancellation(); await Task.yield() }
        })
        await engine.start()
        let t = Task { await engine.syncNow() }
        var it = engine.statusStream().makeAsyncIterator()
        while true {
            if case .syncing(let p)? = await it.next(), p.total == 100 { break }
        }
        await engine.pause()
        await t.value
        if case .paused = await firstStatus(engine) {} else { Issue.record("expected .paused") }
    }

    @Test func completionWhilePausedStaysPaused() async {
        let started = Gate(); let release = Gate()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, onProgress in
            await started.open()
            await release.wait()             // ignores cancellation → the sync completes normally
            onProgress(SyncProgress(completed: 1, total: 1, currentItemName: "x", bytesRemaining: nil))
            return SyncReport(uploaded: [], deleted: [], failed: [], skipped: 5)
        })
        await engine.start()
        let t = Task { await engine.syncNow() }
        await started.wait()
        await engine.pause()                 // pausedFlag set; task.cancel ignored by this perform
        await release.open()
        await t.value
        if case .paused = await firstStatus(engine) {} else {
            Issue.record("a sync completing after pause must stay .paused")
        }
    }

    @Test func signOutClearsSummary() async {
        let creds = MutableCreds(.init(username: "u", password: "p"))
        let engine = LiveSyncEngine(credentials: creds, state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() },
                                    refreshBackedUp: { 9 })
        await engine.start()
        #expect(await firstSummary(engine) == SyncSummary(backedUp: 9, failed: []))
        creds.set(nil)
        await engine.start()
        #expect(await firstSummary(engine) == .empty)
        #expect(await firstStatus(engine) == .signedOut)
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
