import Foundation
import Photos
import FilesNestCore

/// PhotoKit enumeration adapter — the `AssetLibrary` counterpart to
/// `PhotosAssetDataSource`. Enumerates `PHAsset`s in `range` and maps each
/// addressable `PHAssetResource` to an `AssetResource` keyed by `ResourceKey`.
/// A Live Photo yields two entries (`#photo` + `#pairedVideo`) sharing `bundleID`,
/// exactly what `SyncPlanner` expects (design §3.3, §5.4).
nonisolated struct PhotosAssetLibrary: AssetLibrary {

    enum LibraryError: Error, Equatable {
        case authorizationDenied(PHAuthorizationStatus)
    }

    func resources(in range: SyncRange) async throws -> [AssetResource] {
        try await ensureAuthorized()

        // Enumerating a large library is heavy, synchronous PhotoKit work. Run it on a GCD
        // queue rather than the Swift concurrency cooperative pool, so a 70k-asset scan can't
        // starve other async work (status/progress delivery) while it runs.
        return await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let options = PHFetchOptions()
                options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
                if case .dates(let r) = range {
                    options.predicate = NSPredicate(format: "creationDate >= %@ AND creationDate <= %@",
                                                    r.lowerBound as NSDate, r.upperBound as NSDate)
                }

                let assets = PHAsset.fetchAssets(with: options)
                var out: [AssetResource] = []
                assets.enumerateObjects { asset, _, _ in
                    let isLive = asset.mediaSubtypes.contains(.photoLive)
                    let created = asset.creationDate ?? .distantPast   // non-optional key field (design §3.4)
                    for resource in PHAssetResource.assetResources(for: asset) {
                        guard let kind = Self.mapType(resource.type) else { continue }   // skip unaddressed types
                        out.append(AssetResource(
                            key: ResourceKey(localIdentifier: asset.localIdentifier, kind: kind),
                            filename: resource.originalFilename,
                            creationDate: created,
                            bundleID: isLive ? asset.localIdentifier : nil))
                    }
                }
                continuation.resume(returning: out)
            }
        }
    }

    private func ensureAuthorized() async throws {
        let current = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        let status = current == .notDetermined
            ? await PHPhotoLibrary.requestAuthorization(for: .readWrite)
            : current
        guard status == .authorized || status == .limited else {
            throw LibraryError.authorizationDenied(status)
        }
    }

    /// Reverse of `PhotosAssetDataSource.mapKind`. Returns nil for resource types
    /// this client does not address (adjustment data, full-size video, etc.); those
    /// are skipped, not errored — they are not uploadable resources.
    private static func mapType(_ type: PHAssetResourceType) -> ResourceKind? {
        switch type {
        case .photo:          return .photo
        case .video:          return .video
        case .audio:          return .audio
        case .pairedVideo:    return .pairedVideo
        case .fullSizePhoto:  return .fullSizePhoto
        case .alternatePhoto: return .alternatePhoto
        default:              return nil
        }
    }
}
