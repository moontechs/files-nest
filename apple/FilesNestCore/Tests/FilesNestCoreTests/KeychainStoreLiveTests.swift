import Testing
import Foundation
import Security
@testable import FilesNestCore

/// Exercises the real Security-framework backend end to end. In an unentitled
/// headless environment (CI `swift test`), the Keychain rejects the write with an
/// entitlement/interaction status; we treat that as "skip", not "fail", so the
/// path is still verified on an entitled/dev machine.
struct KeychainStoreLiveTests {
    @Test func liveSaveReadClearRoundTrip() async throws {
        let service = "kc.live.\(UUID().uuidString)"
        let store = KeychainStore(service: service) // default SystemKeychainBackend
        let creds = BasicCredentials(username: "live-user", password: "live-pass")

        do {
            try store.save(creds)
        } catch let KeychainStoreError.unexpectedStatus(status)
            where status == errSecMissingEntitlement || status == errSecInteractionNotAllowed {
            // Unentitled runner (e.g. headless CI) — skip cleanly.
            return
        }

        // Ensure we always clean up the real Keychain item.
        defer { try? store.clear() }

        #expect(try await store.basicCredentials() == creds)
        try store.clear()
        #expect(try await store.basicCredentials() == nil)
    }
}
