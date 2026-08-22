import Foundation

/// The only durable client state. `lastSyncStarted` supports incremental-range
/// selection and a "last synced" UI label; crash-resume is emergent from
/// re-diffing (spec §3, decision 3), so no queue position is stored.
public protocol SyncStateStore: Sendable {
    func loadLastSyncStarted() -> Date?
    func saveLastSyncStarted(_ date: Date)
    func loadAssessment() -> Assessment?
    func saveAssessment(_ assessment: Assessment)

    /// The not-yet-uploaded resources of the last run, so Resume and a cold launch
    /// can upload straight away instead of re-counting the whole library.
    /// `[]` when absent or undecodable (clean fallback to a normal count).
    func loadRemainingUploads() -> [AssetResource]
    func saveRemainingUploads(_ resources: [AssetResource])
    func clearRemainingUploads()
}

/// App-side implementation. Inject a dedicated `UserDefaults(suiteName:)` in
/// tests so it never touches `.standard`. Stored as an ISO-8601 string.
public final class UserDefaultsSyncStateStore: SyncStateStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.sync.lastSyncStarted"
    private let assessmentKey = "com.filesnest.sync.assessment"
    private let remainingKey = "com.filesnest.sync.remainingUploads"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func loadLastSyncStarted() -> Date? {
        guard let s = defaults.string(forKey: key) else { return nil }
        return ISO8601DateFormatter().date(from: s)
    }

    public func saveLastSyncStarted(_ date: Date) {
        defaults.set(ISO8601DateFormatter().string(from: date), forKey: key)
    }

    public func loadAssessment() -> Assessment? {
        guard let data = defaults.data(forKey: assessmentKey) else { return nil }
        return try? JSONDecoder().decode(Assessment.self, from: data)
    }

    public func saveAssessment(_ assessment: Assessment) {
        if let data = try? JSONEncoder().encode(assessment) { defaults.set(data, forKey: assessmentKey) }
    }

    public func loadRemainingUploads() -> [AssetResource] {
        guard let data = defaults.data(forKey: remainingKey) else { return [] }
        return (try? JSONDecoder().decode([AssetResource].self, from: data)) ?? []
    }

    public func saveRemainingUploads(_ resources: [AssetResource]) {
        if let data = try? JSONEncoder().encode(resources) { defaults.set(data, forKey: remainingKey) }
    }

    public func clearRemainingUploads() { defaults.removeObject(forKey: remainingKey) }
}
