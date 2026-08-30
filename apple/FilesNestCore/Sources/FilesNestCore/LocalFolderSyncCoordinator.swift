import Foundation

public enum LocalFolderSyncError: Error, Equatable {
    case unavailableDestination
    case destinationChanged
    case unsafeDestination
}

/// Executes a local-folder reconciliation serially against one root acquired by
/// the composition root's security-scoped access session.
public struct LocalFolderSyncCoordinator: Sendable {
    private let library: any AssetLibrary
    private let writer: LocalFolderWriter
    private let root: URL
    private let bookmark: Data
    private let state: any SyncStateStore
    private let resolveBookmark: @Sendable (Data) -> URL?

    public init(library: any AssetLibrary, writer: LocalFolderWriter, root: URL,
                bookmark: Data, state: any SyncStateStore,
                resolveBookmark: @escaping @Sendable (Data) -> URL? = { resolveLocalFolder(bookmark: $0) }) {
        self.library = library
        self.writer = writer
        self.root = root
        self.bookmark = bookmark
        self.state = state
        self.resolveBookmark = resolveBookmark
    }

    public func sync(range: SyncRange,
                     onProgress: @escaping @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        try validateDestination()
        state.saveLastSyncStarted(Date())
        let resources = try await library.resources(in: range)
        var uploads: [AssetResource] = []
        for resource in resources {
            if !LocalFolderPlanner.isCompletedFile(at: LocalFolderPlanner.expectedPath(for: resource, destinationRoot: root)) {
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
            try validateDestination()
            do { try FileManager.default.removeItem(at: item.path); deleted.append(item.key) }
            catch { failed.append(FailedItem(key: item.key, filename: item.path.lastPathComponent, reason: String(describing: error), kind: .delete)) }
        }
        return SyncReport(uploaded: uploadResult.uploaded, deleted: deleted, failed: failed, skipped: resources.count - uploads.count)
    }

    public func resume(resources: [AssetResource],
                       onProgress: @escaping @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        guard let queuedBookmark = state.loadRemainingUploadsDestination(),
              queuedBookmark == bookmark || queuedBookmarkResolvesToCurrentRoot(queuedBookmark) else {
            throw LocalFolderSyncError.destinationChanged
        }
        try validateDestination()
        state.saveLastSyncStarted(Date())
        let result = try await run(resources, root: root, onProgress: onProgress)
        return SyncReport(uploaded: result.uploaded, deleted: [], failed: result.failed, skipped: 0)
    }

    private func queuedBookmarkResolvesToCurrentRoot(_ queuedBookmark: Data) -> Bool {
        guard let queuedRoot = resolveBookmark(queuedBookmark) else { return false }
        return queuedRoot.resolvingSymlinksInPath().standardizedFileURL
            == root.resolvingSymlinksInPath().standardizedFileURL
    }

    private func validateDestination() throws {
        guard FileManager.default.fileExists(atPath: root.path),
              let values = try? root.resourceValues(forKeys: [.isDirectoryKey, .isWritableKey]),
              values.isDirectory == true, values.isWritable == true else {
            throw LocalFolderSyncError.unavailableDestination
        }
    }

    private func run(_ resources: [AssetResource], root: URL,
                     onProgress: @escaping @Sendable (SyncProgress) -> Void) async throws -> (uploaded: [ResourceKey], failed: [FailedItem]) {
        var uploaded: [ResourceKey] = [], failed: [FailedItem] = []
        let session = state.remainingUploadsSession()
        func persist() {
            let completed = Set(uploaded.map(\.encoded))
            let remaining = resources.filter { !completed.contains($0.key.encoded) }
            state.saveRemainingUploads(remaining, destination: remaining.isEmpty ? nil : bookmark, session: session)
        }
        defer { persist() }
        for (index, resource) in resources.enumerated() {
            try Task.checkCancellation()
            try validateDestination()
            onProgress(SyncProgress(completed: index, total: resources.count, currentItemName: resource.filename, bytesRemaining: nil, currentItemID: resource.key.localIdentifier))
            do {
                let path = LocalFolderPlanner.expectedPath(for: resource, destinationRoot: root)
                try validateExpectedPath(path)
                try await writer.write(assetID: resource.key.encoded, destinationPath: path)
                uploaded.append(resource.key)
            }
            catch is CancellationError { throw CancellationError() }
            catch let error as LocalFolderSyncError where error == .unavailableDestination { throw error }
            catch { failed.append(FailedItem(key: resource.key, filename: resource.filename, reason: String(describing: error))) }
        }
        onProgress(SyncProgress(completed: resources.count, total: resources.count, currentItemName: nil, bytesRemaining: nil))
        return (uploaded, failed)
    }

    private func actualPaths(_ root: URL) throws -> Set<URL> {
        var result = Set<URL>()
        let fm = FileManager.default
        let years = try fm.contentsOfDirectory(at: root, includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey])
        for year in years where try isDirectory(year) && isNumericComponent(year.lastPathComponent, digits: 4) {
            let months = try fm.contentsOfDirectory(at: year, includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey])
            for month in months where try isDirectory(month) && isNumericComponent(month.lastPathComponent, digits: 2) {
                let days = try fm.contentsOfDirectory(at: month, includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey])
                for day in days where try isDirectory(day) && isNumericComponent(day.lastPathComponent, digits: 2) {
                    let files = try fm.contentsOfDirectory(at: day, includingPropertiesForKeys: [.isRegularFileKey, .isSymbolicLinkKey])
                    for file in files where LocalFolderPlanner.isCompletedFile(at: file) && LocalFolderPlanner.isManagedPath(file) {
                        result.insert(file)
                    }
                }
            }
        }
        return result
    }

    private func isDirectory(_ url: URL) throws -> Bool {
        let values = try url.resourceValues(forKeys: [.isDirectoryKey, .isSymbolicLinkKey])
        return values.isDirectory == true && values.isSymbolicLink != true
    }

    private func isNumericComponent(_ component: String, digits: Int) -> Bool {
        component.count == digits && component.allSatisfy(\.isNumber)
    }

    private func validateExpectedPath(_ path: URL) throws {
        let fm = FileManager.default
        var component = root
        for name in path.pathComponents.dropFirst(root.pathComponents.count) {
            component.appendPathComponent(name)
            guard fm.fileExists(atPath: component.path) else { break }
            if (try? fm.destinationOfSymbolicLink(atPath: component.path)) != nil {
                throw LocalFolderSyncError.unsafeDestination
            }
        }
    }
}
