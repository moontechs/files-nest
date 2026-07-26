import Foundation
import Security
import Testing
@testable import FilesNestCore

/// In-memory stand-in for the real Keychain, keyed by service|account.
/// Reproduces the SecItem status semantics KeychainStore branches on, and
/// asserts the store always sends the required query attributes so the fake
/// can't paper over a malformed real query.
///
/// All configuration is immutable (set at init), so the only shared mutable
/// state is `items`, guarded by `lock` — making the `@unchecked Sendable`
/// claim sound.
final class FakeKeychainBackend: KeychainBackend, @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String: Data] = [:]

    private let forcedAdd: OSStatus?
    private let forcedCopy: OSStatus?
    private let forcedUpdate: OSStatus?
    private let forcedDelete: OSStatus?
    /// When true, `update` deletes the keyed item and returns `errSecItemNotFound`,
    /// simulating a racing deleter between `add` (duplicate) and `update`.
    private let vanishOnUpdate: Bool

    /// Force every operation to the same status (error-path convenience).
    convenience init(forcedStatus: OSStatus? = nil) {
        self.init(addStatus: forcedStatus, copyStatus: forcedStatus,
                  updateStatus: forcedStatus, deleteStatus: forcedStatus)
    }

    init(addStatus: OSStatus? = nil, copyStatus: OSStatus? = nil,
         updateStatus: OSStatus? = nil, deleteStatus: OSStatus? = nil,
         vanishOnUpdate: Bool = false) {
        self.forcedAdd = addStatus
        self.forcedCopy = copyStatus
        self.forcedUpdate = updateStatus
        self.forcedDelete = deleteStatus
        self.vanishOnUpdate = vanishOnUpdate
    }

    /// Directly seed an item, bypassing the query-attribute assertions — used to
    /// simulate a pre-existing (possibly corrupt) Keychain item.
    func seed(service: String, account: String, data: Data) {
        lock.lock(); defer { lock.unlock() }
        items["\(service)|\(account)"] = data
    }

    private func key(_ query: [String: Any]) -> String {
        let service = query[kSecAttrService as String] as? String ?? ""
        let account = query[kSecAttrAccount as String] as? String ?? ""
        return "\(service)|\(account)"
    }

    private func requireGenericPassword(_ query: [String: Any]) {
        if query[kSecClass as String] as? String != (kSecClassGenericPassword as String) {
            Issue.record("Keychain query must set kSecClass = kSecClassGenericPassword")
        }
    }

    func add(_ query: [String: Any]) -> OSStatus {
        requireGenericPassword(query)
        lock.lock(); defer { lock.unlock() }
        if let forcedAdd { return forcedAdd }
        let k = key(query)
        if items[k] != nil { return errSecDuplicateItem }
        items[k] = query[kSecValueData as String] as? Data ?? Data()
        return errSecSuccess
    }

    func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?) {
        requireGenericPassword(query)
        if query[kSecReturnData as String] as? Bool != true {
            Issue.record("read query must set kSecReturnData = true")
        }
        if query[kSecMatchLimit as String] as? String != (kSecMatchLimitOne as String) {
            Issue.record("read query must set kSecMatchLimit = kSecMatchLimitOne")
        }
        lock.lock(); defer { lock.unlock() }
        if let forcedCopy { return (forcedCopy, nil) }
        guard let data = items[key(query)] else { return (errSecItemNotFound, nil) }
        return (errSecSuccess, data)
    }

    func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus {
        requireGenericPassword(query)
        lock.lock(); defer { lock.unlock() }
        if vanishOnUpdate {
            items.removeValue(forKey: key(query))
            return errSecItemNotFound
        }
        if let forcedUpdate { return forcedUpdate }
        let k = key(query)
        guard items[k] != nil else { return errSecItemNotFound }
        if let data = attributes[kSecValueData as String] as? Data { items[k] = data }
        return errSecSuccess
    }

    func delete(_ query: [String: Any]) -> OSStatus {
        requireGenericPassword(query)
        lock.lock(); defer { lock.unlock() }
        if let forcedDelete { return forcedDelete }
        guard items.removeValue(forKey: key(query)) != nil else { return errSecItemNotFound }
        return errSecSuccess
    }
}
