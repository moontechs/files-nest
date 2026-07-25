import Foundation
@testable import FilesNestCore

/// Feeds a fixed sequence of blobs to a CallbackStreamReader from a background
/// serial queue, mimicking PhotoKit's arbitrary-serial-queue delivery. Honours
/// backpressure: it stops when `onData` returns false.
final class FakeCallbackProducer: @unchecked Sendable {
    private let blobs: [Data]
    private let queue = DispatchQueue(label: "fake.producer")
    private let cancelledLock = NSLock()
    private var _cancelled = false
    var cancelled: Bool { cancelledLock.lock(); defer { cancelledLock.unlock() }; return _cancelled }

    init(blobs: [Data]) { self.blobs = blobs }

    /// Builds a reader whose `start` streams `blobs` on the background queue.
    func makeReader(diagnostics: StreamDiagnostics? = nil) -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.queue.async {
                    for blob in self.blobs {
                        if !onData(blob) { onDone(nil); return }
                    }
                    onDone(nil)
                }
                return 1   // token
            },
            cancel: { _ in
                self.cancelledLock.lock(); self._cancelled = true; self.cancelledLock.unlock()
            },
            diagnostics: diagnostics)
    }
}

/// Collects blobs a reader delivers, so tests can assert order and content.
final class BlobCollector: @unchecked Sendable {
    private let lock = NSLock()
    private var _data = Data()
    var joined: Data { lock.lock(); defer { lock.unlock() }; return _data }
    @Sendable func sink(_ blob: Data) async throws { append(blob) }
    private func append(_ blob: Data) { lock.lock(); _data.append(blob); lock.unlock() }
}

/// Measures how far the producer runs AHEAD of the sink: `delivered - sinkDone`
/// sampled each time the producer pushes a blob. Under capacity-1 with no
/// look-ahead this must never exceed 1. (A single serial producer loop's own
/// reentrancy is always 1 and proves nothing — this measures the real property.)
final class BackpressureTracker: @unchecked Sendable {
    private let lock = NSLock()
    private var _delivered = 0
    private var _sinkDone = 0
    private var _maxAhead = 0
    var maxAhead: Int { lock.lock(); defer { lock.unlock() }; return _maxAhead }
    func producerDelivered() {
        lock.lock(); _delivered += 1; _maxAhead = max(_maxAhead, _delivered - _sinkDone); lock.unlock()
    }
    func sinkCompleted() { lock.lock(); _sinkDone += 1; lock.unlock() }
}

/// Delivers `blobCount` one-byte blobs, recording via `tracker` how far it runs
/// ahead of the sink.
final class InstrumentedProducer: @unchecked Sendable {
    private let blobCount: Int
    private let tracker: BackpressureTracker
    private let queue = DispatchQueue(label: "instrumented.producer")
    init(blobCount: Int, tracker: BackpressureTracker) { self.blobCount = blobCount; self.tracker = tracker }
    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.queue.async {
                    for i in 0..<self.blobCount {
                        self.tracker.producerDelivered()   // about to push blob i
                        if !onData(Data([UInt8(i & 0xff)])) { onDone(nil); return }
                    }
                    onDone(nil)
                }
                return 1
            },
            cancel: { _ in })
    }
}

/// A producer whose delivery you can pause and whose token arrival you can delay,
/// so tests can force the exact interleavings spec §4.1 enumerates.
final class ControllableProducer: @unchecked Sendable {
    let started = DispatchSemaphore(value: 0)      // signalled when start() runs
    let releaseToken = DispatchSemaphore(value: 0) // gate token return
    private let queue = DispatchQueue(label: "controllable.producer")
    private let cancelledLock = NSLock()
    private var _cancelCount = 0
    var cancelCount: Int { cancelledLock.lock(); defer { cancelledLock.unlock() }; return _cancelCount }

    let blobs: [Data]
    let delayTokenReturn: Bool
    let completes: Bool
    init(blobs: [Data], delayTokenReturn: Bool = false, completes: Bool = true) {
        self.blobs = blobs; self.delayTokenReturn = delayTokenReturn; self.completes = completes
    }

    /// Async bridge to the blocking `started` wait (DispatchSemaphore.wait is
    /// banned in async contexts under Swift 6).
    func waitStarted() async {
        await withCheckedContinuation { c in
            DispatchQueue.global().async { self.started.wait(); c.resume() }
        }
    }

    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.started.signal()
                self.queue.async {
                    for blob in self.blobs {
                        if !onData(blob) { onDone(nil); return }
                    }
                    if self.completes { onDone(nil) }
                }
                if self.delayTokenReturn { self.releaseToken.wait() }
                return 1
            },
            cancel: { _ in
                self.cancelledLock.lock(); self._cancelCount += 1; self.cancelledLock.unlock()
            })
    }
}

/// Completes only when told to, after start().
final class LateCompletingProducer: @unchecked Sendable {
    let started = DispatchSemaphore(value: 0)
    private let doneLock = NSLock()
    private var onDone: (@Sendable (Error?) -> Void)?

    func waitStarted() async {
        await withCheckedContinuation { c in
            DispatchQueue.global().async { self.started.wait(); c.resume() }
        }
    }
    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { _, onDone in
                self.doneLock.lock(); self.onDone = onDone; self.doneLock.unlock()
                self.started.signal()
                return 1
            }, cancel: { _ in })
    }
    func completeNow() {
        doneLock.lock(); let d = onDone; doneLock.unlock(); d?(nil)
    }
}

/// Delivers one blob, then fires completion while that blob's sink is running.
/// `deliverReturned` fires only when the blocked `deliver` call actually returns
/// — i.e. the producer thread was released, not stranded. A test that waits on
/// it can detect the handoff-under-signal deadlock, which read()'s own
/// completion cannot (read returns via the terminal path regardless).
final class CompleteDuringSinkProducer: @unchecked Sendable {
    private let queue = DispatchQueue(label: "complete.during.sink")
    private let doneLock = NSLock()
    private var onDone: (@Sendable (Error?) -> Void)?
    let deliverReturned = DispatchSemaphore(value: 0)
    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.doneLock.lock(); self.onDone = onDone; self.doneLock.unlock()
                self.queue.async {
                    _ = onData(Data([7]))
                    self.deliverReturned.signal()   // only reached if deliver unblocked
                }
                return 1
            }, cancel: { _ in })
    }
    func fireCompletionDuringSink() {
        doneLock.lock(); let d = onDone; doneLock.unlock()
        DispatchQueue.global().async { d?(nil) }
    }
    /// Async bridge: true if the producer's deliver returned within `timeoutMs`.
    func awaitDeliverReturned(timeoutMs: Int) async -> Bool {
        await withCheckedContinuation { c in
            DispatchQueue.global().async {
                let r = self.deliverReturned.wait(timeout: .now() + .milliseconds(timeoutMs))
                c.resume(returning: r == .success)
            }
        }
    }
}