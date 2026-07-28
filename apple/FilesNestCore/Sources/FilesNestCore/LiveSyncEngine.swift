import Foundation

/// A real `SyncEngine` that runs a single `SyncCoordinator` pass on `syncNow()`
/// and publishes `SyncStatus`. Continuous watching (a `PHPhotoLibraryChangeObserver`
/// + scheduler) is a later slice — hence "Live", not "Continuous": this drives the
/// one-shot Sync Now against the live library + server.
///
/// PhotoKit-free by construction: the app's composition root builds the real
/// `SyncCoordinator` (+ PhotoKit adapters) into the injected `perform` closure,
/// keeping this unit headless-testable with a fake `perform`.
///
/// ## Concurrency
/// A single `NSLock` guards all mutable state. Lifecycle transitions
/// (`pause`/`resume`/sign-out/new sync) atomically **bump `generation`** and set a
/// terminal status under one lock hold. Every publish that belongs to a running sync
/// is gated on its captured generation (`publish(gen:…)`), so a transition that lands
/// mid-sync causes the run's later progress/summary/terminal publishes to be dropped
/// rather than clobbering the new state. The lock is never held across an `await`.
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
    private var summary: SyncSummary = .empty
    private var generation: UInt64 = 0        // bumped by every lifecycle transition
    private var isSyncing = false
    private var syncTask: Task<SyncReport, Error>?
    private var statusConts: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]
    private var summaryConts: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]

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

    // MARK: - Streams

    public func statusStream() -> AsyncStream<SyncStatus> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(status)          // current status first
            statusConts[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.statusConts[id] = nil; self.lock.unlock()
            }
        }
    }

    public func summaryStream() -> AsyncStream<SyncSummary> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(summary)         // current summary first
            summaryConts[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.summaryConts[id] = nil; self.lock.unlock()
            }
        }
    }

    // MARK: - Generation-gated publishing

    /// Atomically bump the generation and set a terminal status (+ optional summary),
    /// superseding any in-flight sync. Returns the new generation and the in-flight
    /// task to cancel *outside* the lock.
    @discardableResult
    private func supersede(status newStatus: SyncStatus,
                           summary newSummary: SyncSummary? = nil) -> (gen: UInt64, task: Task<SyncReport, Error>?) {
        lock.lock()
        generation &+= 1
        let gen = generation
        status = newStatus
        let statusListeners = Array(statusConts.values)
        var summaryListeners: [AsyncStream<SyncSummary>.Continuation] = []
        if let newSummary { summary = newSummary; summaryListeners = Array(summaryConts.values) }
        let t = syncTask
        lock.unlock()
        for c in statusListeners { c.yield(newStatus) }
        if let newSummary { for c in summaryListeners { c.yield(newSummary) } }
        return (gen, t)
    }

    /// Publish a status only if `gen` is still the current generation.
    private func publish(gen: UInt64, status newStatus: SyncStatus) {
        lock.lock()
        guard gen == generation else { lock.unlock(); return }
        status = newStatus
        let listeners = Array(statusConts.values)
        lock.unlock()
        for c in listeners { c.yield(newStatus) }
    }

    /// Publish a summary only if `gen` is still the current generation.
    private func publish(gen: UInt64, summary newSummary: SyncSummary) {
        lock.lock()
        guard gen == generation else { lock.unlock(); return }
        summary = newSummary
        let listeners = Array(summaryConts.values)
        lock.unlock()
        for c in listeners { c.yield(newSummary) }
    }

    /// Claim the single sync slot: guard + generation bump + initial `.syncing(0/0)`,
    /// all atomically so `pause`/sign-out cannot interleave between the entry check and
    /// the first publish. Returns the run's generation, or nil if a sync can't start.
    private func beginRun() -> UInt64? {
        lock.lock()
        if isSyncing { lock.unlock(); return nil }
        if case .signedOut = status { lock.unlock(); return nil }
        if case .paused = status { lock.unlock(); return nil }
        generation &+= 1
        let gen = generation
        isSyncing = true
        let initial = SyncStatus.syncing(SyncProgress(completed: 0, total: 0,
                                                      currentItemName: nil, bytesRemaining: nil))
        status = initial
        let listeners = Array(statusConts.values)
        lock.unlock()
        for c in listeners { c.yield(initial) }
        return gen
    }

    private func endRun() { lock.lock(); isSyncing = false; syncTask = nil; lock.unlock() }
    private func storeSyncTask(_ t: Task<SyncReport, Error>?) { lock.lock(); syncTask = t; lock.unlock() }
    private func currentGeneration() -> UInt64 { lock.lock(); defer { lock.unlock() }; return generation }

    private var isSignedOut: Bool {
        lock.lock(); defer { lock.unlock() }
        if case .signedOut = status { return true }
        return false
    }

    /// Last sync = the coordinator-persisted start time (single source of truth).
    private var lastSync: Date? { state.loadLastSyncStarted() }

    // MARK: - SyncEngine

    public func start() async {
        let creds = try? await credentials.basicCredentials()
        guard creds != nil else {
            let (_, t) = supersede(status: .signedOut, summary: .empty)   // drop stale state + stop in-flight work
            t?.cancel()
            return
        }
        let (gen, _) = supersede(status: .watching(lastSync: lastSync))
        // Live "Backed up" refresh; gated so a sign-out landing after this drops the stale value.
        if let refresh = refreshBackedUp, let count = try? await refresh() {
            publish(gen: gen, summary: SyncSummary(backedUp: count, failed: []))
        }
    }

    public func pause() async {
        guard !isSignedOut else { return }
        let (_, t) = supersede(status: .paused(pending: 0))
        t?.cancel()                          // coordinator checks cancellation between items
    }

    public func resume() async {
        guard !isSignedOut else { return }
        supersede(status: .watching(lastSync: lastSync))
    }

    public func syncNow() async {
        guard let gen = beginRun() else { return }
        defer { endRun() }

        let task = Task { [perform] () throws -> SyncReport in
            try await perform(.all) { [weak self] progress in
                self?.publish(gen: gen, status: .syncing(progress))   // dropped if superseded
            }
        }
        storeSyncTask(task)
        if currentGeneration() != gen { task.cancel() }   // pause/sign-out raced ahead of the store

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
            publish(gen: gen, summary: SyncSummary(backedUp: backedUp, failed: report.failed))
            publish(gen: gen, status: .watching(lastSync: lastSync))
        } catch is CancellationError {
            // A superseder (pause/sign-out) already set the terminal status; this publish
            // is dropped when the generation moved on, and only applies for a stray cancel.
            publish(gen: gen, status: .watching(lastSync: lastSync))
        } catch {
            publish(gen: gen, status: .error(message: String(describing: error)))
        }
    }

    /// Per-item failures don't fail the whole sync (skip-and-continue). Surface them
    /// to the log; the panel renders `SyncReport.failed` via the summary.
    private func logFailures(_ failed: [FailedItem]) {
        for item in failed {
            print("FilesNest sync: failed \(item.key.encoded): \(item.reason)")
        }
    }
}
