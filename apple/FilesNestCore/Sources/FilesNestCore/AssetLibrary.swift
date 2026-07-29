import Foundation

/// The enumeration seam. The app conforms this to PhotoKit; tests use
/// `FakeAssetLibrary`. A Live Photo MUST yield both resource keys.
/// `onProgress(done, total)` reports scan progress (assets enumerated of the asset
/// count) so a launch "counting" state can be determinate; pass `nil` when unneeded.
public protocol AssetLibrary: Sendable {
    func resources(in range: SyncRange,
                   onProgress: (@Sendable (_ done: Int, _ total: Int) -> Void)?) async throws -> [AssetResource]
}

public extension AssetLibrary {
    /// Convenience for callers that don't need progress (e.g. `SyncCoordinator`'s scan).
    func resources(in range: SyncRange) async throws -> [AssetResource] {
        try await resources(in: range, onProgress: nil)
    }
}
