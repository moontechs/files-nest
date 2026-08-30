import Foundation

public enum LocalFolderWriterError: Error, Equatable {
    case insufficientFreeSpace(available: Int64, required: Int64)
}

/// Streams one resource into a local-folder destination using a same-directory
/// temporary file, so a failed write never exposes a partial final file.
public struct LocalFolderWriter: Sendable {
    private let source: any AssetDataSource
    private let minimumFreeSpace: Int64
    private let volumeFreeSpace: @Sendable (URL) throws -> Int64

    public init(source: any AssetDataSource,
                minimumFreeSpace: Int64 = 1 * 1024 * 1024,
                volumeFreeSpace: @escaping @Sendable (URL) throws -> Int64 = DiskProbe.volumeFreeSpace(at:)) {
        self.source = source
        self.minimumFreeSpace = minimumFreeSpace
        self.volumeFreeSpace = volumeFreeSpace
    }

    public func write(assetID: String, destinationPath: URL) async throws {
        let available = try volumeFreeSpace(destinationPath)
        guard available >= minimumFreeSpace else {
            throw LocalFolderWriterError.insufficientFreeSpace(
                available: available, required: minimumFreeSpace)
        }

        let fm = FileManager.default
        let directory = destinationPath.deletingLastPathComponent()
        try fm.createDirectory(at: directory, withIntermediateDirectories: true)
        let temporary = directory.appendingPathComponent(".filesnest-" + UUID().uuidString + ".tmp")
        fm.createFile(atPath: temporary.path, contents: nil)
        var completed = false
        defer {
            if !completed { try? fm.removeItem(at: temporary) }
        }

        let handle = try FileHandle(forWritingTo: temporary)
        do {
            try await source.read(assetID: assetID, from: 0) { blob in
                try Task.checkCancellation()
                try handle.write(contentsOf: blob)
            }
            try handle.close()
            if fm.fileExists(atPath: destinationPath.path) {
                _ = try fm.replaceItemAt(destinationPath, withItemAt: temporary)
            } else {
                try fm.moveItem(at: temporary, to: destinationPath)
            }
            completed = true
        } catch {
            try? handle.close()
            throw error
        }
    }
}
