import Foundation

/// Measures disk usage for the streaming measurement run.
///
/// `directorySize`/`sizeDelta` sample a directory's byte footprint; under App
/// Sandbox they cannot read the Photos library container, so the measurement run
/// uses `volumeFreeSpace` instead, which reads a sandbox-safe volume resource
/// value. See docs/design/20260724-photosassetdatasource.md §6.3.
public enum DiskProbe {

    /// Total allocated size of all regular files under `url`, recursively.
    public static func directorySize(at url: URL) throws -> Int64 {
        let fm = FileManager.default
        guard let enumerator = fm.enumerator(
            at: url,
            includingPropertiesForKeys: [.isRegularFileKey,
                                         .totalFileAllocatedSizeKey,
                                         .fileSizeKey],
            options: [.skipsHiddenFiles]
        ) else { return 0 }

        var total: Int64 = 0
        for case let fileURL as URL in enumerator {
            let values = try fileURL.resourceValues(forKeys: [.isRegularFileKey,
                                                              .totalFileAllocatedSizeKey,
                                                              .fileSizeKey])
            guard values.isRegularFile == true else { continue }
            total += Int64(values.totalFileAllocatedSize ?? values.fileSize ?? 0)
        }
        return total
    }

    /// Runs `work` and returns the directory's size change in bytes.
    public static func sizeDelta(of url: URL,
                                 during work: () async throws -> Void) async throws -> Int64 {
        let before = try directorySize(at: url)
        try await work()
        return try directorySize(at: url) - before
    }

    /// Bytes available for "important" usage on the volume containing `url`.
    /// Sandbox-safe: reads a volume resource value, not directory contents.
    public static func volumeFreeSpace(at url: URL) throws -> Int64 {
        let values = try url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
        return Int64(values.volumeAvailableCapacityForImportantUsage ?? 0)
    }
}
