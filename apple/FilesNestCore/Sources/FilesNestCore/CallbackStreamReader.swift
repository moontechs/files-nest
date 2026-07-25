import Foundation

/// Bridges a synchronous, callback-delivering byte producer (e.g. PhotoKit's
/// `PHAssetResourceManager.requestData`) to an async sink, honouring the four
/// `AssetDataSource` clauses. `Token` is generic so this file never names a
/// PhotoKit type. See docs/design/20260724-photosassetdatasource.md §4.
public struct CallbackStreamReader<Token: Sendable>: Sendable {

    /// Returned by the consumer's `next()`.
    fileprivate enum Item {
        case blob(Data, generation: Int)
        case done(Error?)
    }

    /// One blob only is ever in flight (capacity-1), enforced by `drained`.
    fileprivate final class Coordinator: @unchecked Sendable {
        enum State {
            case idle
            case blobPending(Data, generation: Int)
            case consumerWaiting(CheckedContinuation<Item, Never>)
            case handoff(generation: Int)   // continuation taken, resume in flight
            case terminal(Error?)
        }

        let lock = NSLock()
        var state: State = .idle
        var nextGeneration = 0
        let drained = DispatchSemaphore(value: 0)
        let diagnostics: StreamDiagnostics?
        // Task 5 adds cancellation via these; declared here so the type is
        // stable across tasks.
        var token: Token?
        var cancelOnTokenArrival = false

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

        /// Producer completion. Task 5 hardens this; the delivery path needs
        /// only the terminal transition and waking a suspended consumer.
        func finish(_ error: Error?) {
            lock.lock()
            if case .terminal = state { lock.unlock(); return }
            let prev = state
            state = .terminal(error)
            switch prev {
            case .consumerWaiting(let k):
                lock.unlock()
                k.resume(returning: .done(error))
            default:
                // .handoff: resume already in flight; consumer owns the signal.
                // .idle / .blobPending: consumer observes terminal at next next().
                lock.unlock()
            }
        }

        /// Consumer side. Suspends until a blob or terminal is available.
        /// The lock lives in the synchronous `registerOrTake` because NSLock
        /// may not be held across an `await` under Swift 6.
        func next() async -> Item {
            await withCheckedContinuation { (k: CheckedContinuation<Item, Never>) in
                if let item = registerOrTake(k) { k.resume(returning: item) }
            }
        }

        /// Under the lock: return an immediately-available item (caller resumes
        /// the continuation), or stash the continuation and return nil.
        private func registerOrTake(_ k: CheckedContinuation<Item, Never>) -> Item? {
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
        /// even if the state went terminal while the sink ran (spec §4.1).
        func consumerDidProcess(generation: Int) {
            lock.lock()
            if case .handoff(let g) = state, g == generation { state = .idle }
            lock.unlock()
            drained.signal()
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
        let coord = Coordinator(diagnostics: diagnostics)
        var skip = OffsetSkip(skipping: offset)
        let start = self.start

        // Per-CALL serial queue: a re-entrant read on the same reader gets its
        // own queue, so it cannot deadlock behind this call's blocked producer.
        let queue = DispatchQueue(label: "CallbackStreamReader.\(UUID().uuidString)")
        queue.async {
            _ = coord.reentrantStart(start)   // Task 5 stores/cancels the token.
        }

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
                    coord.consumerDidProcess(generation: gen)  // release producer first
                    coord.finish(error)                        // then mark terminal
                    throw error
                }
                coord.consumerDidProcess(generation: gen)
            }
        }
    }
}
