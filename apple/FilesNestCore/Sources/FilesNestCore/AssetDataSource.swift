import Foundation

/// Streams an asset's bytes to a sink, one blob at a time.
///
/// The sink IS the backpressure mechanism. Because the sink is `async` and the
/// source must await it, "at most one blob in flight" is guaranteed by this
/// signature rather than by coordination code that has to be kept correct.
///
/// This is deliberately NOT an `AsyncSequence`. `AsyncThrowingStream` has no
/// producer backpressure, and its buffering policies DROP elements rather than
/// throttling — which for file bytes is silent corruption, not throttling.
/// A stream plus a semaphore was tried in the previous client: it serialized
/// correctly but still grew linearly, because appending and slicing one
/// long-lived buffer kept copy-on-write backing storage alive. See
/// `docs/design/20260724-assetuploader.md` §2.
///
/// `FilesNestCore` stays pure Foundation; the `Photos`-backed conformance lives
/// in the app target.
public protocol AssetDataSource: Sendable {

    /// Delivers the asset's bytes starting at `offset`.
    ///
    /// Conformances MUST:
    /// 1. Deliver bytes starting at `offset`, in order, with no gaps or overlaps.
    /// 2. Fully await `sink` before producing the next blob (capacity-1).
    /// 3. Honour `Task` cancellation and stop producing promptly.
    /// 4. Throw on failure. Partial delivery is legal — the next run resumes
    ///    from the TUS offset.
    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws
}
