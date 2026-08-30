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
}
