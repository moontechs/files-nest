import Foundation

/// Discards a fixed prefix of a byte stream delivered as successive blobs.
///
/// `PHAssetResourceManager.requestData` cannot resume mid-file: after an
/// interruption iCloud restarts at byte 0 regardless of the TUS offset. Adapters
/// use this to satisfy `AssetDataSource`'s "deliver from `offset`" contract.
/// Expected behaviour, not a bug — see `docs/architecture.md`.
///
/// This lives in core, tested, rather than in each adapter: the discard is
/// exactly the `dropFirst` shape implicated in the previous client's memory
/// leak, and adapters are the one component `swift test` cannot reach.
public struct OffsetSkip: Sendable {
    private var remaining: Int64

    public init(skipping: Int64) {
        self.remaining = max(0, skipping)
    }

    /// Returns the portion of `blob` at or beyond the skip point, or nil when
    /// the blob falls entirely inside the skipped prefix.
    public mutating func take(_ blob: Data) -> Data? {
        guard remaining > 0 else { return blob }
        let count = Int64(blob.count)
        if remaining >= count {
            remaining -= count
            return nil
        }
        let dropCount = Int(remaining)
        remaining = 0
        // `Data(_:)` copies into fresh storage. Returning `blob.dropFirst(_:)`
        // directly would hand back a slice that keeps the ENTIRE input buffer
        // alive for as long as the remainder is retained.
        return Data(blob.dropFirst(dropCount))
    }
}
