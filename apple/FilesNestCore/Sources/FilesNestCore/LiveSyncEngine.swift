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
    private let refreshBackedUp: (@Sendable () async throws -> Int)?
    private let now: @Sendable () -> Date

    private let lock = NSLock()
    private var status: SyncStatus = .signedOut
    private var isSyncing = false
    private var pausedFlag = false
    private var syncTask: Task<SyncReport, Error>?
    private var continuations: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]
    private var summary: SyncSummary = .empty
    private var summaryContinuations: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]

    public init(credentials: any CredentialStore,
                state: any SyncStateStore,
                perform: @escaping Perform,
                refreshBackedUp: (@Sendable () async throws -> Int)? = nil,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.state = state
        self.perform = perform
        self.refreshBackedUp = refreshBackedUp
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

    public func summaryStream() -> AsyncStream<SyncSummary> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(summary)         // current summary first
            summaryContinuations[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.summaryContinuations[id] = nil; self.lock.unlock()
            }
        }
    }

    private func setSummary(_ newSummary: SyncSummary) {
        lock.lock()
        summary = newSummary
        let conts = Array(summaryContinuations.values)
        lock.unlock()
        for c in conts { c.yield(newSummary) }
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

    private func storeSyncTask(_ t: Task<SyncReport, Error>?) { lock.lock(); syncTask = t; lock.unlock() }
    private func cancelSyncTask() { lock.lock(); let t = syncTask; lock.unlock(); t?.cancel() }
    private func setPaused(_ v: Bool) { lock.lock(); pausedFlag = v; lock.unlock() }
    private var isPausedFlag: Bool { lock.lock(); defer { lock.unlock() }; return pausedFlag }

    /// Last sync = the coordinator-persisted start time (single source of truth).
    private var lastSync: Date? { state.loadLastSyncStarted() }

    public func start() async {
        let creds = try? await credentials.basicCredentials()
        guard creds != nil else { set(.signedOut); setSummary(.empty); return }   // drop stale failures
        setPaused(false)
        set(.watching(lastSync: lastSync))
        if let refresh = refreshBackedUp, let count = try? await refresh() {
            setSummary(SyncSummary(backedUp: count, failed: []))
        }
    }

    public func pause() async {
        guard !isSignedOut else { return }
        setPaused(true)
        set(.paused(pending: 0))
        cancelSyncTask()                 // stop any in-flight sync (coordinator checks cancellation)
    }

    public func resume() async {
        guard !isSignedOut else { return }
        setPaused(false)
        set(.watching(lastSync: lastSync))
    }

    public func syncNow() async {
        guard !isSignedOut, !isPaused else { return }
        guard beginSyncing() else { return }        // re-entrancy guard
        defer { endSyncing() }

        set(.syncing(SyncProgress(completed: 0, total: 0,
                                  currentItemName: nil, bytesRemaining: nil)))

        let task = Task { [perform] () throws -> SyncReport in
            try await perform(.all) { [weak self] progress in
                guard let self, !self.isPausedFlag else { return }   // don't overwrite .paused with late progress
                self.set(.syncing(progress))
            }
        }
        storeSyncTask(task)
        defer { storeSyncTask(nil) }
        if isPausedFlag { task.cancel() }        // pause raced ahead of the store → cancel now

        do {
            let report = try await task.value
            if !report.failed.isEmpty { logFailures(report.failed) }
            // Backed up = live server count; fall back to the report if no refresh or it fails.
            let backedUp: Int
            if let refresh = refreshBackedUp, let count = try? await refresh() {
                backedUp = count
            } else {
                backedUp = report.skipped + report.uploaded.count
            }
            setSummary(SyncSummary(backedUp: backedUp, failed: report.failed))
            if !isPausedFlag { set(.watching(lastSync: lastSync)) }   // paused during the run → stay .paused
        } catch is CancellationError {
            // Paused → stay paused; a non-pause cancellation returns to idle.
            if isPausedFlag { set(.paused(pending: 0)) } else { set(.watching(lastSync: lastSync)) }
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
