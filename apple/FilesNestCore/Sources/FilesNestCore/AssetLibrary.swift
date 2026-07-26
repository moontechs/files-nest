import Foundation

/// The enumeration seam. The app conforms this to PhotoKit (later slice); tests
/// use `FakeAssetLibrary`. A Live Photo MUST yield both resource keys.
public protocol AssetLibrary: Sendable {
    func resources(in range: SyncRange) async throws -> [AssetResource]
}
