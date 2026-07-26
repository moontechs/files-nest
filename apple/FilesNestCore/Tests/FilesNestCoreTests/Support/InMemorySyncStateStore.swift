import Foundation
@testable import FilesNestCore

/// Deterministic test double. `final class` behind a lock (Sendable; mutated
/// from the coordinator). Exact `Date` is preserved, unlike the ISO-8601 store.
final class InMemorySyncStateStore: SyncStateStore, @unchecked Sendable {
    private let lock = NSLock()
    private var value: Date?

    func loadLastSyncStarted() -> Date? { lock.lock(); defer { lock.unlock() }; return value }
    func saveLastSyncStarted(_ date: Date) { lock.lock(); defer { lock.unlock() }; value = date }
}
