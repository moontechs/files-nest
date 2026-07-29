import Foundation

/// A snapshot of backup state after the last completed sync, observed by the panel.
/// `Pending` is not stored here — the panel derives it live from `.syncing` progress
/// (design §4.3).
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int          // library resources confirmed complete after the last sync
    public let failed: [FailedItem]   // items that failed in the last sync (empty = none)

    public init(backedUp: Int, failed: [FailedItem]) {
        self.backedUp = backedUp
        self.failed = failed
    }

    public static let empty = SyncSummary(backedUp: 0, failed: [])
}
