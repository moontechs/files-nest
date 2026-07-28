import Foundation

/// A real `SyncEngine` that runs a single `SyncCoordinator` pass on `syncNow()`
/// and publishes `SyncStatus`. Continuous watching (a `PHPhotoLibraryChangeObserver`
/// + scheduler) is a later slice — hence "Live", not "Continuous": this drives the
/// one-shot Sync Now against the live library + server.
///
/// PhotoKit-free by construction: the app's composition root builds the real
/// `SyncCoordinator` (+ PhotoKit adapters) into the injected `perform` closure,
/// keeping this unit headless-testable with a fake `perform`.
public final class LiveSyncEngine: SyncEngine, @unchecked Sendable {
    public typealias Perform =
        @Sendable (SyncRange, @Sendable (SyncProgress) -> Void) async throws -> SyncReport

    private let credentials: any CredentialStore
    private let state: any SyncStateStore
    private let perform: Perform
    private let now: @Sendable () -> Date

    private let lock = NSLock()
    private var status: SyncStatus = .signedOut
    private var isSyncing = false
    private var continuations: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]

    public init(credentials: any CredentialStore,
                state: any SyncStateStore,
                perform: @escaping Perform,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.state = state
        self.perform = perform
        self.now = now
    }

    public func statusStream() -> AsyncStream<SyncStatus> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(status)          // current status first
            continuations[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.continuations[id] = nil; self.lock.unlock()
            }
        }
    }

    private func set(_ newStatus: SyncStatus) {
        lock.lock()
        status = newStatus
        let conts = Array(continuations.values)
        lock.unlock()
        for c in conts { c.yield(newStatus) }
    }

    private var isSignedOut: Bool {
        lock.lock(); defer { lock.unlock() }
        if case .signedOut = status { return true }
        return false
    }

    private var isPaused: Bool {
        lock.lock(); defer { lock.unlock() }
        if case .paused = status { return true }
        return false
    }

    /// Atomically claim the single sync slot. Returns false if one is already running.
    private func beginSyncing() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if isSyncing { return false }
        isSyncing = true
        return true
    }

    private func endSyncing() { lock.lock(); isSyncing = false; lock.unlock() }

    /// Last sync = the coordinator-persisted start time (single source of truth).
    private var lastSync: Date? { state.loadLastSyncStarted() }

    public func start() async {
        let creds = try? await credentials.basicCredentials()
        set(creds == nil ? .signedOut : .watching(lastSync: lastSync))
    }

    public func pause() async {
        guard !isSignedOut else { return }
        set(.paused(pending: 0))
    }

    public func resume() async {
        guard !isSignedOut else { return }
        set(.watching(lastSync: lastSync))
    }

    public func syncNow() async {
        guard !isSignedOut, !isPaused else { return }
        guard beginSyncing() else { return }        // re-entrancy guard
        defer { endSyncing() }

        set(.syncing(SyncProgress(completed: 0, total: 0,
                                  currentItemName: nil, bytesRemaining: nil)))
        do {
            let report = try await perform(.all) { [weak self] progress in
                self?.set(.syncing(progress))
            }
            if !report.failed.isEmpty { logFailures(report.failed) }
            set(.watching(lastSync: lastSync))
        } catch is CancellationError {
            set(.watching(lastSync: lastSync))       // cancellation is not an error
        } catch {
            set(.error(message: String(describing: error)))
        }
    }

    /// Per-item failures don't fail the whole sync (skip-and-continue). Surface them
    /// to the log; a later slice renders `SyncReport.failed` in the panel.
    private func logFailures(_ failed: [FailedItem]) {
        for item in failed {
            print("FilesNest sync: failed \(item.key.encoded): \(item.reason)")
        }
    }
}
