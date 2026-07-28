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
        case barrier(@Sendable () -> Void)   // test-only: resumes once all prior commands are processed
    }

    private let credentials: any CredentialStore
    private let state: any SyncStateStore
    private let perform: Perform
    private let refreshBackedUp: (@Sendable () async throws -> Int)?
    private let now: @Sendable () -> Date

    // Consumer-only state (mutated exclusively by the single consumer task).
    private var generation: UInt64 = 0
    private var signedIn = false
    private var syncChild: Task<Void, Never>?

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
                refreshBackedUp: (@Sendable () async throws -> Int)? = nil,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.state = state
        self.perform = perform
        self.refreshBackedUp = refreshBackedUp
        self.now = now

        let (stream, continuation) = AsyncStream.makeStream(of: Command.self)
        self.submit = { continuation.yield($0) }
        self.finishCommands = { continuation.finish() }
        Task { [weak self] in
            for await command in stream { await self?.handle(command) }
        }
    }

    deinit { finishCommands() }

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
            if gen == generation { setStatus(.syncing(p)) }
        case .finished(let gen, let report):
            if gen == generation { await finishSync(report) }
        case .failed(let gen, let message):
            if gen == generation { syncChild = nil; setStatus(.error(message: message)) }
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
        generation &+= 1
        guard creds != nil else {
            signedIn = false
            syncChild?.cancel(); syncChild = nil
            setStatus(.signedOut)
            setSummary(.empty)                          // drop stale failures
            return
        }
        signedIn = true
        if !isSyncingStatus { setStatus(.watching(lastSync: lastSync)) }   // don't clobber a running sync
        if let refresh = refreshBackedUp, let count = try? await refresh() {
            setSummary(SyncSummary(backedUp: count, failed: currentSummary.failed))
        }
    }

    private func doPause() {
        if case .signedOut = currentStatus { return }
        generation &+= 1
        syncChild?.cancel(); syncChild = nil          // coordinator checks cancellation between items
        setStatus(.paused(pending: 0))
    }

    private func doResume() {
        if case .signedOut = currentStatus { return }
        generation &+= 1
        setStatus(.watching(lastSync: lastSync))
    }

    private func doSyncNow() {
        guard signedIn, syncChild == nil else { return }
        if case .paused = currentStatus { return }
        generation &+= 1
        let gen = generation
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

    private func finishSync(_ report: SyncReport) async {
        syncChild = nil
        if !report.failed.isEmpty { logFailures(report.failed) }
        // Backed up = live server count; fall back to the report if no refresh or it fails.
        let backedUp: Int
        if let refresh = refreshBackedUp, let count = try? await refresh() {
            backedUp = count
        } else {
            backedUp = report.skipped + report.uploaded.count
        }
        setSummary(SyncSummary(backedUp: backedUp, failed: report.failed))
        setStatus(.watching(lastSync: lastSync))
    }

    // MARK: - Published snapshot

    private func setStatus(_ s: SyncStatus) {
        fanoutLock.lock(); status = s; let cs = Array(statusConts.values); fanoutLock.unlock()
        for c in cs { c.yield(s) }
    }

    private func setSummary(_ s: SyncSummary) {
        fanoutLock.lock(); summary = s; let cs = Array(summaryConts.values); fanoutLock.unlock()
        for c in cs { c.yield(s) }
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
