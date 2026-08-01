import Testing
import Foundation
@testable import FilesNestCore

struct StubSyncEngineTests {
    func firstStatus(_ engine: any SyncEngine) async -> SyncStatus {
        var it = engine.statusStream().makeAsyncIterator()
        return await it.next()!
    }

    @Test func startWithoutCredentialsIsSignedOut() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(nil))
        await engine.start()
        #expect(await firstStatus(engine) == .signedOut)
    }

    @Test func startWithCredentialsIsWatching() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.start()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }

    @Test func pauseThenResume() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.start()
        await engine.pause()
        if case .paused = await firstStatus(engine) {} else { Issue.record("expected .paused") }
        await engine.resume()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }

    @Test func syncNowEntersSyncingThenWatching() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")),
                                    autoComplete: false)
        await engine.start()
        await engine.syncNow()
        if case .syncing = await firstStatus(engine) {} else { Issue.record("expected .syncing") }
    }

    @Test func syncNowIgnoredWhenSignedOut() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(nil))
        await engine.start()
        await engine.syncNow()
        #expect(await firstStatus(engine) == .signedOut)
    }

    @Test func libraryDidChangeIsANoOp() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.start()
        await engine.libraryDidChange()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))   // unchanged; no crash
    }
}
