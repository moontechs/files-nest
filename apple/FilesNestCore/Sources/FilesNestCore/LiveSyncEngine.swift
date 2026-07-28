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
/// A single `NSLock` guards all mutable state; it is never held across an `await`.
/// Two monotonic counters make lifecycle transitions race-free:
///
/// - **`generation`** gates run publishes. `beginRun` and every status transition
///   (`pause`/`resume`/sign-out/sign-in) bump it under the lock; a running sync's
///   progress/summary/terminal publishes go through `publish(gen:)` and are dropped
///   once the generation moves on — so a transition landing mid-sync can't be
///   clobbered by the superseded run.
/// - **`lifecycleEpoch`** orders `start()` against everything else. `start()` reads
///   credentials via an `await`, so it snapshots the epoch at entry and re-checks it
///   (under the same lock as its apply) before publishing; `pause`/`resume`/`beginRun`
///   and other `start()`s all bump it, so a stale `start()` decision is dropped rather
///   than overwriting a newer sign-out/pause/resume.
///
/// Synchronous transitions (`pause`/`resume`/`beginRun`) do their guard **and** their
/// state change under one lock hold, so no check-then-act window exists.
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
    private var generation: UInt64 = 0        // gates run publishes
    private var lifecycleEpoch: UInt64 = 0    // orders start() against other transitions
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

    /// Claim the single sync slot: guard + counter bumps + initial `.syncing(0/0)`,
    /// all atomically so no lifecycle op can interleave between the entry check and the
    /// first publish. Returns the run's generation, or nil if a sync can't start.
    private func beginRun() -> UInt64? {
        lock.lock()
        if isSyncing { lock.unlock(); return nil }
        if case .signedOut = status { lock.unlock(); return nil }
        if case .paused = status { lock.unlock(); return nil }
        lifecycleEpoch &+= 1
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

    /// Last sync = the coordinator-persisted start time (single source of truth).
    private var lastSync: Date? { state.loadLastSyncStarted() }

    // MARK: - SyncEngine

    public func start() async {
        let myEpoch = claimLifecycleEpoch()
        let creds = try? await credentials.basicCredentials()
        let ls = lastSync
        let (refreshGen, cancelTask) = applyStart(myEpoch: myEpoch, signedIn: creds != nil, lastSync: ls)
        cancelTask?.cancel()
        if let gen = refreshGen, let refresh = refreshBackedUp, let count = try? await refresh() {
            publish(gen: gen, summary: SyncSummary(backedUp: count, failed: []))
        }
    }

    public func pause() async { pauseLocked()?.cancel() }   // coordinator checks cancellation between items

    public func resume() async { resumeLocked() }

    // MARK: - Synchronous locked transitions (NSLock is illegal in async contexts)

    private func claimLifecycleEpoch() -> UInt64 {
        lock.lock(); lifecycleEpoch &+= 1; let e = lifecycleEpoch; lock.unlock(); return e
    }

    /// Final atomic apply for `start()`. Returns the generation to refresh the summary
    /// under (nil = don't) and any in-flight task to cancel. Drops if superseded.
    private func applyStart(myEpoch: UInt64, signedIn: Bool, lastSync ls: Date?) -> (refreshGen: UInt64?, cancel: Task<SyncReport, Error>?) {
        lock.lock()
        guard myEpoch == lifecycleEpoch else { lock.unlock(); return (nil, nil) }
        if !signedIn {
            generation &+= 1
            status = .signedOut
            summary = .empty
            let scs = Array(statusConts.values)
            let mcs = Array(summaryConts.values)
            let t = syncTask
            lock.unlock()
            for c in scs { c.yield(.signedOut) }
            for c in mcs { c.yield(.empty) }
            return (nil, t)                              // stop any in-flight sync
        }
        // Signed in. Don't clobber a running sync's status — just refresh the count.
        if isSyncing {
            let g = generation
            lock.unlock()
            return (g, nil)
        }
        generation &+= 1
        let g = generation
        let s = SyncStatus.watching(lastSync: ls)
        status = s
        let scs = Array(statusConts.values)
        lock.unlock()
        for c in scs { c.yield(s) }
        return (g, nil)
    }

    private func pauseLocked() -> Task<SyncReport, Error>? {
        lock.lock()
        if case .signedOut = status { lock.unlock(); return nil }   // atomic guard + transition
        lifecycleEpoch &+= 1
        generation &+= 1
        status = .paused(pending: 0)
        let scs = Array(statusConts.values)
        let t = syncTask
        lock.unlock()
        for c in scs { c.yield(.paused(pending: 0)) }
        return t
    }

    private func resumeLocked() {
        let ls = lastSync
        lock.lock()
        if case .signedOut = status { lock.unlock(); return }
        lifecycleEpoch &+= 1
        generation &+= 1
        let s = SyncStatus.watching(lastSync: ls)
        status = s
        let scs = Array(statusConts.values)
        lock.unlock()
        for c in scs { c.yield(s) }
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
