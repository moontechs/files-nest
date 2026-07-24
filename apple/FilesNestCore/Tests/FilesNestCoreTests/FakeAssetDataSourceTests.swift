import Testing
import Foundation
@testable import FilesNestCore

@Suite struct FakeAssetDataSourceTests {

    private actor Collector {
        private(set) var totalBytes = 0
        private(set) var blobCount = 0
        private(set) var maxConcurrent = 0
        private var active = 0

        func begin() { active += 1; maxConcurrent = max(maxConcurrent, active) }
        func end(_ count: Int) { active -= 1; totalBytes += count; blobCount += 1 }
    }

    @Test func deliversExactlyTheRequestedByteCount() async throws {
        let source = FakeAssetDataSource(totalBytes: 25, blobSize: 10)
        let collector = Collector()
        try await source.read(assetID: "A", from: 0) { blob in
            await collector.begin()
            await collector.end(blob.count)
        }
        #expect(await collector.totalBytes == 25)
        #expect(await collector.blobCount == 3)   // 10 + 10 + 5
    }

    @Test func startingOffsetReducesDeliveredBytes() async throws {
        let source = FakeAssetDataSource(totalBytes: 100, blobSize: 30)
        let collector = Collector()
        try await source.read(assetID: "A", from: 40) { blob in
            await collector.begin()
            await collector.end(blob.count)
        }
        #expect(await collector.totalBytes == 60)
    }

    /// The capacity-1 guarantee from spec §5.2 clause 2.
    @Test func neverInvokesSinkConcurrently() async throws {
        let source = FakeAssetDataSource(totalBytes: 1000, blobSize: 10)
        let collector = Collector()
        try await source.read(assetID: "A", from: 0) { blob in
            await collector.begin()
            try await Task.sleep(for: .microseconds(50))
            await collector.end(blob.count)
        }
        #expect(await collector.maxConcurrent == 1)
    }

    @Test func propagatesSinkErrors() async throws {
        let source = FakeAssetDataSource(totalBytes: 100, blobSize: 10)
        await #expect(throws: FakeSourceError.injected) {
            try await source.read(assetID: "A", from: 0) { _ in
                throw FakeSourceError.injected
            }
        }
    }

    @Test func injectedFailureStopsDelivery() async throws {
        let source = FakeAssetDataSource(totalBytes: 100, blobSize: 10, failAfterBlobs: 3)
        let collector = Collector()
        await #expect(throws: FakeSourceError.injected) {
            try await source.read(assetID: "A", from: 0) { blob in
                await collector.begin()
                await collector.end(blob.count)
            }
        }
        #expect(await collector.blobCount == 3)
    }

    /// Spec §5.2 clause 3 — conformances must honour cancellation.
    @Test func honoursTaskCancellation() async throws {
        let source = FakeAssetDataSource(totalBytes: 10_000, blobSize: 10)
        let collector = Collector()
        let task = Task {
            try await source.read(assetID: "A", from: 0) { blob in
                await collector.begin()
                try await Task.sleep(for: .milliseconds(1))
                await collector.end(blob.count)
            }
        }
        try await Task.sleep(for: .milliseconds(20))
        task.cancel()
        await #expect(throws: (any Error).self) { try await task.value }
        #expect(await collector.blobCount < 1000)
    }

    // MARK: - Harness validity

    /// The memory gate is only meaningful if `BlobLifetimeTracker` actually
    /// observes blob deaths. This replaces an earlier `phys_footprint`-based
    /// check that was deleted: process-wide footprint proved unable to see
    /// retained memory under allocation churn, and could not be asserted
    /// reliably while other suites ran in parallel. Liveness is exact.
    @Test func trackerObservesEveryBlobAllocationAndFree() async throws {
        let tracker = BlobLifetimeTracker()
        let source = FakeAssetDataSource(totalBytes: 1000, blobSize: 100,
                                         tracker: tracker, fillRandom: false)

        try await source.read(assetID: "A", from: 0) { _ in }

        #expect(tracker.allocated == 10)
        #expect(tracker.alive == 0)      // every blob freed once the sink returned
        #expect(tracker.maxAlive == 1)   // the fake itself never holds two
    }

    /// A retained blob must keep `alive` elevated — proving the tracker can
    /// actually detect the leak shape the gate relies on it to catch.
    @Test func trackerDetectsRetainedBlobs() async throws {
        let tracker = BlobLifetimeTracker()
        let source = FakeAssetDataSource(totalBytes: 500, blobSize: 100,
                                         tracker: tracker, fillRandom: false)

        nonisolated(unsafe) var retained: [Data] = []
        try await source.read(assetID: "A", from: 0) { blob in
            retained.append(blob)
        }

        #expect(tracker.allocated == 5)
        #expect(tracker.alive == 5)      // nothing freed: all still referenced
        #expect(tracker.maxAlive == 5)
        retained.removeAll()
        #expect(tracker.alive == 0)      // and freed once released
    }
}
