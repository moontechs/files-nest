import SwiftUI
import Combine
import FilesNestCore

@MainActor
final class AppModel: ObservableObject {
    @Published private(set) var status: SyncStatus = .signedOut
    let engine: any SyncEngine
    private var streamTask: Task<Void, Never>?

    init(engine: any SyncEngine) { self.engine = engine }

    /// Subscribe to the engine and start it. Idempotent.
    func begin() {
        guard streamTask == nil else { return }
        streamTask = Task { [engine] in
            for await s in engine.statusStream() { self.status = s }
        }
        Task { await engine.start() }
    }

    /// Re-reconcile after credentials change (Settings save).
    func restart() { Task { await engine.start() } }

    func pause()   { Task { await engine.pause() } }
    func resume()  { Task { await engine.resume() } }
    func syncNow() { Task { await engine.syncNow() } }
}
