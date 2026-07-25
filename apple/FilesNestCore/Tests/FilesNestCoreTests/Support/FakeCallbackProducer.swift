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
