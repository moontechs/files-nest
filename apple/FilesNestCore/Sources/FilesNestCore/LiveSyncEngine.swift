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
        case libraryChanged
        case progress(gen: UInt64, SyncProgress)
        case finished(gen: UInt64, SyncReport)
        case failed(gen: UInt64, message: String)
        case counting(gen: UInt64, done: Int, total: Int)   // off-consumer scan progress
        case assessFinished(gen: UInt64, Assessment?)       // nil = scan failed → leave .counting, keep summary
        case barrier(@Sendable () -> Void)   // test-only: resumes once all prior commands are processed
    }

    private let credentials: any CredentialStore
    private let state: any SyncStateStore
    private let perform: Perform
    private let assess: (@Sendable (_ range: SyncRange, _ progress: AssessProgress) async throws -> Assessment)?
    private let cachedAssessment: (@Sendable () -> Assessment?)?
    private let now: @Sendable () -> Date

    // Consumer-only state (mutated exclusively by the single consumer task).
    private var generation: UInt64 = 0
    private var signedIn = false
    private var syncChild: Task<Void, Never>?
    private var assessChild: Task<Void, Never>?   // cancellable launch-count child (mirrors syncChild)
    private var lastProgress: SyncProgress?   // latest progress of the running sync, for pause's pending count
    private var syncBaseBackedUp = 0          // backed-up count at the current sync's start, for a live climb
    private var autoSyncRange: SyncRange?      // range for the sync to chain after the in-flight count (nil = don't chain)
    private var currentSyncRange: SyncRange = .all   // range of the in-flight sync, for finishSync sourcing
    private var pendingLibraryChange = false  // a change arrived mid-run; drain when the run finishes

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
                assess: (@Sendable (_ range: SyncRange, _ progress: AssessProgress) async throws -> Assessment)? = nil,
                cachedAssessment: (@Sendable () -> Assessment?)? = nil,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.state = state
        self.perform = perform
        self.assess = assess
        self.cachedAssessment = cachedAssessment
        self.now = now

        let (stream, continuation) = AsyncStream.makeStream(of: Command.self)
        self.submit = { continuation.yield($0) }
        self.finishCommands = { continuation.finish() }
        Task { [weak self] in
            for await command in stream { await self?.handle(command) }
        }
    }

    deinit { syncChild?.cancel(); assessChild?.cancel(); finishCommands() }   // stop in-flight work when dropped

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
    public func libraryDidChange() async { submit(.libraryChanged) }

    // MARK: - Consumer (single task; no interleaving)

    private func handle(_ command: Command) async {
        switch command {
        case .start:   await doStart()
        case .pause:   doPause()
        case .resume:  doResume()
        case .syncNow: doSyncNow(range: .all)      // manual Sync Now is always a full sync
        case .progress(let gen, let p):
            if gen == generation {
                lastProgress = p
                // Live climb: each completed upload is one more file on the server. Reconciled
                // to the true server count by the post-completion refresh.
                setSummary(SyncSummary(backedUp: syncBaseBackedUp + p.completed,
                                       pending: currentSummary.pending, failed: currentSummary.failed))
                setStatus(.syncing(p))
            }
        case .finished(let gen, let report):
            if gen == generation { finishSync(report) }
        case .failed(let gen, let message):
            if gen == generation { syncChild = nil; lastProgress = nil; setStatus(.error(message: message)) }
        case .counting(let gen, let done, let total):
            if gen == generation { setStatus(.counting(done: done, total: total)) }
        case .assessFinished(let gen, let a):
            if gen == generation {
                assessChild = nil
                if let a { setSummary(SyncSummary(backedUp: a.backedUp, pending: a.pending, failed: currentSummary.failed)) }
                setStatus(.watching(lastSync: lastSync))
                let range = autoSyncRange
                autoSyncRange = nil
                if let range, (a?.pending ?? 0) > 0 { doSyncNow(range: range) }
                else { drainPendingChangeIfAny() }
            }
        case .libraryChanged:
            guard signedIn else { break }                 // ignore while signed out / before start
            switch currentStatus {
            case .watching, .error:
                startIdleCount(range: incrementalRange(), autoSync: true)   // idle → incremental count, then sync if pending
            case .syncing, .counting:
                pendingLibraryChange = true               // coalesce; drained when the run finishes
            case .paused:
                pendingLibraryChange = true               // honored on resume (never upload while paused)
            case .signedOut:
                break
            }
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
            assessChild?.cancel(); assessChild = nil
            lastProgress = nil                          // so a later idle Pause shows 0, not stale remaining
            pendingLibraryChange = false                // sign-out drops any coalesced change
            autoSyncRange = nil
            setStatus(.signedOut)
            setSummary(.empty)                          // drop stale failures
            return
        }
        signedIn = true
        // While a sync or count is running, leave it (and its generation) intact — bumping would
        // orphan the in-flight child. Only reconcile here when idle; the count refreshes the
        // exact backlog off the consumer.
        if !isSyncingStatus && !isCountingStatus {
            if let cached = cachedAssessment?() {      // warm launch: show last-known instantly
                setSummary(SyncSummary(backedUp: cached.backedUp, pending: cached.pending, failed: currentSummary.failed))
            }
            if isPausedStatus {
                // Restart from paused reconciles to watching (updates Pending) but must not
                // auto-upload — the user explicitly paused. Drop any change coalesced during the
                // pause too, so the reconcile count's else-drain can't turn it into an upload;
                // that count already reflects it in Pending, and watching stays live for the next one.
                pendingLibraryChange = false
                startIdleCount(range: .all, autoSync: false)
            } else {
                startIdleCount(range: .all, autoSync: true)   // launch/restart catch-up (option A) — always full
            }
        }
    }

    private func doPause() {
        log("cmd pause (status=\(currentStatus))")
        if case .signedOut = currentStatus { return }
        if case .paused = currentStatus { return }    // already paused; don't recompute/zero the remaining
        generation &+= 1
        syncChild?.cancel(); syncChild = nil          // coordinator checks cancellation between items
        assessChild?.cancel(); assessChild = nil      // pausing during a count cancels it
        // Preserve the not-yet-uploaded count so "Paused" shows remaining work, not 0.
        let remaining = lastProgress.map { max(0, $0.total - $0.completed) } ?? 0
        setStatus(.paused(pending: remaining))
        lastProgress = nil                            // cleared on this non-syncing transition (invariant)
    }

    private func doResume() {
        log("cmd resume (status=\(currentStatus))")
        // Only meaningful from `.paused` — where there is no active child to strand. Bumping the
        // generation during an active sync would orphan its in-flight child.
        guard case .paused = currentStatus else { return }
        generation &+= 1
        assessChild?.cancel(); assessChild = nil      // defensive; no count is in flight while paused
        lastProgress = nil                            // resumed work is a fresh run; no stale remaining
        setStatus(.watching(lastSync: lastSync))
        drainPendingChangeIfAny()                     // honor a change that arrived while paused
    }

    private func doSyncNow(range: SyncRange) {
        log("cmd syncNow (signedIn=\(signedIn) syncChild=\(syncChild != nil) status=\(currentStatus) range=\(range))")
        guard signedIn, syncChild == nil else { return }
        if case .paused = currentStatus { return }
        generation &+= 1
        assessChild?.cancel(); assessChild = nil      // syncNow supersedes an in-flight count
        let gen = generation
        lastProgress = nil
        currentSyncRange = range                     // for finishSync's backedUp sourcing
        syncBaseBackedUp = currentSummary.backedUp   // baseline for the live backed-up climb
        setStatus(.syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)))
        syncChild = Task { [perform, submit] in
            do {
                let report = try await perform(range) { progress in submit(.progress(gen: gen, progress)) }
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
        // Summary straight from the report: after an `.all` sync everything uploaded except
        // failures. Pending counts only UPLOAD failures (a failed delete is server cruft, not a
        // resource awaiting upload) so it stays consistent with assess's plan.uploads.count. No
        // re-scan (a launch count already gave the exact numbers; warm launch recounts).
        let pendingUploads = report.failed.filter { $0.kind == .upload }.count
        let backedUp: Int
        switch currentSyncRange {
        case .all:
            backedUp = report.skipped + report.uploaded.count           // full scan saw everything
        case .modifiedSince:
            backedUp = syncBaseBackedUp + report.uploaded.count          // windowed: baseline (from the count) + new uploads
        }
        setSummary(SyncSummary(backedUp: backedUp, pending: pendingUploads, failed: report.failed))
        setStatus(.watching(lastSync: lastSync))
        drainPendingChangeIfAny()                   // pick up a change coalesced during this sync
    }

    /// Runs `assess` OFF the consumer (PhotoKit scan + server diff; must not block the command
    /// queue). Reports scan progress via `.counting` and the result via `.assessFinished`, both
    /// generation-gated. `nil` result = the scan failed → the handler settles to `.watching`.
    /// Bump the generation and start an off-consumer count. If `autoSync` and the count finds
    /// pending work, `.assessFinished` chains into a sync (count-then-upload).
    private func startIdleCount(range: SyncRange, autoSync: Bool) {
        guard signedIn else { return }
        generation &+= 1
        lastProgress = nil
        autoSyncRange = autoSync ? range : nil
        beginCounting(gen: generation, range: range)
    }

    private static let incrementalMargin: TimeInterval = 60   // clock-skew safety; re-scanning slightly wider is a no-op

    /// The window for a change-triggered cycle: everything modified since the last sync started
    /// (minus a small margin). `.all` when there is no prior sync yet.
    private func incrementalRange() -> SyncRange {
        guard let last = state.loadLastSyncStarted() else { return .all }
        return .modifiedSince(last.addingTimeInterval(-Self.incrementalMargin))
    }

    /// If a library change was coalesced while a run was in flight, start a fresh count now
    /// (which chains into a sync if anything is pending). Consumer-only.
    private func drainPendingChangeIfAny() {
        guard pendingLibraryChange else { return }
        pendingLibraryChange = false
        startIdleCount(range: incrementalRange(), autoSync: true)
    }

    private func beginCounting(gen: UInt64, range: SyncRange) {
        guard let assess else { setStatus(.watching(lastSync: lastSync)); return }
        setStatus(.counting(done: 0, total: 0))
        assessChild = Task { [assess, submit] in
            do {
                let progress = AssessProgress { done, total in submit(.counting(gen: gen, done: done, total: total)) }
                let a = try await assess(range, progress)
                submit(.assessFinished(gen: gen, a))
            } catch is CancellationError {
                // Superseded (pause/syncNow/sign-out) already set the terminal status.
            } catch {
                submit(.assessFinished(gen: gen, nil))
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
    private var isCountingStatus: Bool { if case .counting = currentStatus { return true }; return false }
    private var isPausedStatus: Bool { if case .paused = currentStatus { return true }; return false }

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
