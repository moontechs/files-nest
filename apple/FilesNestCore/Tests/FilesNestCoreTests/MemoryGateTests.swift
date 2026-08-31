import Testing
import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
@testable import FilesNestCore

/// The slice's central guarantee: memory is a function of BLOB size, never of
/// ASSET size. A 3 MB photo and a 3 GB video must cost the same.
///
/// **Why this is measured by blob liveness and not by `phys_footprint`.**
/// The footprint approach was built first and proved unusable. With a leak
/// deliberately injected into `LookAhead` — retaining a slice-copy of every blob,
/// the previous client's actual bug — instrumentation confirmed 200,802,304 bytes
/// were genuinely retained, while `phys_footprint` reported 39 MB and the gate
/// passed. Isolated: the same 200 MB retained WITHOUT concurrent allocation churn
/// reported 148 MB. A large upload churns by definition, so macOS compresses and
/// swaps out the cold retained pages and the metric stops counting them. It is
/// structurally blind to the workload it would need to measure.
///
/// Blob liveness is exact. `FakeAssetDataSource` allocates each blob itself and
/// wraps it in `Data(bytesNoCopy:count:deallocator:)`, so the deallocator fires
/// exactly when the last reference dies. Retaining a blob, or an aliasing slice
/// of one, keeps it alive and `maxAlive` climbs — regardless of what the OS does
/// with the pages.
///
/// Two further defects were found and fixed while building this, both of which
/// had produced vacuous passes:
/// - Baseline contamination: `peakGrowth` is relative, the allocator does not
///   return freed pages promptly, so the case that ran second reported a NEGATIVE
///   delta. Absolute peaks and a warm-up fixed that.
/// - Compressible blobs: the fake wrote one byte per 4 KB page, ~99.97% zeros,
///   which the memory compressor erased. Blobs are now verified incompressible
///   (256/256 distinct byte values).
///
/// **Verified to have teeth.** A passing gate proves nothing unless it can fail,
/// so both failure modes were injected into `LookAhead` and measured:
///
/// | Injected leak | maxAlive 8 blobs → 64 blobs | Caught |
/// |---|---|---|
/// | `leak.append(previous.dropFirst(1))` (aliasing slice, COW-retains the whole buffer — the historical bug) | 8 → 64 | YES |
/// | `leak.append(Data(previous.prefix(512 * 1024)))` (copies into fresh storage) | 2 → 2 | no |
///
/// Clean code measures 2 → 2. The aliasing case is the failure mode recorded in
/// `ios/memory_management_lessons` and the one this slice exists to prevent; it
/// produces a perfectly linear signal. The copying case allocates memory the
/// tracker never handed out and is invisible here by construction — `phys_footprint`
/// cannot see it either, for the churn reason above. That gap is known and
/// unclosed; `footprintSmokeCheck` would still catch a gross whole-asset buffer.
///
/// Serialized: reuses one per-host handler (`Self.host`), and `phys_footprint` is
/// process-wide so parallel tests contaminate the readings.
@Suite(.serialized)
struct MemoryGateTests {

    static let host = "gate.test"
    static let blobSize = 8 * 1024 * 1024

    /// Look-ahead holds one blob back while the previous is in flight, so two is
    /// the design floor. The transport may briefly reference a third while its
    /// PATCH body is being consumed.
    static let maxExpectedLiveBlobs = 3

    final class ByteCounter: @unchecked Sendable {
        private let lock = NSLock()
        private var _total: Int64 = 0
        var total: Int64 { lock.lock(); defer { lock.unlock() }; return _total }
        func add(_ n: Int64) { lock.lock(); defer { lock.unlock() }; _total += n }
    }

    /// Discards bodies. Never call `httpBodyStreamData()` here — it accumulates
    /// the whole body and would measure the harness instead of the uploader.
    func installDiscardingHandler(counter: ByteCounter) {
        MockURLProtocol.setHandler(forHost: Self.host) { req in
            let url = req.url!
            switch (req.httpMethod, url.path) {
            case ("HEAD", _):
                return MockURLProtocol.respond(
                    status: 200, headers: ["Upload-Offset": "0"], for: url)
            case ("PATCH", let path) where path.hasSuffix("/data"):
                let offset = Int64(req.value(forHTTPHeaderField: "Upload-Offset") ?? "0") ?? 0
                let length = req.httpBodyByteCount()
                counter.add(length)
                return MockURLProtocol.respond(
                    status: 204, headers: ["Upload-Offset": String(offset + length)], for: url)
            default:
                return MockURLProtocol.respond(status: 200, for: url)
            }
        }
    }

    struct Measurement {
        let maxAlive: Int
        let aliveAfter: Int
        let blobsAllocated: Int
        let transferred: Int64
    }

    func measure(totalBytes: Int64, fillRandom: Bool = false) async throws -> Measurement {
        let counter = ByteCounter()
        let tracker = BlobLifetimeTracker()
        installDiscardingHandler(counter: counter)

        let client = ServerClient(baseURL: URL(string: "https://\(Self.host)")!,
                                  credentials: FakeCredentialStore(creds: nil),
                                  session: MockURLProtocol.makeSession())
        let source = FakeAssetDataSource(totalBytes: totalBytes,
                                         blobSize: Self.blobSize,
                                         tracker: tracker,
                                         fillRandom: fillRandom)
        try await AssetUploader(client: client, source: source)
            .upload(assetID: "A", uploadID: "U")

        return Measurement(maxAlive: tracker.maxAlive,
                           aliveAfter: tracker.alive,
                           blobsAllocated: tracker.allocated,
                           transferred: counter.total)
    }

    /// THE GATE. 3 MB photo vs 3 GB video: 1024x the data, same peak liveness.
    @Test func liveBlobCountIsBoundedAndIndependentOfAssetSize() async throws {
        let photo = try await measure(totalBytes: 3 * 1024 * 1024)          // 3 MB
        let video = try await measure(totalBytes: 3 * 1024 * 1024 * 1024)   // 3 GB

        print("""
        MEMORY GATE (blob liveness)
          3 MB photo : maxAlive=\(photo.maxAlive) allocated=\(photo.blobsAllocated) \
        aliveAfter=\(photo.aliveAfter) transferred=\(photo.transferred)
          3 GB video : maxAlive=\(video.maxAlive) allocated=\(video.blobsAllocated) \
        aliveAfter=\(video.aliveAfter) transferred=\(video.transferred)
        """)

        // VALIDITY: the transfers must actually have happened, and the fake must
        // really have produced one blob per chunk. Without this a source that
        // yielded nothing would trivially satisfy every liveness assertion.
        #expect(photo.transferred == 3 * 1024 * 1024)
        #expect(video.transferred == 3 * 1024 * 1024 * 1024)
        #expect(photo.blobsAllocated == 1)
        #expect(video.blobsAllocated == 384)

        // Nothing may outlive the upload.
        #expect(photo.aliveAfter == 0)
        #expect(video.aliveAfter == 0)

        // THE assertion: 1024x the bytes must not raise peak liveness.
        #expect(video.maxAlive <= Self.maxExpectedLiveBlobs)
    }

    /// Same property across two multi-blob sizes, so the single-blob photo case
    /// cannot mask a per-blob growth term.
    @Test func liveBlobCountDoesNotGrowWithBlobCount() async throws {
        let small = try await measure(totalBytes: 64 * 1024 * 1024)    // 8 blobs
        let large = try await measure(totalBytes: 512 * 1024 * 1024)   // 64 blobs

        print("MEMORY GATE (scaling): 64MB maxAlive=\(small.maxAlive) " +
              "blobs=\(small.blobsAllocated) | 512MB maxAlive=\(large.maxAlive) " +
              "blobs=\(large.blobsAllocated)")

        #expect(small.blobsAllocated == 8)
        #expect(large.blobsAllocated == 64)
        #expect(small.aliveAfter == 0)
        #expect(large.aliveAfter == 0)
        #expect(large.maxAlive == small.maxAlive)
        #expect(large.maxAlive <= Self.maxExpectedLiveBlobs)
    }
}
