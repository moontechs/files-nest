import Foundation

public enum LocalFolderWriterError: Error, Equatable {
    case insufficientFreeSpace(available: Int64, required: Int64)
    case unsafeDestination
}

/// Streams one resource into a local-folder destination using a same-directory
/// temporary file, so a failed write never exposes a partial final file.
public struct LocalFolderWriter: Sendable {
    private let source: any AssetDataSource
    private let minimumFreeSpace: Int64
    private let volumeFreeSpace: @Sendable (URL) throws -> Int64
    private let destinationRoot: URL?

    public init(source: any AssetDataSource,
                minimumFreeSpace: Int64 = 1 * 1024 * 1024,
                destinationRoot: URL? = nil,
                volumeFreeSpace: @escaping @Sendable (URL) throws -> Int64 = DiskProbe.volumeFreeSpace(at:)) {
        self.source = source
        self.minimumFreeSpace = minimumFreeSpace
        self.destinationRoot = destinationRoot
        self.volumeFreeSpace = volumeFreeSpace
    }

    public func write(assetID: String, destinationPath: URL) async throws {
        try validateDestinationPath(destinationPath)
        let available = try volumeFreeSpace(existingAncestor(of: destinationPath))
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

    private func existingAncestor(of url: URL) -> URL {
        let fm = FileManager.default
        var candidate = url.deletingLastPathComponent()
        while !fm.fileExists(atPath: candidate.path) {
            let parent = candidate.deletingLastPathComponent()
            guard parent.path != candidate.path else { break }
            candidate = parent
        }
        return candidate
    }

    private func validateDestinationPath(_ destinationPath: URL) throws {
        guard let destinationRoot else { return }
        let rootPath = destinationRoot.standardizedFileURL.path
        let destinationPath = destinationPath.standardizedFileURL.path
        guard destinationPath.hasPrefix(rootPath + "/") else {
            throw LocalFolderWriterError.unsafeDestination
        }

        let fm = FileManager.default
        var component = destinationRoot
        for name in URL(fileURLWithPath: destinationPath).pathComponents.dropFirst(destinationRoot.pathComponents.count) {
            component.appendPathComponent(name)
            guard fm.fileExists(atPath: component.path) else { break }
            if (try? fm.destinationOfSymbolicLink(atPath: component.path)) != nil {
                throw LocalFolderWriterError.unsafeDestination
            }
        }
    }
}
