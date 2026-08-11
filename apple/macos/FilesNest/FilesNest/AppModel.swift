import SwiftUI
import Combine
import FilesNestCore

@MainActor
final class AppModel: ObservableObject {
    @Published private(set) var status: SyncStatus = .signedOut
    @Published private(set) var summary: SyncSummary = .empty
    let engine: any SyncEngine
    private var streamTask: Task<Void, Never>?
    private var summaryTask: Task<Void, Never>?

    init(engine: any SyncEngine) { self.engine = engine }

    /// Subscribe to the engine's streams for the UI. Idempotent. The engine is *started*
    /// at app launch (see `FilesNestApp.init`), not here — the menu-bar panel may never be
    /// opened, and launch catch-up + continuous watching must run regardless.
    func begin() {
        guard streamTask == nil else { return }
        streamTask = Task { [engine] in
            for await s in engine.statusStream() { self.status = s }
        }
        summaryTask = Task { [engine] in
            for await s in engine.summaryStream() { self.summary = s }
        }
    }

    /// Force a full reconcile after a Settings save (config change) — supersedes any active run.
    func restart() { Task { await engine.reconcile() } }

    func pause()   { Task { await engine.pause() } }
    func resume()  { Task { await engine.resume() } }
    func syncNow() { Task { await engine.syncNow() } }
}
