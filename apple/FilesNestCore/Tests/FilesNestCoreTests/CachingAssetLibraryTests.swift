import Testing
import Foundation
@testable import FilesNestCore

@Suite struct CachingAssetLibraryTests {
    private func item(_ id: String) -> AssetResource {
        AssetResource(key: ResourceKey(localIdentifier: id, kind: .photo),
                      filename: "\(id).jpg", creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)
    }

    /// Mutable clock so TTL expiry is deterministic (no wall-clock sleeps).
    final class Clock: @unchecked Sendable {
        private let lock = NSLock(); private var t: Date
        init(_ t: Date) { self.t = t }
        var value: Date { lock.lock(); defer { lock.unlock() }; return t }
        func advance(_ s: TimeInterval) { lock.lock(); t = t.addingTimeInterval(s); lock.unlock() }
    }

    final class ProgressBox: @unchecked Sendable {
        private let lock = NSLock(); private var _last = ""
        func set(_ d: Int, _ t: Int) { lock.lock(); _last = "\(d)/\(t)"; lock.unlock() }
        var last: String { lock.lock(); defer { lock.unlock() }; return _last }
    }

    @Test func secondCallWithinTTLReusesTheScan() async throws {
        let fake = FakeAssetLibrary(items: [item("A"), item("B")])
        let caching = CachingAssetLibrary(wrapping: fake, ttl: 60, now: { Date(timeIntervalSince1970: 0) })
        _ = try await caching.resources(in: .all, onProgress: nil)
        let second = try await caching.resources(in: .all, onProgress: nil)
        #expect(second.count == 2)
        #expect(fake.requestedRanges.count == 1)   // wrapped scanned once; the second call hit the cache
    }

    @Test func expiryReScans() async throws {
        let fake = FakeAssetLibrary(items: [item("A")])
        let clock = Clock(Date(timeIntervalSince1970: 0))
        let caching = CachingAssetLibrary(wrapping: fake, ttl: 60, now: { clock.value })
        _ = try await caching.resources(in: .all, onProgress: nil)
        clock.advance(61)
        _ = try await caching.resources(in: .all, onProgress: nil)
        #expect(fake.requestedRanges.count == 2)   // expired → re-scanned
    }

    @Test func differentRangeReScans() async throws {
        let fake = FakeAssetLibrary(items: [item("A")])
        let caching = CachingAssetLibrary(wrapping: fake, ttl: 60, now: { Date(timeIntervalSince1970: 0) })
        _ = try await caching.resources(in: .all, onProgress: nil)
        let window = Date(timeIntervalSince1970: 0)...Date(timeIntervalSince1970: 10)
        _ = try await caching.resources(in: .dates(window), onProgress: nil)
        #expect(fake.requestedRanges.count == 2)   // a different range is a distinct scan
    }

    @Test func cacheHitEmitsCompletedProgress() async throws {
        let fake = FakeAssetLibrary(items: [item("A"), item("B")])
        let caching = CachingAssetLibrary(wrapping: fake, ttl: 60, now: { Date(timeIntervalSince1970: 0) })
        _ = try await caching.resources(in: .all, onProgress: nil)
        let box = ProgressBox()
        _ = try await caching.resources(in: .all, onProgress: { d, t in box.set(d, t) })
        #expect(box.last == "2/2")                 // hit jumps the counting UI to done
    }

    @Test func invalidateForcesReScan() async throws {
        let fake = FakeAssetLibrary(items: [item("A")])
        let caching = CachingAssetLibrary(wrapping: fake, ttl: 60, now: { Date(timeIntervalSince1970: 0) })
        _ = try await caching.resources(in: .all, onProgress: nil)
        await caching.invalidate()
        _ = try await caching.resources(in: .all, onProgress: nil)
        #expect(fake.requestedRanges.count == 2)
    }

    /// A scan that is in flight when `invalidate()` lands must NOT publish its now-stale
    /// result into the cache — otherwise a coalesced follow-up serves a photo-losing snapshot
    /// for up to the TTL. (Reuses `Gate` from LiveSyncEngineTests in the same test target.)
    @Test func invalidateDuringScanDoesNotPublishStaleCache() async throws {
        let entered = Gate(); let release = Gate()
        let fake = SuspendingLibrary(items: [item("A")], entered: entered, release: release)
        let caching = CachingAssetLibrary(wrapping: fake, ttl: 60, now: { Date(timeIntervalSince1970: 0) })

        let first = Task { try await caching.resources(in: .all, onProgress: nil) }
        await entered.wait()              // the first scan is suspended inside the wrapped library
        await caching.invalidate()        // a library change lands mid-scan
        await release.open()              // let the (now stale) first scan finish
        _ = try await first.value

        _ = try await caching.resources(in: .all, onProgress: nil)
        #expect(fake.calls == 2)          // the stale scan did not populate the cache → re-scanned
    }
}

/// A wrapped `AssetLibrary` whose first `resources(...)` suspends until released, so a test can
/// call `invalidate()` while a scan is in flight.
final class SuspendingLibrary: AssetLibrary, @unchecked Sendable {
    private let items: [AssetResource]
    private let entered: Gate
    private let release: Gate
    private let lock = NSLock(); private var _calls = 0
    var calls: Int { lock.lock(); defer { lock.unlock() }; return _calls }

    init(items: [AssetResource], entered: Gate, release: Gate) {
        self.items = items; self.entered = entered; self.release = release
    }

    func resources(in range: SyncRange,
                   onProgress: (@Sendable (Int, Int) -> Void)?) async throws -> [AssetResource] {
        let n = bump()                                             // sync: NSLock must not span an await
        if n == 1 { await entered.open(); await release.wait() }   // first scan suspends mid-flight
        return items
    }

    private func bump() -> Int { lock.lock(); defer { lock.unlock() }; _calls += 1; return _calls }
}
