import Testing
import Foundation
@testable import FilesNestCore

struct ShellStoresTests {
    @Test func syncDestinationDefaultsToServer() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        let store = UserDefaultsSyncDestinationStore(defaults: suite)
        #expect(store.load() == .server)
    }

    @Test func syncDestinationRoundTrips() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        let store = UserDefaultsSyncDestinationStore(defaults: suite)
        store.save(.localFolder)
        #expect(store.load() == .localFolder)
        store.save(.server)
        #expect(store.load() == .server)
    }

    @Test func syncDestinationDefaultsToServerForUnknownStoredValue() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        suite.set("unknown", forKey: "com.filesnest.syncDestination")
        let store = UserDefaultsSyncDestinationStore(defaults: suite)
        #expect(store.load() == .server)
    }

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
