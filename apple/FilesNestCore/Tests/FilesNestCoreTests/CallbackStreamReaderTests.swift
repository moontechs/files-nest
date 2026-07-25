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
}
