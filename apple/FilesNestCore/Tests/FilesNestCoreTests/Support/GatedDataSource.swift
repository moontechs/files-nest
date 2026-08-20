import Foundation
@testable import FilesNestCore

/// An `AssetDataSource` that reports entry/exit to an `ArrivalGate`, so a test
/// can observe how many uploads run concurrently. Produces `totalBytes` of
/// zeroed blobs (content is irrelevant to concurrency).
struct GatedDataSource: AssetDataSource {
    let gate: ArrivalGate
    let totalBytes: Int64
    let blobSize: Int

    func read(assetID: String, from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws {
        await gate.enter()
        var sent = offset
        while sent < totalBytes {
            let n = Int(min(Int64(blobSize), totalBytes - sent))
            try await sink(Data(count: n))
            sent += Int64(n)
        }
        await gate.exit()
    }
}
