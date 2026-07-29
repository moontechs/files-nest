import Foundation

/// A snapshot of backup state observed by the panel.
///
/// `backedUp` is the server's completed count. `libraryTotal` is the (cheap) count of
/// assets in the local library — used for an at-rest "pending" estimate and to size the
/// "Scanning…" label without the expensive full-resource enumeration. It's `nil` until
/// first known (e.g. before Photos access). At-rest pending is derived by the panel as
/// `libraryTotal - backedUp`; exact per-resource pending awaits the continuous-watching slice.
public struct SyncSummary: Sendable, Equatable {
    public let backedUp: Int          // library resources confirmed complete on the server
    public let failed: [FailedItem]   // items that failed in the last sync (empty = none)
    public let libraryTotal: Int?     // cheap local asset count; nil if unknown

    public init(backedUp: Int, failed: [FailedItem], libraryTotal: Int? = nil) {
        self.backedUp = backedUp
        self.failed = failed
        self.libraryTotal = libraryTotal
    }

    public static let empty = SyncSummary(backedUp: 0, failed: [], libraryTotal: nil)
}
