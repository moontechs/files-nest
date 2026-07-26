import Foundation

public struct SyncReport: Sendable, Equatable {
    public let uploaded: [ResourceKey]
    public let deleted: [ResourceKey]
    public let failed: [FailedItem]
    public let skipped: Int

    public init(uploaded: [ResourceKey], deleted: [ResourceKey], failed: [FailedItem], skipped: Int) {
        self.uploaded = uploaded
        self.deleted = deleted
        self.failed = failed
        self.skipped = skipped
    }
}

public struct FailedItem: Sendable, Equatable {
    public let key: ResourceKey
    public let reason: String   // human-readable; the UI slice renders these as a list

    public init(key: ResourceKey, reason: String) {
        self.key = key
        self.reason = reason
    }
}
