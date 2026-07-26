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
}
