import Foundation

public struct SyncProgress: Sendable, Equatable {
    public let completed: Int
    public let total: Int
    public let currentItemName: String?
    public let currentItemID: String?     // PHAsset local identifier, for the thumbnail
    public let bytesRemaining: Int64?
    public let inFlight: Int              // uploads currently in flight (concurrency)
    public let retry: RetryProgress?      // reconnecting requests, if the server is unavailable

    public init(completed: Int, total: Int, currentItemName: String?,
                bytesRemaining: Int64?, currentItemID: String? = nil, inFlight: Int = 0,
                retry: RetryProgress? = nil) {
        self.completed = completed
        self.total = total
        self.currentItemName = currentItemName
        self.currentItemID = currentItemID
        self.bytesRemaining = bytesRemaining
        self.inFlight = inFlight
        self.retry = retry
    }

    /// 0.0…1.0; 0 when `total == 0`. Drives the panel's progress ring.
    public var fraction: Double { total > 0 ? Double(completed) / Double(total) : 0 }
}

public struct RetryProgress: Sendable, Equatable {
    public let retryAt: Date
    public let waitingRequests: Int

    public init(retryAt: Date, waitingRequests: Int) {
        self.retryAt = retryAt
        self.waitingRequests = waitingRequests
    }
}

/// Why a count is running, so the panel can say what it is doing. A count that follows a
/// completed upload is a verification pass, not a fresh survey — without this the two are
/// indistinguishable and the second one reads as "it started over".
public enum CountPurpose: Sendable, Equatable {
    case survey     // establishing the backlog: launch, sign-in, a library change
    case verify     // confirming what was just uploaded, and catching what changed meanwhile
}

public enum SyncStatus: Sendable, Equatable {
    case signedOut                        // no credentials → "Sign in in Settings"
    case counting(done: Int, total: Int, purpose: CountPurpose = .survey)  // scan in progress (determinate)
    case watching(lastSync: Date?)        // idle, monitoring for new items
    case syncing(SyncProgress)
    case reconnecting(SyncProgress)
    case paused(pending: Int)
    case error(message: String)
}

public extension SyncStatus {
    /// Whether each control would actually do something. These mirror the guards in
    /// `LiveSyncEngine`'s command handlers, so a button is never offered for a command
    /// the engine will drop on the floor.
    ///
    /// `doSyncNow` requires `syncChild == nil` and returns early when paused; superseding
    /// an in-flight count would restart its scan from zero.
    var canSyncNow: Bool {
        switch self {
        case .watching, .error:                     return true
        case .syncing, .reconnecting, .paused, .counting, .signedOut: return false
        }
    }

    /// `doPause` is a no-op when signed out or already paused. Counting is excluded by
    /// policy rather than capability: the engine would cancel the scan, but `.paused`
    /// carries the remaining of a RUN, so pausing a count would report "0 pending".
    var canPause: Bool {
        switch self {
        case .syncing, .reconnecting, .watching, .error: return true
        case .paused, .counting, .signedOut: return false
        }
    }

    /// `doResume` only acts from `.paused`.
    var canResume: Bool { if case .paused = self { return true }; return false }
}
