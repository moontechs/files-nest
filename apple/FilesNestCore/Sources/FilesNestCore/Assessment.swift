import Foundation

/// A snapshot of backup state computed by a full library assessment (scan + server diff).
/// Cached so a warm launch shows numbers instantly while a fresh count runs.
public struct Assessment: Sendable, Equatable, Codable {
    public let backedUp: Int      // server records with status == .complete
    public let pending: Int       // SyncPlanner.plan(...).uploads.count
    public let resourceTotal: Int // library resources enumerated

    public init(backedUp: Int, pending: Int, resourceTotal: Int) {
        self.backedUp = backedUp
        self.pending = pending
        self.resourceTotal = resourceTotal
    }
}
