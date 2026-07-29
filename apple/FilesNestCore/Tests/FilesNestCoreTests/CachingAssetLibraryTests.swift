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
}
