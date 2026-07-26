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
}
