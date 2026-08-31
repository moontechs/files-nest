import Foundation

/// The one active place FilesNest is configured to send backups.
public enum SyncDestination: String, Sendable, CaseIterable {
    case server
    case localFolder
}

public protocol SyncDestinationStore: Sendable {
    func load() -> SyncDestination
    func save(_ destination: SyncDestination)
}

/// Destination choice is ordinary configuration, not a secret.
public final class UserDefaultsSyncDestinationStore: SyncDestinationStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.syncDestination"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func load() -> SyncDestination {
        guard let rawValue = defaults.string(forKey: key),
              let destination = SyncDestination(rawValue: rawValue) else {
            return .server
        }
        return destination
    }

    public func save(_ destination: SyncDestination) {
        defaults.set(destination.rawValue, forKey: key)
    }
}

/// Whether the saved resume list was created for the active destination.
/// Server lists use `nil`; local-folder lists retain their selected bookmark.
public func remainingUploadsBelong(
    to destination: SyncDestination,
    savedDestination: Data?,
    localFolderStore: any LocalFolderStore
) -> Bool {
    switch destination {
    case .server:
        return savedDestination == nil
    case .localFolder:
        guard let savedDestination, let current = localFolderStore.load() else { return false }
        if savedDestination == current { return true }
        guard let savedRoot = resolveLocalFolder(bookmark: savedDestination),
              let currentRoot = resolveLocalFolder(bookmark: current) else { return false }
        return savedRoot.resolvingSymlinksInPath().standardizedFileURL
            == currentRoot.resolvingSymlinksInPath().standardizedFileURL
    }
}

/// Returns whether the active destination has every value needed to start a sync.
public func isDestinationReady(
    _ destination: SyncDestination,
    urlStore: any ServerURLStore,
    credStore: any CredentialStore,
    localFolderStore: any LocalFolderStore
) async -> Bool {
    switch destination {
    case .server:
        guard urlStore.load() != nil else { return false }
        return (try? await credStore.basicCredentials()) != nil
    case .localFolder:
        guard let url = resolveLocalFolder(store: localFolderStore) else { return false }
        let accessing = url.startAccessingSecurityScopedResource()
        defer {
            if accessing { url.stopAccessingSecurityScopedResource() }
        }
        return FileManager.default.fileExists(atPath: url.path)
            && FileManager.default.isWritableFile(atPath: url.path)
    }
}
