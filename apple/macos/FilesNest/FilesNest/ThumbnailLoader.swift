import AppKit
import Photos

/// Loads PHAsset thumbnails for the panel, with an in-memory cache. Returns nil when the
/// asset is missing (deleted / not in the library) so callers show the placeholder.
final class ThumbnailLoader: @unchecked Sendable {
    private let cache = NSCache<NSString, NSImage>()
    private let manager = PHImageManager.default()

    func thumbnail(for id: String, size: CGSize) async -> NSImage? {
        if let hit = cache.object(forKey: id as NSString) { return hit }
        guard let asset = PHAsset.fetchAssets(withLocalIdentifiers: [id], options: nil).firstObject else {
            return nil
        }
        let options = PHImageRequestOptions()
        options.deliveryMode = .opportunistic
        options.resizeMode = .fast
        options.isNetworkAccessAllowed = true
        options.isSynchronous = false

        return await withCheckedContinuation { (cont: CheckedContinuation<NSImage?, Never>) in
            let resumed = ResumeOnce()
            manager.requestImage(for: asset, targetSize: size, contentMode: .aspectFill, options: options) { image, info in
                // `.opportunistic` may call back twice (degraded then full). Cache the latest image;
                // resume the continuation once, on the first non-degraded result.
                if let image { self.cache.setObject(image, forKey: id as NSString) }
                let isDegraded = (info?[PHImageResultIsDegradedKey] as? Bool) ?? false
                if !isDegraded, resumed.tryResume() { cont.resume(returning: image) }
            }
        }
    }
}

/// One-shot guard so a multi-callback PHImageManager request resumes its continuation exactly once.
private final class ResumeOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func tryResume() -> Bool { lock.lock(); defer { lock.unlock() }; if done { return false }; done = true; return true }
}
