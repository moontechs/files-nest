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
/// ## Concurrency — serial command loop
/// `start`/`pause`/`resume`/`syncNow` do not touch state directly; they enqueue a
/// `Command`. A single consumer task drains the queue and processes commands **one at
/// a time, awaits included** — so lifecycle transitions can never interleave, which is
/// what earlier lock/generation gating could not guarantee (`start()` suspends in the
/// keychain read; the sync suspends for a long time). Only the consumer mutates engine
/// state, so no lock is needed for that logic.
///
/// The long-running sync must not block the consumer (or `pause` couldn't interrupt it),
/// so `syncNow` launches a cancellable **child task** that reports back via internal
/// commands (`progress`/`finished`/`failed`). Those carry the run's `generation`; the
/// consumer applies them only if the generation still matches, so results from a run
/// that was paused/superseded are dropped. `generation` is consumer-only state.
///
/// A separate `fanoutLock` guards only the published `status`/`summary` snapshot and the
/// stream continuation registries, since `statusStream()`/`summaryStream()` are called
/// from other threads. It is never held across an `await`.
public final class LiveSyncEngine: SyncEngine, @unchecked Sendable {
    public typealias Perform =
        @Sendable (SyncRange, @Sendable (SyncProgress) -> Void) async throws -> SyncReport

    private enum Command: Sendable {
        case start, pause, resume, syncNow
        case progress(gen: UInt64, SyncProgress)
        case finished(gen: UInt64, SyncReport)
        case failed(gen: UInt64, message: String)
        case summaryRefreshed(gen: UInt64, backedUp: Int, libraryTotal: Int)   // off-consumer counts
        case barrier(@Sendable () -> Void)   // test-only: resumes once all prior commands are processed
    }

    private let credentials: any CredentialStore
    private let state: any SyncStateStore
    private let perform: Perform
    private let refreshCounts: (@Sendable () async throws -> (backedUp: Int, libraryTotal: Int))?
    private let now: @Sendable () -> Date

    // Consumer-only state (mutated exclusively by the single consumer task).
    private var generation: UInt64 = 0
    private var signedIn = false
    private var syncChild: Task<Void, Never>?
    private var lastProgress: SyncProgress?   // latest progress of the running sync, for pause's pending count
    private var syncBaseBackedUp = 0          // backed-up count at the current sync's start, for a live climb

    // Published snapshot + stream registries (read from arbitrary threads → fanoutLock).
    private let fanoutLock = NSLock()
    private var status: SyncStatus = .signedOut
    private var summary: SyncSummary = .empty
    private var statusConts: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]
    private var summaryConts: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]

    private let submit: @Sendable (Command) -> Void
    private let finishCommands: @Sendable () -> Void

    public init(credentials: any CredentialStore,
                state: any SyncStateStore,
                perform: @escaping Perform,
                refreshCounts: (@Sendable () async throws -> (backedUp: Int, libraryTotal: Int))? = nil,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.state = state
        self.perform = perform
        self.refreshCounts = refreshCounts
        self.now = now

        let (stream, continuation) = AsyncStream.makeStream(of: Command.self)
        self.submit = { continuation.yield($0) }
        self.finishCommands = { continuation.finish() }
        Task { [weak self] in
            for await command in stream { await self?.handle(command) }
        }
    }

    deinit { syncChild?.cancel(); finishCommands() }   // stop in-flight upload work when dropped

    // MARK: - Streams

    public func statusStream() -> AsyncStream<SyncStatus> {
        AsyncStream { continuation in
            let id = UUID()
            fanoutLock.lock()
            continuation.yield(status)          // current status first
            statusConts[id] = continuation
            fanoutLock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.fanoutLock.lock(); self.statusConts[id] = nil; self.fanoutLock.unlock()
            }
        }
    }

    public func summaryStream() -> AsyncStream<SyncSummary> {
        AsyncStream { continuation in
            let id = UUID()
            fanoutLock.lock()
            continuation.yield(summary)         // current summary first
            summaryConts[id] = continuation
            fanoutLock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.fanoutLock.lock(); self.summaryConts[id] = nil; self.fanoutLock.unlock()
            }
        }
    }

    // MARK: - SyncEngine (enqueue only)

    public func start() async  { submit(.start) }
    public func pause() async  { submit(.pause) }
    public func resume() async { submit(.resume) }
    public func syncNow() async { submit(.syncNow) }

    // MARK: - Consumer (single task; no interleaving)

    private func handle(_ command: Command) async {
        switch command {
        case .start:   await doStart()
        case .pause:   doPause()
        case .resume:  doResume()
        case .syncNow: doSyncNow()
        case .progress(let gen, let p):
            if gen == generation {
                lastProgress = p
                // Live climb: each completed upload is one more file on the server. Reconciled
                // to the true server count by the post-completion refresh.
                setSummary(SyncSummary(backedUp: syncBaseBackedUp + p.completed,
                                       failed: currentSummary.failed, libraryTotal: currentSummary.libraryTotal))
                setStatus(.syncing(p))
            }
        case .finished(let gen, let report):
            if gen == generation { finishSync(report) }
        case .failed(let gen, let message):
            if gen == generation { syncChild = nil; lastProgress = nil; setStatus(.error(message: message)) }
        case .summaryRefreshed(let gen, let backedUp, let libraryTotal):
            // libraryTotal is generation-independent (just the local library size), so apply it
            // even if the run was superseded; only the backed-up count is generation-gated.
            let bu = (gen == generation) ? backedUp : currentSummary.backedUp
            setSummary(SyncSummary(backedUp: bu, failed: currentSummary.failed, libraryTotal: libraryTotal))
        case .barrier(let ack):
            ack()
        }
    }

    /// Test-only synchronization: returns once every command enqueued before it has been
    /// processed by the consumer (FIFO). Does not wait for an in-flight sync to *complete*.
    func settle() async {
        await withCheckedContinuation { c in submit(.barrier({ c.resume() })) }
    }

    private func doStart() async {
        let creds = try? await credentials.basicCredentials()
        guard creds != nil else {
            generation &+= 1                            // supersede any in-flight run
            signedIn = false
            syncChild?.cancel(); syncChild = nil
            setStatus(.signedOut)
            setSummary(.empty)                          // drop stale failures
            return
        }
        signedIn = true
        // While a sync is running, leave it (and its generation) intact — bumping would orphan
        // the in-flight child, and scheduling a second same-generation refresh could land stale
        // after the sync's own post-completion refresh (Codex round 7). The running sync refreshes
        // the count when it finishes. Only reconcile here when idle.
        if !isSyncingStatus {
            generation &+= 1
            setStatus(.watching(lastSync: lastSync))
            scheduleCountsRefresh(gen: generation)     // off-consumer; never blocks the queue
        }
    }

    private func doPause() {
        log("cmd pause (status=\(currentStatus))")
        if case .signedOut = currentStatus { return }
        generation &+= 1
        syncChild?.cancel(); syncChild = nil          // coordinator checks cancellation between items
        // Preserve the not-yet-uploaded count so "Paused" shows remaining work, not 0.
        let remaining = lastProgress.map { max(0, $0.total - $0.completed) } ?? 0
        setStatus(.paused(pending: remaining))
    }

    private func doResume() {
        log("cmd resume (status=\(currentStatus))")
        // Only meaningful from `.paused` — where there is no active child to strand. Bumping the
        // generation during an active sync would orphan its in-flight child.
        guard case .paused = currentStatus else { return }
        generation &+= 1
        setStatus(.watching(lastSync: lastSync))
    }

    private func doSyncNow() {
        log("cmd syncNow (signedIn=\(signedIn) syncChild=\(syncChild != nil) status=\(currentStatus))")
        guard signedIn, syncChild == nil else { return }
        if case .paused = currentStatus { return }
        generation &+= 1
        let gen = generation
        lastProgress = nil
        syncBaseBackedUp = currentSummary.backedUp   // baseline for the live backed-up climb
        setStatus(.syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)))
        syncChild = Task { [perform, submit] in
            do {
                let report = try await perform(.all) { progress in submit(.progress(gen: gen, progress)) }
                submit(.finished(gen: gen, report))
            } catch is CancellationError {
                // Superseded (pause/sign-out) already set the terminal status.
            } catch {
                submit(.failed(gen: gen, message: String(describing: error)))
            }
        }
    }

    private func finishSync(_ report: SyncReport) {
        syncChild = nil
        lastProgress = nil                         // so a later idle Pause shows 0, not stale remaining
        if !report.failed.isEmpty { logFailures(report.failed) }
        // Immediate summary from the report (skipped+uploaded == the completed count for an .all
        // sync); a background refresh reconciles it to the live server count.
        setSummary(SyncSummary(backedUp: report.skipped + report.uploaded.count,
                               failed: report.failed, libraryTotal: currentSummary.libraryTotal))
        setStatus(.watching(lastSync: lastSync))
        scheduleCountsRefresh(gen: generation)
    }

    /// Runs `refreshBackedUp` OFF the consumer (network-backed; must not block the command
    /// queue) and reports the result via `.summaryRefreshed`, gated by generation.
    private func scheduleCountsRefresh(gen: UInt64) {
        guard let refresh = refreshCounts else { return }
        Task { [submit] in
            if let counts = try? await refresh() {
                submit(.summaryRefreshed(gen: gen, backedUp: counts.backedUp, libraryTotal: counts.libraryTotal))
            }
        }
    }

    // MARK: - Published snapshot

    private func setStatus(_ s: SyncStatus) {
        log("status → \(s)")
        fanoutLock.lock(); status = s; let cs = Array(statusConts.values); fanoutLock.unlock()
        for c in cs { c.yield(s) }
    }

    private func setSummary(_ s: SyncSummary) {
        log("summary → backedUp=\(s.backedUp) failed=\(s.failed.count)")
        fanoutLock.lock(); summary = s; let cs = Array(summaryConts.values); fanoutLock.unlock()
        for c in cs { c.yield(s) }
    }

    /// DEBUG-only trace of engine transitions (visible in the Xcode console).
    private func log(_ message: @autoclosure () -> String) {
        #if DEBUG
        print("🟣 FN engine: \(message())")
        #endif
    }

    private var currentStatus: SyncStatus { fanoutLock.lock(); defer { fanoutLock.unlock() }; return status }
    private var currentSummary: SyncSummary { fanoutLock.lock(); defer { fanoutLock.unlock() }; return summary }
    private var isSyncingStatus: Bool { if case .syncing = currentStatus { return true }; return false }

    /// Last sync = the coordinator-persisted start time (single source of truth).
    private var lastSync: Date? { state.loadLastSyncStarted() }

    /// Per-item failures don't fail the whole sync (skip-and-continue). Surface them
    /// to the log; the panel renders `SyncReport.failed` via the summary.
    private func logFailures(_ failed: [FailedItem]) {
        for item in failed {
            print("FilesNest sync: failed \(item.key.encoded): \(item.reason)")
        }
    }
}
