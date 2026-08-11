import Testing
import Foundation
@testable import FilesNestCore

struct ShellStoresTests {
    @Test func serverURLRoundTrips() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        let store = UserDefaultsServerURLStore(defaults: suite)
        #expect(store.load() == nil)
        store.save(URL(string: "https://nest.home.example")!)
        #expect(store.load() == URL(string: "https://nest.home.example")!)
    }

    @Test func serverURLNilWhenStoredValueInvalid() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        suite.set("", forKey: "com.filesnest.serverURL")
        let store = UserDefaultsServerURLStore(defaults: suite)
        #expect(store.load() == nil)
    }

    @Test func staticCredentialStoreReturnsValueThenNil() async throws {
        let creds = BasicCredentials(username: "u", password: "p")
        #expect(try await StaticCredentialStore(creds).basicCredentials() == creds)
        #expect(try await StaticCredentialStore(nil).basicCredentials() == nil)
    }
}
