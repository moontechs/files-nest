import Foundation

/// Uploads one asset's bytes to one TUS upload record.
///
/// Peak memory is two blobs, independent of asset size, and each asset is read
/// exactly once. Bytes go straight from the source into PATCH bodies — there is
/// no accumulation buffer, so there is no buffer to mismanage.
///
/// Handles nothing and propagates everything: `ServerClientError.backendLost`
/// goes to `SyncCoordinator`, which owns delete-and-re-register recovery.
/// Keeping recovery out of here is what lets this stay a stateless `struct`.
public struct AssetUploader: Sendable {
    private let client: ServerClient
    private let source: any AssetDataSource

    public init(client: ServerClient, source: any AssetDataSource) {
        self.client = client
        self.source = source
    }

    func reportingRetries(with client: ServerClient) -> AssetUploader {
        AssetUploader(client: client, source: source)
    }

    public func upload(assetID: String, uploadID: String) async throws {
        let start = try await client.offset(forUploadID: uploadID)
        let state = LookAhead(client: client, uploadID: uploadID, startOffset: start.offset)

        // The closure captures ONLY a Sendable actor reference. Capturing and
        // mutating local vars here is rejected under Swift 6 strict concurrency
        // (#SendableClosureCaptures) — see design §6.2.1.
        try await source.read(assetID: assetID, from: start.offset) { blob in
            try await state.consume(blob)
        }
        try await state.finish()
    }
}

/// Holds one blob back so the final PATCH can declare `Upload-Length`.
///
/// A blob is only known to be the last one when the source completes — after a
/// naive pass-through would already have sent it. tusd will not finalize an
/// upload whose size is still deferred, so the last PATCH must carry the length.
private actor LookAhead {
    private let client: ServerClient
    private let uploadID: String
    private var offset: Int64
    private var held: Data?

    init(client: ServerClient, uploadID: String, startOffset: Int64) {
        self.client = client
        self.uploadID = uploadID
        self.offset = startOffset
    }

    /// Actors serialize access but are REENTRANT, so concurrent calls would in
    /// principle interleave across the `patchData` await and produce
    /// out-of-order offsets. No runtime guard is needed: `AssetDataSource.read`
    /// takes `sink` as a NON-ESCAPING parameter, so a conformance cannot put it
    /// in a task group or `async let` — the compiler rejects it. Concurrent
    /// invocation is unrepresentable rather than merely forbidden.
    ///
    /// If that parameter is ever made `@escaping`, this guarantee disappears and
    /// a reentrancy guard must be reintroduced here.
    func consume(_ blob: Data) async throws {
        if let previous = held {
            held = nil
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: previous, finalLength: nil)
        }
        held = blob
    }

    func finish() async throws {
        if let last = held {
            held = nil
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: last,
                                                finalLength: offset + Int64(last.count))
        } else {
            // Zero-blob path: no chunk to carry the length, so declare it with an
            // empty PATCH. Verified against tusd in
            // `TestTUSZeroByteFinalPatchDeclaresLength`.
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: Data(), finalLength: offset)
        }
        try await client.markComplete(uploadID: uploadID)
    }
}
