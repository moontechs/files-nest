import Foundation

/// A snapshot of backup state observed by the panel: the server's completed count and
/// the last run's failures. An exact at-rest *pending* count needs a per-resource library
/// diff (the continuous-watching slice); the panel shows pending only live during a sync.
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int          // library resources confirmed complete on the server
    public let failed: [FailedItem]   // items that failed in the last sync (empty = none)

    public init(backedUp: Int, failed: [FailedItem]) {
        self.backedUp = backedUp
        self.failed = failed
    }

    public static let empty = SyncSummary(backedUp: 0, failed: [])
}
