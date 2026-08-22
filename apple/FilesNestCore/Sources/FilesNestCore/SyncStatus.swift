import Foundation

public struct SyncProgress: Sendable, Equatable {
    public let completed: Int
    public let total: Int
    public let currentItemName: String?
    public let currentItemID: String?     // PHAsset local identifier, for the thumbnail
    public let bytesRemaining: Int64?
    public let inFlight: Int              // uploads currently in flight (concurrency)

    public init(completed: Int, total: Int, currentItemName: String?,
                bytesRemaining: Int64?, currentItemID: String? = nil, inFlight: Int = 0) {
        self.completed = completed
        self.total = total
        self.currentItemName = currentItemName
        self.currentItemID = currentItemID
        self.bytesRemaining = bytesRemaining
        self.inFlight = inFlight
    }

    /// 0.0…1.0; 0 when `total == 0`. Drives the panel's progress ring.
    public var fraction: Double { total > 0 ? Double(completed) / Double(total) : 0 }
}

public enum SyncStatus: Sendable, Equatable {
    case signedOut                        // no credentials → "Sign in in Settings"
    case counting(done: Int, total: Int)  // launch scan in progress (determinate)
    case watching(lastSync: Date?)        // idle, monitoring for new items
    case syncing(SyncProgress)
    case paused(pending: Int)
    case error(message: String)
}
