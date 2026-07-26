import Foundation
@testable import FilesNestCore

/// Records the ranges it was asked for, so tests can assert the coordinator
/// forwards `range` to the library (a struct couldn't record across the async
/// call — the coordinator holds its own copy).
final class FakeAssetLibrary: AssetLibrary, @unchecked Sendable {
    private let lock = NSLock()
    private let items: [AssetResource]
    private let error: (any Error)?
    private var _requestedRanges: [SyncRange] = []

    init(items: [AssetResource] = [], error: (any Error)? = nil) {
        self.items = items
        self.error = error
    }

    var requestedRanges: [SyncRange] { lock.lock(); defer { lock.unlock() }; return _requestedRanges }

    func resources(in range: SyncRange) async throws -> [AssetResource] {
        recordRange(range)               // sync helper: NSLock must not be held across an await
        if let error { throw error }
        return items
    }

    private func recordRange(_ range: SyncRange) {
        lock.lock(); defer { lock.unlock() }
        _requestedRanges.append(range)
    }
}
