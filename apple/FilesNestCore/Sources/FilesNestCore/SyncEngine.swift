import Foundation

/// The seam the panel observes and the sync engine implements. This slice ships
/// `StubSyncEngine`; the next slice replaces it with the PhotoKit-backed engine.
public protocol SyncEngine: Sendable {
    /// The current status followed by every change. Each call returns an
    /// independent stream whose first element is the current status.
    func statusStream() -> AsyncStream<SyncStatus>
    /// The current summary followed by every change. Each call returns an
    /// independent stream whose first element is the current summary.
    func summaryStream() -> AsyncStream<SyncSummary>
    func start() async     // reconcile signed-in/out from credentials, begin watching
    func pause() async
    func resume() async
    func syncNow() async   // manual trigger
    func libraryDidChange() async   // a debounced host signal that the photo library changed
    func reconcile() async   // a configuration change (Settings save): force a full reconcile now
}
