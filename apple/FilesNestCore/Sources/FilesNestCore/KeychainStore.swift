import Foundation
import Security

/// Seam over the raw `SecItem*` C API so `KeychainStore`'s logic is unit-testable
/// against an in-memory fake. Dictionaries are built and consumed within a single
/// synchronous call and never cross an isolation boundary, so `[String: Any]` is sound.
public protocol KeychainBackend: Sendable {
    func add(_ query: [String: Any]) -> OSStatus
    func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?)
    func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus
    func delete(_ query: [String: Any]) -> OSStatus
}

/// The real backend: a logic-free forwarder to Security framework.
public struct SystemKeychainBackend: KeychainBackend {
    public init() {}

    public func add(_ query: [String: Any]) -> OSStatus {
        SecItemAdd(query as CFDictionary, nil)
    }

    public func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?) {
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        return (status, result)
    }

    public func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus {
        SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
    }

    public func delete(_ query: [String: Any]) -> OSStatus {
        SecItemDelete(query as CFDictionary)
    }
}

public enum KeychainStoreError: Error, Equatable {
    /// A `SecItem*` status we don't special-case; carries the exact `OSStatus`.
    case unexpectedStatus(OSStatus)
    /// The stored blob was present but not a decodable `BasicCredentials`.
    case decoding
}

/// `CredentialStore` conformance backed by the data-protection Keychain.
/// Stores exactly one Basic Auth credential, addressed by `service` + `account`.
public struct KeychainStore: CredentialStore {
    private let service: String
    private let account: String
    private let backend: KeychainBackend

    public init(
        service: String = "com.filesnest.credentials",
        account: String = "basic-auth",
        backend: KeychainBackend = SystemKeychainBackend()
    ) {
        self.service = service
        self.account = account
        self.backend = backend
    }

    /// The full credential tuple lives inside the encrypted `kSecValueData`, so the
    /// username never leaks into searchable Keychain metadata.
    private struct Stored: Codable {
        let username: String
        let password: String
    }

    private var baseQuery: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecUseDataProtectionKeychain as String: true,
        ]
    }

    public func basicCredentials() async throws -> BasicCredentials? {
        var query = baseQuery
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        let (status, item) = backend.copyMatching(query)
        switch status {
        case errSecSuccess:
            guard let data = item as? Data else { throw KeychainStoreError.decoding }
            return try decode(data)
        case errSecItemNotFound:
            return nil
        default:
            throw KeychainStoreError.unexpectedStatus(status)
        }
    }

    public func save(_ credentials: BasicCredentials) throws {
        let data = try JSONEncoder().encode(
            Stored(username: credentials.username, password: credentials.password))
        var addQuery = baseQuery
        addQuery[kSecValueData as String] = data
        addQuery[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let addStatus = backend.add(addQuery)
        switch addStatus {
        case errSecSuccess:
            return
        case errSecDuplicateItem:
            let updateStatus = backend.update(baseQuery, [kSecValueData as String: data])
            switch updateStatus {
            case errSecSuccess:
                return
            case errSecItemNotFound:
                // The item was deleted between the add and the update (a racing
                // caller). Honor the add-or-update contract by adding it now.
                let retryStatus = backend.add(addQuery)
                guard retryStatus == errSecSuccess else {
                    throw KeychainStoreError.unexpectedStatus(retryStatus)
                }
            default:
                throw KeychainStoreError.unexpectedStatus(updateStatus)
            }
        default:
            throw KeychainStoreError.unexpectedStatus(addStatus)
        }
    }

    public func clear() throws {
        let status = backend.delete(baseQuery)
        switch status {
        case errSecSuccess, errSecItemNotFound:
            return
        default:
            throw KeychainStoreError.unexpectedStatus(status)
        }
    }

    private func decode(_ data: Data) throws -> BasicCredentials {
        do {
            let stored = try JSONDecoder().decode(Stored.self, from: data)
            return BasicCredentials(username: stored.username, password: stored.password)
        } catch {
            throw KeychainStoreError.decoding
        }
    }
}
