import Foundation
@testable import FilesNestCore

/// In-memory stand-in for the real Keychain, keyed by service|account.
/// Reproduces the SecItem status semantics KeychainStore branches on.
final class FakeKeychainBackend: KeychainBackend, @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String: Data] = [:]
    /// When set, every operation returns this status (drives error-path tests).
    var forcedStatus: OSStatus?

    init(forcedStatus: OSStatus? = nil) { self.forcedStatus = forcedStatus }

    private func key(_ query: [String: Any]) -> String {
        let service = query[kSecAttrService as String] as? String ?? ""
        let account = query[kSecAttrAccount as String] as? String ?? ""
        return "\(service)|\(account)"
    }

    func add(_ query: [String: Any]) -> OSStatus {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return forcedStatus }
        let k = key(query)
        if items[k] != nil { return errSecDuplicateItem }
        items[k] = query[kSecValueData as String] as? Data ?? Data()
        return errSecSuccess
    }

    func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?) {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return (forcedStatus, nil) }
        guard let data = items[key(query)] else { return (errSecItemNotFound, nil) }
        return (errSecSuccess, data)
    }

    func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return forcedStatus }
        let k = key(query)
        guard items[k] != nil else { return errSecItemNotFound }
        if let data = attributes[kSecValueData as String] as? Data { items[k] = data }
        return errSecSuccess
    }

    func delete(_ query: [String: Any]) -> OSStatus {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return forcedStatus }
        guard items.removeValue(forKey: key(query)) != nil else { return errSecItemNotFound }
        return errSecSuccess
    }
}
