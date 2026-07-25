import Foundation

/// Returned by the coordinator's `next()`. Internal (not public) so tests can
/// drive the coordinator directly, but it is not part of the package's API.
enum CallbackStreamItem {
    case blob(Data, generation: Int)
    case done(Error?)
}

/// The capacity-1 state machine bridging a synchronous producer callback to an
/// async consumer. Extracted from `CallbackStreamReader` so its concurrency —
/// the part three review rounds each found a defect in — is unit-testable
/// deterministically, not only through the async reader. `@unchecked Sendable`:
/// all mutable state is guarded by `lock`. See design §4.
final class CallbackStreamCoordinator<Token: Sendable>: @unchecked Sendable {
    enum State {
        case idle
        case blobPending(Data, generation: Int)
        case consumerWaiting(CheckedContinuation<CallbackStreamItem, Never>)
        case handoff(generation: Int)   // continuation taken, resume in flight
        case terminal(Error?)
    }

    private let lock = NSLock()
    private var state: State = .idle
    private var nextGeneration = 0
    private let drained = DispatchSemaphore(value: 0)
    private let diagnostics: StreamDiagnostics?
    private var token: Token?
    private var cancelOnTokenArrival = false

    init(diagnostics: StreamDiagnostics?) { self.diagnostics = diagnostics }

    /// Producer side. Returns false to tell the producer to stop.
    func deliver(_ blob: Data) -> Bool {
        diagnostics?.enter(byteCount: blob.count)
        defer { diagnostics?.exit() }

        lock.lock()
        if case .terminal = state { lock.unlock(); return false }
        let gen = nextGeneration; nextGeneration += 1
        switch state {
        case .consumerWaiting(let k):
            state = .handoff(generation: gen)
            lock.unlock()
            k.resume(returning: .blob(blob, generation: gen))
        case .idle:
            state = .blobPending(blob, generation: gen)
            lock.unlock()
        default:
            // Unreachable under capacity-1: the producer is blocked in
            // `drained.wait()` until the prior blob is consumed.
            lock.unlock()
            return false
        }
        drained.wait()                 // BACKPRESSURE
        lock.lock()
        let stopping: Bool
        if case .terminal = state { stopping = true } else { stopping = false }
        lock.unlock()
        return !stopping
    }

    /// Producer completion (via `onDone`).
    func finish(_ error: Error?) {
        lock.lock()
        if case .terminal = state { lock.unlock(); return }
        let prev = state
        state = .terminal(error)
        switch prev {
        case .consumerWaiting(let k):
            lock.unlock()
            k.resume(returning: .done(error))
        case .blobPending:
            // A blob was delivered but not yet consumed, so a producer is blocked
            // in `deliver`'s `drained.wait()`. Terminal drops the blob; the
            // producer must still be released or it strands forever.
            lock.unlock()
            drained.signal()
        default:
            // .handoff: resume already in flight; consumer owns the signal.
            // .idle: nothing is blocked.
            lock.unlock()
        }
    }

    /// Consumer side. Suspends until a blob or terminal is available. The lock
    /// lives in the synchronous `registerOrTake` because NSLock may not be held
    /// across an `await` under Swift 6.
    func next() async -> CallbackStreamItem {
        await withCheckedContinuation { (k: CheckedContinuation<CallbackStreamItem, Never>) in
            if let item = registerOrTake(k) { k.resume(returning: item) }
        }
    }

    /// Under the lock: return an immediately-available item (caller resumes the
    /// continuation), or stash the continuation and return nil.
    private func registerOrTake(_ k: CheckedContinuation<CallbackStreamItem, Never>) -> CallbackStreamItem? {
        lock.lock(); defer { lock.unlock() }
        switch state {
        case .blobPending(let blob, let gen):
            state = .idle
            return .blob(blob, generation: gen)
        case .terminal(let err):
            return .done(err)
        case .idle:
            state = .consumerWaiting(k)
            return nil
        default:
            return .done(nil)   // unreachable
        }
    }

    /// Called by the consumer after it has fully processed the blob for
    /// `generation`. OWNERSHIP FOLLOWS THE BLOB: this signals unconditionally,
    /// even if the state went terminal while the sink ran (design §4.1).
    func consumerDidProcess(generation: Int) {
        lock.lock()
        if case .handoff(let g) = state, g == generation { state = .idle }
        lock.unlock()
        drained.signal()
    }

    /// Terminal transition from cancellation. Under the lock: mark terminal,
    /// extract any suspended continuation (never a `.handoff` one — its resume is
    /// already in flight), release a producer blocked on a pending blob, and
    /// decide whether the token is available now or must be cancelled on arrival
    /// (§4.1.1). Caller resumes and cancels OUTSIDE the lock (§4.1).
    func beginCancellation() -> (CheckedContinuation<CallbackStreamItem, Never>?, Token?) {
        lock.lock()
        if case .terminal = state { lock.unlock(); return (nil, nil) }
        let prev = state
        state = .terminal(CancellationError())
        var k: CheckedContinuation<CallbackStreamItem, Never>?
        if case .consumerWaiting(let cont) = prev { k = cont }
        var releaseProducer = false
        if case .blobPending = prev { releaseProducer = true }
        let tok = token
        if tok == nil { cancelOnTokenArrival = true }
        lock.unlock()
        if releaseProducer { drained.signal() }
        return (k, tok)
    }

    /// Terminal transition from a sink failure. The consumer already holds the
    /// failing blob, so no producer is blocked on it yet — but the producer is
    /// still running and must be cancelled. Set terminal BEFORE the consumer
    /// releases the producer, so the next `deliver` sees terminal and stops
    /// instead of depositing a blob nobody will consume. Returns the token to
    /// cancel (or arranges cancellation on arrival).
    func beginSinkFailure(_ error: Error) -> Token? {
        lock.lock()
        if case .terminal = state { lock.unlock(); return nil }
        state = .terminal(error)
        let tok = token
        if tok == nil { cancelOnTokenArrival = true }
        lock.unlock()
        return tok
    }

    /// Store the token once `start` returns it. If cancellation already fired
    /// while `start` was running, return it so the caller cancels immediately
    /// (outside the lock). Lock-serialised with `beginCancellation` so the token
    /// is cancelled exactly once.
    func storeToken(_ t: Token) -> Token? {
        lock.lock(); defer { lock.unlock() }
        token = t
        return cancelOnTokenArrival ? t : nil
    }

    /// Invokes `start`, wiring `onData`→`deliver` and `onDone`→`finish`.
    func reentrantStart(
        _ start: @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                            _ onDone: @escaping @Sendable (Error?) -> Void) -> Token
    ) -> Token {
        start({ [weak self] blob in self?.deliver(blob) ?? false },
              { [weak self] error in self?.finish(error) })
    }
}

/// Bridges a synchronous, callback-delivering byte producer (e.g. PhotoKit's
/// `PHAssetResourceManager.requestData`) to an async sink, honouring the four
/// `AssetDataSource` clauses. `Token` is generic so this file never names a
/// PhotoKit type. See docs/design/20260724-photosassetdatasource.md §4.
public struct CallbackStreamReader<Token: Sendable>: Sendable {

    private let start: @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                                  _ onDone: @escaping @Sendable (Error?) -> Void) -> Token
    private let cancel: @Sendable (Token) -> Void
    private let diagnostics: StreamDiagnostics?

    public init(
        start: @escaping @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                                    _ onDone: @escaping @Sendable (Error?) -> Void) -> Token,
        cancel: @escaping @Sendable (Token) -> Void,
        diagnostics: StreamDiagnostics? = nil
    ) {
        self.start = start
        self.cancel = cancel
        self.diagnostics = diagnostics
    }

    public func read(from offset: Int64,
                     into sink: @Sendable (Data) async throws -> Void) async throws {
        let coord = CallbackStreamCoordinator<Token>(diagnostics: diagnostics)
        var skip = OffsetSkip(skipping: offset)
        let start = self.start
        let cancel = self.cancel

        // Per-CALL serial queue: a re-entrant read on the same reader gets its
        // own queue, so it cannot deadlock behind this call's blocked producer.
        let queue = DispatchQueue(label: "CallbackStreamReader.\(UUID().uuidString)")
        queue.async {
            let token = coord.reentrantStart(start)
            // Decide-under-lock, call-out-outside-lock (§4.1.1).
            if let toCancel = coord.storeToken(token) { cancel(toCancel) }
        }

        try await withTaskCancellationHandler {
            // Consumer loop. Each blob handed to us is ours to signal, always.
            while true {
                let item = await coord.next()
                switch item {
                case .done(let err):
                    if let err { throw err }
                    return
                case .blob(let blob, let gen):
                    do {
                        if let b = skip.take(blob) { try await sink(b) }
                    } catch {
                        // Terminal FIRST (so a racing next deliver stops), then
                        // release this blob's producer, then cancel the request.
                        let tok = coord.beginSinkFailure(error)
                        coord.consumerDidProcess(generation: gen)
                        if let tok { cancel(tok) }
                        throw error
                    }
                    coord.consumerDidProcess(generation: gen)
                }
            }
        } onCancel: {
            let (k, tok) = coord.beginCancellation()
            k?.resume(returning: .done(CancellationError()))   // outside lock
            if let tok { cancel(tok) }                          // outside lock
        }
    }
}
