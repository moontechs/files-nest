import Foundation

/// The only durable client state. `lastSyncStarted` supports incremental-range
/// selection and a "last synced" UI label; crash-resume is emergent from
/// re-diffing (spec §3, decision 3), so no queue position is stored.
public protocol SyncStateStore: Sendable {
    func loadLastSyncStarted() -> Date?
    func saveLastSyncStarted(_ date: Date)
}

/// App-side implementation. Inject a dedicated `UserDefaults(suiteName:)` in
/// tests so it never touches `.standard`. Stored as an ISO-8601 string.
public final class UserDefaultsSyncStateStore: SyncStateStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.sync.lastSyncStarted"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func loadLastSyncStarted() -> Date? {
        guard let s = defaults.string(forKey: key) else { return nil }
        return ISO8601DateFormatter().date(from: s)
    }

    public func saveLastSyncStarted(_ date: Date) {
        defaults.set(ISO8601DateFormatter().string(from: date), forKey: key)
    }
}
