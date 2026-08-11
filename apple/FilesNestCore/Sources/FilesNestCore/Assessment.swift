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

/// Escaping progress sink handed to an `assess` pass so it can forward scan progress
/// (assets enumerated of the asset count) to `AssetLibrary.resources(in:onProgress:)`.
/// A wrapper (not a bare closure param) because a closure literal's function-type
/// parameter is non-escaping and can't be forwarded to the library's escaping hook.
public struct AssessProgress: Sendable {
    public let report: @Sendable (_ done: Int, _ total: Int) -> Void
    public init(_ report: @escaping @Sendable (_ done: Int, _ total: Int) -> Void) { self.report = report }
    public func callAsFunction(_ done: Int, _ total: Int) { report(done, total) }
}
