import Foundation
import Photos
import FilesNestCore

/// `AssetDataSource` backed by PhotoKit. All contract logic lives in
/// `CallbackStreamReader`; this only resolves the resource and supplies two
/// closures. See docs/design/20260724-photosassetdatasource.md §3, §5.
nonisolated struct PhotosAssetDataSource: AssetDataSource {

    enum ResolveError: Error {
        case assetNotFound(String)
        case resourceNotFound(ResourceKind)
    }

    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable @concurrent (Data) async throws -> Void) async throws {
        let key = try ResourceKey(parsing: assetID)
        let resource = try Self.resolveResource(key)

        let options = PHAssetResourceRequestOptions()
        options.isNetworkAccessAllowed = true   // iCloud-only originals

        let manager = PHAssetResourceManager.default()
        let reader = CallbackStreamReader<PHAssetResourceDataRequestID>(
            start: { onData, onDone in
                manager.requestData(
                    for: resource,
                    options: options,
                    dataReceivedHandler: { data in _ = onData(data) },
                    completionHandler: { error in onDone(error) })
            },
            cancel: { id in manager.cancelDataRequest(id) })

        try await reader.read(from: offset, into: sink)
    }

    /// Resolves the ResourceKey to a concrete PHAssetResource. Throws rather
    /// than falling back to a primary resource — a silent fallback would upload
    /// the wrong bytes under the right key (spec §5).
    private static func resolveResource(_ key: ResourceKey) throws -> PHAssetResource {
        let fetch = PHAsset.fetchAssets(withLocalIdentifiers: [key.localIdentifier], options: nil)
        guard let asset = fetch.firstObject else {
            throw ResolveError.assetNotFound(key.localIdentifier)
        }
        let wanted = mapKind(key.kind)
        let resources = PHAssetResource.assetResources(for: asset)
        guard let match = resources.first(where: { $0.type == wanted }) else {
            throw ResolveError.resourceNotFound(key.kind)
        }
        return match
    }

    private static func mapKind(_ kind: ResourceKind) -> PHAssetResourceType {
        switch kind {
        case .photo:          return .photo
        case .video:          return .video
        case .audio:          return .audio
        case .pairedVideo:    return .pairedVideo
        case .fullSizePhoto:  return .fullSizePhoto
        case .alternatePhoto: return .alternatePhoto
        }
    }
}
