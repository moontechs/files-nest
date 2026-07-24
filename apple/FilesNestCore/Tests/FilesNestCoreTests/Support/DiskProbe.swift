import Foundation

/// Measures the byte-size delta of a directory across a unit of work.
///
/// In this slice it is exercised only against temp directories. Pointing it at
/// the Photos library container to answer "does PhotoKit stream or materialize
/// first?" belongs to the adapter slice, which needs a real library and TCC
/// consent. This slice builds the instrument; it does not produce iCloud numbers.
enum DiskProbe {

    /// Total allocated size of all regular files under `url`, recursively.
    static func directorySize(at url: URL) throws -> Int64 {
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
    static func sizeDelta(of url: URL,
                          during work: () async throws -> Void) async throws -> Int64 {
        let before = try directorySize(at: url)
        try await work()
        return try directorySize(at: url) - before
    }
}
