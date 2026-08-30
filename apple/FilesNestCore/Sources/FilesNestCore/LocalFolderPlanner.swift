import Foundation

/// Computes deterministic paths and pure orphan diffs for a local-folder destination.
public enum LocalFolderPlanner {
    public static func expectedPath(for asset: AssetResource, destinationRoot: URL) -> URL {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        let components = calendar.dateComponents([.year, .month, .day], from: asset.creationDate)
        let year = String(format: "%04d", components.year ?? 0)
        let month = String(format: "%02d", components.month ?? 0)
        let day = String(format: "%02d", components.day ?? 0)

        let original = asset.filename as NSString
        let stem = original.deletingPathExtension
        let ext = original.pathExtension
        let name = ext.isEmpty
            ? "\(stem)_\(safeID(asset.key.encoded))"
            : "\(stem)_\(safeID(asset.key.encoded)).\(ext)"
        return destinationRoot.appendingPathComponent(year)
            .appendingPathComponent(month)
            .appendingPathComponent(day)
            .appendingPathComponent(name)
    }

    public static func planDeletes(library: [AssetResource], actualPaths: Set<URL>, destinationRoot: URL) -> [(path: URL, key: ResourceKey)] {
        let expected = Set(library.map { expectedPath(for: $0, destinationRoot: destinationRoot) })
        return actualPaths.subtracting(expected).sorted { $0.path < $1.path }.map {
            // The path format intentionally contains no resource key. The key is only
            // diagnostic metadata for the delete report, so use a stable path-derived key.
            (path: $0, key: ResourceKey(localIdentifier: $0.path, kind: .photo))
        }
    }

    /// A completed local backup is always a regular, non-symlinked file.
    public static func isCompletedFile(at path: URL) -> Bool {
        guard let values = try? path.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey]) else {
            return false
        }
        return values.isRegularFile == true && values.isSymbolicLink != true
    }

    /// FilesNest-owned names end in the URL-safe, unpadded SHA-256 suffix produced by `safeID`.
    /// This keeps delete mirroring from treating ordinary files in a chosen date folder as ours.
    public static func isManagedPath(_ path: URL) -> Bool {
        let basename = (path.lastPathComponent as NSString).deletingPathExtension
        let suffixLength = 43 // SHA-256 encoded with URL-safe base64 and no padding.
        guard basename.count > suffixLength + 1 else { return false }
        let suffixStart = basename.index(basename.endIndex, offsetBy: -suffixLength)
        guard basename[basename.index(before: suffixStart)] == "_" else { return false }
        return basename[suffixStart...].allSatisfy { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" }
    }
}
