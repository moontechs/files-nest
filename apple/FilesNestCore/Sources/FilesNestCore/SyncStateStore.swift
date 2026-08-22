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

    /// Token identifying the current remaining-uploads session. `clearRemainingUploads()`
    /// invalidates it, so a superseded run — cancellation is cooperative, so it may still
    /// be unwinding — cannot resurrect a list that sign-out or a server change deliberately
    /// dropped. A list from one server must never drive uploads against another.
    func remainingUploadsSession() -> UInt64
    func saveRemainingUploads(_ resources: [AssetResource], session: UInt64)
    func clearRemainingUploads()
}

public extension SyncStateStore {
    /// For callers that are not part of a cancellable run (seeding, tests).
    func saveRemainingUploads(_ resources: [AssetResource]) {
        saveRemainingUploads(resources, session: remainingUploadsSession())
    }
}

/// App-side implementation. Inject a dedicated `UserDefaults(suiteName:)` in
/// tests so it never touches `.standard`. Stored as an ISO-8601 string.
public final class UserDefaultsSyncStateStore: SyncStateStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.sync.lastSyncStarted"
    private let assessmentKey = "com.filesnest.sync.assessment"
    private let remainingKey = "com.filesnest.sync.remainingUploads"
    // Process-local: a writer from an earlier launch is gone by definition.
    private let sessionLock = NSLock()
    private var remainingSession: UInt64 = 0

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

    public func remainingUploadsSession() -> UInt64 {
        sessionLock.lock(); defer { sessionLock.unlock() }
        return remainingSession
    }

    public func saveRemainingUploads(_ resources: [AssetResource], session: UInt64) {
        sessionLock.lock()
        let stale = session != remainingSession
        sessionLock.unlock()
        guard !stale else { return }          // superseded run: its list is no longer ours to write
        if let data = try? JSONEncoder().encode(resources) { defaults.set(data, forKey: remainingKey) }
    }

    public func clearRemainingUploads() {
        sessionLock.lock(); remainingSession &+= 1; sessionLock.unlock()
        defaults.removeObject(forKey: remainingKey)
    }
}
