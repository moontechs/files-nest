import Foundation

/// In-memory stand-in that drives the UI through every `SyncStatus` without a
/// backend. `start()` reads `credentials` to decide signed-in vs signed-out.
/// `lastSync` is preserved across pause/resume and only advanced by a completed
/// sync, so resuming returns to the same "last synced" label.
public final class StubSyncEngine: SyncEngine, @unchecked Sendable {
    private let credentials: any CredentialStore
    private let autoComplete: Bool
    private let now: @Sendable () -> Date

    private let lock = NSLock()
    private var status: SyncStatus = .signedOut
    private var lastSync: Date?
    private var continuations: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]
    private var summary: SyncSummary = .empty
    private var summaryContinuations: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]

    public init(credentials: any CredentialStore = StaticCredentialStore(nil),
                autoComplete: Bool = true,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.autoComplete = autoComplete
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
            continuation.yield(summary)
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

    private var currentLastSync: Date? {
        lock.lock(); defer { lock.unlock() }
        return lastSync
    }

    /// Synchronous so the `NSLock` never sits in an async context (Swift 6 forbids it).
    private func stampLastSync() -> Date? {
        lock.lock(); defer { lock.unlock() }
        lastSync = now()
        return lastSync
    }

    public func start() async {
        let creds = try? await credentials.basicCredentials()
        set(creds == nil ? .signedOut : .watching(lastSync: currentLastSync))
    }

    public func pause() async {
        guard !isSignedOut else { return }
        set(.paused(pending: 3))
    }

    public func resume() async {
        guard !isSignedOut else { return }
        set(.watching(lastSync: currentLastSync))
    }

    public func syncNow() async {
        guard !isSignedOut else { return }
        let total = 12
        set(.syncing(SyncProgress(completed: 0, total: total,
                                  currentItemName: "IMG_2043.HEIC", bytesRemaining: 210_000_000)))
        guard autoComplete else { return }
        for i in 1...total {
            try? await Task.sleep(nanoseconds: 250_000_000)
            set(.syncing(SyncProgress(completed: i, total: total,
                                      currentItemName: "IMG_\(2043 + i).HEIC",
                                      bytesRemaining: Int64(total - i) * 17_000_000)))
        }
        set(.watching(lastSync: stampLastSync()))
        setSummary(SyncSummary(backedUp: 1_240, failed: []))
    }
}
