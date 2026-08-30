import Foundation
@testable import FilesNestCore

/// Deterministic test double. `final class` behind a lock (Sendable; mutated
/// from the coordinator). Exact `Date` is preserved, unlike the ISO-8601 store.
final class InMemorySyncStateStore: SyncStateStore, @unchecked Sendable {
    private let lock = NSLock()
    private var value: Date?
    private var _assessment: Assessment?
    private var _remaining: [AssetResource] = []
    private var _remainingDestination: Data?
    private var _remainingSession: UInt64 = 0

    func loadLastSyncStarted() -> Date? { lock.lock(); defer { lock.unlock() }; return value }
    func saveLastSyncStarted(_ date: Date) { lock.lock(); defer { lock.unlock() }; value = date }
    func loadAssessment() -> Assessment? { lock.lock(); defer { lock.unlock() }; return _assessment }
    func saveAssessment(_ assessment: Assessment) { lock.lock(); defer { lock.unlock() }; _assessment = assessment }

    func loadRemainingUploads() -> [AssetResource] { lock.lock(); defer { lock.unlock() }; return _remaining }
    func loadRemainingUploadsDestination() -> Data? { lock.lock(); defer { lock.unlock() }; return _remainingDestination }
    func remainingUploadsSession() -> UInt64 { lock.lock(); defer { lock.unlock() }; return _remainingSession }
    func saveRemainingUploads(_ resources: [AssetResource], destination: Data?, session: UInt64) {
        lock.lock(); defer { lock.unlock() }
        guard session == _remainingSession else { return }
        _remaining = resources
        _remainingDestination = destination
    }
    func clearRemainingUploads() { lock.lock(); defer { lock.unlock() }; _remainingSession &+= 1; _remaining = []; _remainingDestination = nil }
}
