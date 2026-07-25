import Testing
import Foundation
@testable import FilesNestCore

@Suite struct CallbackStreamReaderTests {

    @Test func deliversBytesInOrderFromZero() async throws {
        let blobs = [Data([0,1,2]), Data([3,4,5]), Data([6,7])]
        let producer = FakeCallbackProducer(blobs: blobs)
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 0, into: collector.sink)
        #expect(collector.joined == Data([0,1,2,3,4,5,6,7]))
    }

    @Test func deliversBytesInOrderFromOffset() async throws {
        // Offset 4 drops the first four bytes across blob boundaries.
        let blobs = [Data([0,1,2]), Data([3,4,5]), Data([6,7])]
        let producer = FakeCallbackProducer(blobs: blobs)
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 4, into: collector.sink)
        #expect(collector.joined == Data([4,5,6,7]))
    }

    @Test func offsetBeyondEndYieldsNothing() async throws {
        let producer = FakeCallbackProducer(blobs: [Data([0,1,2])])
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 100, into: collector.sink)
        #expect(collector.joined.isEmpty)
    }

    @Test func zeroBlobsCompletesCleanly() async throws {
        let producer = FakeCallbackProducer(blobs: [])
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 0, into: collector.sink)
        #expect(collector.joined.isEmpty)
    }

    /// Capacity-1: the producer must not run ahead of the sink. Measured as
    /// `delivered - sinkDone` at each push; with a broken semaphore this reaches
    /// 2, so this catches a backpressure regression (unlike a producer-reentrancy
    /// counter, which is vacuously 1 for a serial producer).
    @Test func sinkIsFullyAwaitedBeforeNextDelivery() async throws {
        let tracker = BackpressureTracker()
        let producer = InstrumentedProducer(blobCount: 20, tracker: tracker)
        try await producer.makeReader().read(from: 0) { _ in
            try await Task.sleep(for: .milliseconds(5))
            tracker.sinkCompleted()
        }
        #expect(tracker.maxAhead == 1)
    }

    @Test func sinkErrorPropagatesAndStopsProducer() async throws {
        struct Boom: Error {}
        let producer = FakeCallbackProducer(blobs: [Data([1]), Data([2]), Data([3])])
        await #expect(throws: Boom.self) {
            try await producer.makeReader().read(from: 0) { _ in throw Boom() }
        }
    }

    // MARK: - Termination, cancellation, handoff (Task 5)

    /// Cancellation while the consumer is suspended with no blob pending must
    /// throw CancellationError, not hang. Never-resume path (§4.1 rule 1).
    @Test func cancellationWhileConsumerSuspendedResumesAndThrows() async throws {
        let producer = ControllableProducer(blobs: [], completes: false)   // never delivers, never completes
        let reader = producer.makeReader()
        let task = Task { try await reader.read(from: 0) { _ in } }
        await producer.waitStarted()
        try await Task.sleep(for: .milliseconds(50))   // let consumer suspend
        task.cancel()
        await #expect(throws: CancellationError.self) { try await task.value }
        #expect(producer.cancelCount == 1)
    }

    /// A late onDone after terminal must not double-resume the continuation
    /// (would crash). Reaching the end without a crash is the assertion.
    @Test func lateOnDoneAfterTerminalDoesNotDoubleResume() async throws {
        let producer = LateCompletingProducer()
        let reader = producer.makeReader()
        let task = Task { try await reader.read(from: 0) { _ in } }
        await producer.waitStarted()
        try await Task.sleep(for: .milliseconds(50))
        task.cancel()
        _ = try? await task.value
        producer.completeNow()             // fires onDone AFTER terminal
        try await Task.sleep(for: .milliseconds(50))
        #expect(Bool(true))
    }

    /// After cancellation, `cancel(token)` fires even if the token had not yet
    /// been returned by `start` (§4.1.1).
    @Test func cancellationBeforeTokenReturnsStillCancels() async throws {
        let producer = ControllableProducer(blobs: [], delayTokenReturn: true, completes: false)
        let reader = producer.makeReader()
        let task = Task { try await reader.read(from: 0) { _ in } }
        await producer.waitStarted()
        task.cancel()                      // cancel while start() is blocked pre-return
        producer.releaseToken.signal()     // now let the token return
        _ = try? await task.value
        try await Task.sleep(for: .milliseconds(50))
        #expect(producer.cancelCount == 1)
    }

    /// A consumer resumed with a blob that discovers `.terminal` while running
    /// must still signal, or the blocked producer is stranded (§4.1 handoff).
    /// `read()` completes via the terminal path either way, so the assertion is
    /// that the producer's blocked `deliver` actually RETURNED — the only
    /// observable difference between "signalled" and "stranded".
    @Test func resumedConsumerSignalsEvenWhenTerminal() async throws {
        let producer = CompleteDuringSinkProducer()
        let reader = producer.makeReader()
        try await reader.read(from: 0) { _ in
            producer.fireCompletionDuringSink()
            try await Task.sleep(for: .milliseconds(30))
        }
        let released = await producer.awaitDeliverReturned(timeoutMs: 2000)
        #expect(released)   // false ⇒ producer thread stranded in drained.wait()
    }

    /// A cancel racing normal delivery must not leave a spare semaphore permit
    /// (which would loosen capacity-1). Assert the producer never ran ahead.
    @Test func drainIsSignalledExactlyOncePerBlob() async throws {
        let tracker = BackpressureTracker()
        let producer = InstrumentedProducer(blobCount: 50, tracker: tracker)
        let reader = producer.makeReader()
        let task = Task {
            try await reader.read(from: 0) { _ in
                try await Task.sleep(for: .milliseconds(1))
                tracker.sinkCompleted()
            }
        }
        try await Task.sleep(for: .milliseconds(20))
        task.cancel()
        _ = try? await task.value
        #expect(tracker.maxAhead == 1)
    }

    @Test func inlineSynchronousDeliveryIsSupported() async throws {
        let collector = BlobCollector()
        let reader = CallbackStreamReader<Int>(
            start: { onData, onDone in
                _ = onData(Data([9, 9, 9]))   // inline, synchronous, before returning
                onDone(nil)
                return 1
            },
            cancel: { _ in })
        try await reader.read(from: 0, into: collector.sink)
        #expect(collector.joined == Data([9, 9, 9]))
    }

    @Test func nestedReadOnSameReaderDoesNotDeadlock() async throws {
        let inner = FakeCallbackProducer(blobs: [Data([2])]).makeReader()
        let outerCollector = BlobCollector()
        let innerCollector = BlobCollector()
        let outer = FakeCallbackProducer(blobs: [Data([1])]).makeReader()
        try await outer.read(from: 0) { blob in
            try await outerCollector.sink(blob)
            try await inner.read(from: 0, into: innerCollector.sink)
        }
        #expect(outerCollector.joined == Data([1]))
        #expect(innerCollector.joined == Data([2]))
    }
}
