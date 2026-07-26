import Testing
import Foundation
@testable import FilesNestCore

struct KeychainStoreTests {
    @Test func saveThenReadRoundTripsExactCredentials() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        try store.save(BasicCredentials(username: "alice", password: "s3cr3t:with/odd\"chars"))
        #expect(try await store.basicCredentials()
                == BasicCredentials(username: "alice", password: "s3cr3t:with/odd\"chars"))
    }

    @Test func saveTwiceUpdatesInPlaceSecondValueWins() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        try store.save(BasicCredentials(username: "alice", password: "first"))
        try store.save(BasicCredentials(username: "alice", password: "second"))
        #expect(try await store.basicCredentials()
                == BasicCredentials(username: "alice", password: "second"))
    }

    @Test func readOnEmptyReturnsNil() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        #expect(try await store.basicCredentials() == nil)
    }

    @Test func clearRemovesItemAndIsIdempotent() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        try store.save(BasicCredentials(username: "alice", password: "pw"))
        try store.clear()
        #expect(try await store.basicCredentials() == nil)
        try store.clear() // second clear on empty must not throw
    }

    @Test func unexpectedStatusOnSaveThrowsMappedError() {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend(forcedStatus: errSecIO))
        #expect(throws: KeychainStoreError.unexpectedStatus(errSecIO)) {
            try store.save(BasicCredentials(username: "a", password: "b"))
        }
    }

    @Test func unexpectedStatusOnReadThrowsMappedError() async {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend(forcedStatus: errSecIO))
        await #expect(throws: KeychainStoreError.unexpectedStatus(errSecIO)) {
            _ = try await store.basicCredentials()
        }
    }

    @Test func corruptStoredBytesThrowDecoding() async {
        // Seed the backend directly with non-JSON bytes under the store's key.
        let service = "kc.test.\(UUID().uuidString)"
        let backend = FakeKeychainBackend()
        _ = backend.add([
            kSecAttrService as String: service,
            kSecAttrAccount as String: "basic-auth",
            kSecValueData as String: Data([0x00, 0x01, 0x02]),
        ])
        let store = KeychainStore(service: service, backend: backend)
        await #expect(throws: KeychainStoreError.decoding) {
            _ = try await store.basicCredentials()
        }
    }
}
