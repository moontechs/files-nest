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

    func resources(in range: SyncRange,
                   onProgress: (@Sendable (_ done: Int, _ total: Int) -> Void)? = nil) async throws -> [AssetResource] {
        try await ensureAuthorized()

        // Enumerating a large library is heavy, synchronous PhotoKit work (a
        // `PHAssetResource.assetResources(for:)` lookup per asset). Run it on a GCD queue
        // off the cooperative pool, and make it cancellable so Pause / sign-out during the
        // scan stop it promptly instead of churning to completion.
        let cancel = CancelFlag()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<[AssetResource], Error>) in
                DispatchQueue.global(qos: .userInitiated).async {
                    #if DEBUG
                    let clock = Date()
                    print("🟢 FN library: enumeration start (range=\(range))")
                    #endif
                    let options = PHFetchOptions()
                    options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
                    if case .dates(let r) = range {
                        options.predicate = NSPredicate(format: "creationDate >= %@ AND creationDate <= %@",
                                                        r.lowerBound as NSDate, r.upperBound as NSDate)
                    } else if case .modifiedSince(let d) = range {
                        options.predicate = NSPredicate(format: "modificationDate >= %@", d as NSDate)
                    }

                    let assets = PHAsset.fetchAssets(with: options)
                    let total = assets.count
                    var out: [AssetResource] = []
                    assets.enumerateObjects { asset, idx, stop in
                        if cancel.isSet { stop.pointee = true; return }   // Pause/sign-out cancelled the scan
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
                        // Throttle: emit every 250 assets and on the last (design §10 — tune during verify).
                        if let onProgress, idx % 250 == 0 || idx == total - 1 { onProgress(idx + 1, total) }
                    }
                    if cancel.isSet {
                        continuation.resume(throwing: CancellationError())
                        return
                    }
                    #if DEBUG
                    print("🟢 FN library: enumerated \(out.count) resources from \(assets.count) assets in \(String(format: "%.2f", -clock.timeIntervalSinceNow))s")
                    #endif
                    continuation.resume(returning: out)
                }
            }
        } onCancel: {
            cancel.set()
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

/// Thread-safe one-shot cancel flag for the off-cooperative-thread enumeration.
private final class CancelFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    var isSet: Bool { lock.lock(); defer { lock.unlock() }; return value }
    func set() { lock.lock(); value = true; lock.unlock() }
}
