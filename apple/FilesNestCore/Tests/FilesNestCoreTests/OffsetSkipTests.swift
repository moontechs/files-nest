import Testing
import Foundation
@testable import FilesNestCore

@Suite struct OffsetSkipTests {

    private func bytes(_ range: ClosedRange<UInt8>) -> Data {
        Data(range.map { $0 })
    }

    @Test func zeroSkipPassesBlobsThrough() {
        var skip = OffsetSkip(skipping: 0)
        let blob = bytes(1...10)
        #expect(skip.take(blob) == blob)
    }

    @Test func negativeSkipIsTreatedAsZero() {
        var skip = OffsetSkip(skipping: -5)
        let blob = bytes(1...10)
        #expect(skip.take(blob) == blob)
    }

    @Test func fullyConsumedBlobReturnsNil() {
        var skip = OffsetSkip(skipping: 10)
        #expect(skip.take(bytes(1...10)) == nil)
    }

    @Test func partiallyConsumedBlobReturnsRemainder() {
        var skip = OffsetSkip(skipping: 4)
        #expect(skip.take(bytes(1...10)) == bytes(5...10))
    }

    @Test func skipSpansMultipleBlobs() {
        var skip = OffsetSkip(skipping: 25)
        #expect(skip.take(bytes(1...10)) == nil)           // 15 remaining
        #expect(skip.take(bytes(1...10)) == nil)           // 5 remaining
        #expect(skip.take(bytes(1...10)) == bytes(6...10)) // consumed
        #expect(skip.take(bytes(1...10)) == bytes(1...10)) // pass-through after
    }

    @Test func skipBeyondAllDataReturnsNilForever() {
        var skip = OffsetSkip(skipping: 1000)
        #expect(skip.take(bytes(1...10)) == nil)
        #expect(skip.take(bytes(1...10)) == nil)
    }

    @Test func emptyBlobIsHandled() {
        var skip = OffsetSkip(skipping: 4)
        #expect(skip.take(Data()) == nil)
        #expect(skip.take(bytes(1...10)) == bytes(5...10))
    }

    /// The returned Data must NOT retain the input's storage. A retained slice
    /// keeps the WHOLE input buffer alive, which is the copy-on-write failure
    /// mode from design §2.2 — an 8 MB blob kept alive by a 1-byte slice.
    ///
    /// This proves non-retention directly, by observing the input buffer's
    /// deallocator. An earlier version only asserted `startIndex == 0`, which an
    /// aliasing implementation with normalised indices would also satisfy —
    /// inferring independence rather than demonstrating it.
    @Test func returnedDataDoesNotRetainInputStorage() {
        // Must exceed Data's inline-storage threshold (~14 bytes on 64-bit).
        // At or below it, Data(bytesNoCopy:) copies into inline storage and fires
        // the deallocator immediately, which would make this test meaningless.
        let size = 1024
        let tracker = BlobLifetimeTracker()
        var skip = OffsetSkip(skipping: 4)
        var result: Data?

        do {
            let ptr = UnsafeMutableRawPointer.allocate(byteCount: size, alignment: 8)
            ptr.initializeMemory(as: UInt8.self, repeating: 7, count: size)
            tracker.didAllocate()
            let source = Data(bytesNoCopy: ptr, count: size, deallocator: .custom { p, _ in
                p.deallocate()
                tracker.didFree()
            })
            result = skip.take(source)
            // ARC releases at LAST USE, not at scope end, so `source` would
            // already be gone here without withExtendedLifetime — this pins the
            // tracker as working before the assertion below means anything.
            withExtendedLifetime(source) {
                #expect(tracker.alive == 1)
            }
        }

        // `source` is gone but `result` is still held. If the remainder aliased
        // the input, its buffer could not have been freed.
        #expect(tracker.alive == 0)
        #expect(result?.count == size - 4)
        #expect(result == Data(repeating: 7, count: size - 4))
    }
}
