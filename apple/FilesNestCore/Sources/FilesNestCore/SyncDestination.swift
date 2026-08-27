import Foundation

public enum SyncDestination: String, Sendable, CaseIterable {
    case server
    case localFolder
}

public protocol SyncDestinationStore: Sendable {
    func load() -> SyncDestination
    func save(_ destination: SyncDestination)
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
