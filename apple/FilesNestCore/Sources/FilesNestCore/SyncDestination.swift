import Foundation

public enum SyncDestination: String, Sendable, CaseIterable {
    case server
    case localFolder
}

public protocol SyncDestinationStore: Sendable {
    func load() -> SyncDestination
    func save(_ destination: SyncDestination)
}

public struct ServerDestinationConfiguration: Sendable {
    public let url: URL
    public let credentials: BasicCredentials
}

/// Reads one usable server configuration. The URL is read only after the credential
/// lookup completes, so an in-progress Settings update cannot pair newly saved
/// credentials with the URL that preceded them. The destination is checked before and
/// after the credential read so a switch to an unavailable destination cannot yield a
/// server configuration while the Keychain call is suspended.
public func configuredServerDestination(
    destinationStore: any SyncDestinationStore,
    urlStore: any ServerURLStore,
    credStore: any CredentialStore
) async -> ServerDestinationConfiguration? {
    guard destinationStore.load() == .server else { return nil }

    do {
        guard let credentials = try await credStore.basicCredentials(),
              destinationStore.load() == .server,
              let url = urlStore.load() else { return nil }
        return ServerDestinationConfiguration(url: url, credentials: credentials)
    } catch {
        return nil
    }
}

public func isDestinationReady(
    _ destination: SyncDestination,
    urlStore: any ServerURLStore,
    credStore: any CredentialStore
) async -> Bool {
    guard destination == .server, urlStore.load() != nil else { return false }

    do {
        return try await credStore.basicCredentials() != nil
    } catch {
        return false
    }
}

public final class UserDefaultsSyncDestinationStore: SyncDestinationStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.syncDestination"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func load() -> SyncDestination {
        guard let rawValue = defaults.string(forKey: key) else { return .server }
        return SyncDestination(rawValue: rawValue) ?? .server
    }

    public func save(_ destination: SyncDestination) {
        defaults.set(destination.rawValue, forKey: key)
    }
}
