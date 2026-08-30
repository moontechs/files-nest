import Foundation

public protocol LocalFolderStore: Sendable {
    func load() -> Data?
    func save(_ bookmark: Data)
    func clear()
}

/// A security-scoped bookmark is configuration identifying the user's chosen
/// destination, not a secret, so it is stored in UserDefaults.
public final class UserDefaultsLocalFolderStore: LocalFolderStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.localFolderBookmark"

    public init(defaults: UserDefaults) {
        self.defaults = defaults
    }

    public func load() -> Data? {
        guard let bookmark = defaults.data(forKey: key), !bookmark.isEmpty else {
            return nil
        }
        return bookmark
    }

    public func save(_ bookmark: Data) {
        defaults.set(bookmark, forKey: key)
    }

    public func clear() {
        defaults.removeObject(forKey: key)
    }
}

/// Resolves the selected folder and refreshes its bookmark when macOS marks it
/// stale. Keeping this operation in one place ensures readiness and syncing
/// use identical bookmark semantics.
public func resolveLocalFolder(store: LocalFolderStore) -> URL? {
    guard let bookmark = store.load(), !bookmark.isEmpty else { return nil }

    var isStale = false
    guard let url = try? URL(
        resolvingBookmarkData: bookmark,
        options: [.withSecurityScope],
        relativeTo: nil,
        bookmarkDataIsStale: &isStale
    ) else {
        return nil
    }

    if isStale,
       let refreshed = try? url.bookmarkData(
           options: [.withSecurityScope],
           includingResourceValuesForKeys: nil,
           relativeTo: nil
       ) {
        store.save(refreshed)
    }
    return url
}
