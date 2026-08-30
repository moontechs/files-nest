import Foundation

public enum LocalFolderSyncError: Error, Equatable {
    case unavailableDestination
    case destinationChanged
}

/// Executes a local-folder reconciliation serially. The bookmark is resolved for
/// each operation so Settings changes take effect on the next cycle.
public struct LocalFolderSyncCoordinator: Sendable {
    private let library: any AssetLibrary
    private let writer: LocalFolderWriter
    private let store: any LocalFolderStore
    private let originalBookmark: Data?

    public init(library: any AssetLibrary, writer: LocalFolderWriter, store: any LocalFolderStore) {
        self.library = library
        self.writer = writer
        self.store = store
        self.originalBookmark = store.load()
    }

    public func sync(range: SyncRange,
                     onProgress: @escaping @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        let root = try destinationRoot()
        let accessed = root.startAccessingSecurityScopedResource()
        defer { if accessed { root.stopAccessingSecurityScopedResource() } }
        let resources = try await library.resources(in: range)
        var uploads: [AssetResource] = []
        for resource in resources {
            if !FileManager.default.fileExists(atPath: LocalFolderPlanner.expectedPath(for: resource, destinationRoot: root).path) {
                uploads.append(resource)
            }
        }
        let deletes: [(path: URL, key: ResourceKey)]
        if case .all = range {
            deletes = LocalFolderPlanner.planDeletes(library: resources, actualPaths: try actualPaths(root), destinationRoot: root)
        } else { deletes = [] }
        let uploadResult = try await run(uploads, root: root, onProgress: onProgress)
        var failed = uploadResult.failed
        var deleted: [ResourceKey] = []
        for item in deletes {
            try Task.checkCancellation()
            do { try FileManager.default.removeItem(at: item.path); deleted.append(item.key) }
            catch { failed.append(FailedItem(key: item.key, filename: item.path.lastPathComponent, reason: String(describing: error), kind: .delete)) }
        }
        return SyncReport(uploaded: uploadResult.uploaded, deleted: deleted, failed: failed, skipped: resources.count - uploads.count)
    }

    public func resume(resources: [AssetResource],
                       onProgress: @escaping @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        guard store.load() == originalBookmark else { throw LocalFolderSyncError.destinationChanged }
        let root = try destinationRoot()
        let accessed = root.startAccessingSecurityScopedResource()
        defer { if accessed { root.stopAccessingSecurityScopedResource() } }
        let result = try await run(resources, root: root, onProgress: onProgress)
        return SyncReport(uploaded: result.uploaded, deleted: [], failed: result.failed, skipped: 0)
    }

    private func destinationRoot() throws -> URL {
        guard let root = resolveLocalFolder(store: store), FileManager.default.fileExists(atPath: root.path),
              let values = try? root.resourceValues(forKeys: [.isDirectoryKey, .isWritableKey]),
              values.isDirectory == true, values.isWritable == true else {
            throw LocalFolderSyncError.unavailableDestination
        }
        return root
    }

    private func run(_ resources: [AssetResource], root: URL,
                     onProgress: @escaping @Sendable (SyncProgress) -> Void) async throws -> (uploaded: [ResourceKey], failed: [FailedItem]) {
        var uploaded: [ResourceKey] = [], failed: [FailedItem] = []
        for (index, resource) in resources.enumerated() {
            try Task.checkCancellation()
            onProgress(SyncProgress(completed: index, total: resources.count, currentItemName: resource.filename, bytesRemaining: nil, currentItemID: resource.key.localIdentifier))
            do { try await writer.write(assetID: resource.key.encoded, destinationPath: LocalFolderPlanner.expectedPath(for: resource, destinationRoot: root)); uploaded.append(resource.key) }
            catch is CancellationError { throw CancellationError() }
            catch { failed.append(FailedItem(key: resource.key, filename: resource.filename, reason: String(describing: error))) }
        }
        onProgress(SyncProgress(completed: resources.count, total: resources.count, currentItemName: nil, bytesRemaining: nil))
        return (uploaded, failed)
    }

    private func actualPaths(_ root: URL) throws -> Set<URL> {
        var result = Set<URL>()
        let fm = FileManager.default
        guard let years = try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: [.isDirectoryKey]) else { return result }
        for year in years where year.lastPathComponent.count == 4 {
            for month in (try? fm.contentsOfDirectory(at: year, includingPropertiesForKeys: [.isDirectoryKey])) ?? [] {
                for day in (try? fm.contentsOfDirectory(at: month, includingPropertiesForKeys: [.isDirectoryKey])) ?? [] {
                    for file in (try? fm.contentsOfDirectory(at: day, includingPropertiesForKeys: [.isRegularFileKey])) ?? [] { result.insert(file) }
                }
            }
        }
        return result
    }
}
