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

public enum FailureKind: Sendable, Equatable { case upload, delete }

public struct FailedItem: Sendable, Equatable {
    public let key: ResourceKey
    public let filename: String   // human-readable; the failed-items UI renders this
    public let reason: String
    public let kind: FailureKind  // upload vs delete — only upload failures count as at-rest Pending

    public init(key: ResourceKey, filename: String, reason: String, kind: FailureKind = .upload) {
        self.key = key
        self.filename = filename
        self.reason = reason
        self.kind = kind
    }
}
