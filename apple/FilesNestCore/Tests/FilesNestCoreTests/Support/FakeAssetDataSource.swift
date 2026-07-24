import Foundation
@testable import FilesNestCore

enum FakeSourceError: Error, Equatable { case injected }

/// Synthetic `AssetDataSource` for headless tests, including the memory gate.
///
/// HARNESS VALIDITY (design §7.4), invariants this type must preserve:
///
/// 1. **Never retain yielded blobs.** Each blob is freshly allocated, handed to
///    the sink, and dropped. Holding them would make the gate measure the fake.
/// 2. **Blobs must be individually accountable.** Memory is allocated here and
///    wrapped with `Data(bytesNoCopy:count:deallocator:)`, so `BlobLifetimeTracker`
///    learns exactly when each buffer dies. This is what makes the liveness gate
///    exact where `phys_footprint` was blind.
/// 3. **Incompressible content when measuring footprint.** `fillRandom` writes
///    xorshift bytes. An earlier version wrote one byte per 4 KB page and left the
///    rest zero; macOS's memory compressor reduced those pages to nearly nothing,
///    so leaked memory vanished from `phys_footprint`. Content is irrelevant to
///    the liveness gate, so that path can use the cheaper constant fill.
struct FakeAssetDataSource: AssetDataSource {
    let totalBytes: Int64
    let blobSize: Int
    var failAfterBlobs: Int?
    var tracker: BlobLifetimeTracker?
    /// Incompressible fill. Needed only when something measures real memory.
    var fillRandom: Bool

    init(totalBytes: Int64,
         blobSize: Int,
         failAfterBlobs: Int? = nil,
         tracker: BlobLifetimeTracker? = nil,
         fillRandom: Bool = true) {
        self.totalBytes = totalBytes
        self.blobSize = blobSize
        self.failAfterBlobs = failAfterBlobs
        self.tracker = tracker
        self.fillRandom = fillRandom
    }

    /// Allocates `count` bytes wrapped so its lifetime is observable.
    private func makeBlob(_ count: Int) -> Data {
        let ptr = UnsafeMutableRawPointer.allocate(byteCount: max(count, 1), alignment: 8)

        if fillRandom {
            var state: UInt64 = 0x9E37_79B9_7F4A_7C15
            let words = count / 8
            let wordPtr = ptr.bindMemory(to: UInt64.self, capacity: words)
            for i in 0..<words {
                state ^= state << 13
                state ^= state >> 7
                state ^= state << 17
                wordPtr[i] = state
            }
            if count > words * 8 {
                let tail = count - words * 8
                let bytePtr = (ptr + words * 8).bindMemory(to: UInt8.self, capacity: tail)
                for i in 0..<tail {
                    state ^= state << 13
                    state ^= state >> 7
                    state ^= state << 17
                    bytePtr[i] = UInt8(truncatingIfNeeded: state)
                }
            }
        } else {
            // Content irrelevant, but the memory must be initialized before use.
            ptr.initializeMemory(as: UInt8.self, repeating: 0xA5, count: count)
        }

        let tracker = self.tracker
        tracker?.didAllocate()
        return Data(bytesNoCopy: ptr, count: count, deallocator: .custom { pointer, _ in
            pointer.deallocate()
            tracker?.didFree()
        })
    }

    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws {
        var produced = min(max(0, offset), totalBytes)
        var emitted = 0
        while produced < totalBytes {
            try Task.checkCancellation()
            if let limit = failAfterBlobs, emitted >= limit { throw FakeSourceError.injected }
            let count = Int(min(Int64(blobSize), totalBytes - produced))
            try await sink(makeBlob(count))    // fresh allocation, never retained
            produced += Int64(count)
            emitted += 1
        }
    }
}
