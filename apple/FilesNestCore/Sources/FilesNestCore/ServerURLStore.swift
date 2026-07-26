import Foundation

public protocol ServerURLStore: Sendable {
    func load() -> URL?
    func save(_ url: URL)
}

/// Server URL is configuration, not a secret — stored in UserDefaults (inject a
/// suite in tests). An empty or malformed stored string loads as `nil`.
public final class UserDefaultsServerURLStore: ServerURLStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.serverURL"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func load() -> URL? {
        guard let s = defaults.string(forKey: key), !s.isEmpty else { return nil }
        return URL(string: s)
    }

    public func save(_ url: URL) { defaults.set(url.absoluteString, forKey: key) }
}
