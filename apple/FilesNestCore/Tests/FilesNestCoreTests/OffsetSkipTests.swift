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

    /// The returned Data must NOT be a slice aliasing the input's storage — a
    /// retained slice keeps the whole input buffer alive, which is the
    /// copy-on-write failure mode from spec §2.2.
    @Test func returnedDataDoesNotAliasInputStorage() {
        var skip = OffsetSkip(skipping: 4)
        let result = skip.take(bytes(1...10))
        #expect(result?.startIndex == 0)
    }
}
