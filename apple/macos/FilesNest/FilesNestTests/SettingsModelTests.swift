import Testing
import Foundation
import FilesNestCore
@testable import FilesNest

@MainActor
struct SettingsModelTests {
    @Test func loadsDestinationSynchronouslyDuringInitialization() {
        let destinationStore = TestDestinationStore(destination: .localFolder)
        let model = SettingsModel(urlStore: TestURLStore(),
                                  credStore: KeychainStore(),
                                  probe: ConnectionProbe(),
                                  destinationStore: destinationStore)

        #expect(model.destination == .localFolder)
    }

    @Test func changingDestinationPersistsImmediately() {
        let destinationStore = TestDestinationStore()
        let model = SettingsModel(urlStore: TestURLStore(),
                                  credStore: KeychainStore(),
                                  probe: ConnectionProbe(),
                                  destinationStore: destinationStore)

        var reconciliations = 0
        model.onSaved = { reconciliations += 1 }
        model.destination = .localFolder

        #expect(destinationStore.saved == [.localFolder])
        #expect(reconciliations == 1)
    }

    @Test func connectWithInvalidURLDoesNotProbe() async {
        let model = SettingsModel(urlStore: TestURLStore(),
                                  credStore: TestCredentialStore(),
                                  probe: TestProbe(result: .ok),
                                  destinationStore: TestDestinationStore())
        model.serverURL = "not a URL"
        await model.connect()

        #expect(model.saveError == "Enter a valid server URL.")
        #expect(!model.isConnecting)
    }

    @Test func connectPersistsOnlyAfterASuccessfulProbe() async {
        let urlStore = TestURLStore(savedURL: URL(string: "https://previous.example"))
        let credentials = TestCredentialStore(saved: .init(username: "old", password: "old-secret"))
        let model = SettingsModel(urlStore: urlStore, credStore: credentials,
                                  probe: TestProbe(result: .ok), destinationStore: TestDestinationStore())
        model.serverURL = "https://nest.example"
        model.username = "alice"
        model.password = "secret"

        await model.connect()

        #expect(urlStore.savedURL == URL(string: "https://nest.example"))
        #expect(credentials.saved == BasicCredentials(username: "alice", password: "secret"))
        #expect(model.testResult == .ok)
    }

    @Test func failedConnectLeavesStoredConfigurationUntouched() async {
        let urlStore = TestURLStore(savedURL: URL(string: "https://previous.example"))
        let credentials = TestCredentialStore(saved: .init(username: "old", password: "old-secret"))
        let model = SettingsModel(urlStore: urlStore, credStore: credentials,
                                  probe: TestProbe(result: .unauthorized), destinationStore: TestDestinationStore())
        model.serverURL = "https://nest.example"
        model.username = "alice"
        model.password = "secret"

        await model.connect()

        #expect(urlStore.savedURL == URL(string: "https://previous.example"))
        #expect(credentials.saved == BasicCredentials(username: "old", password: "old-secret"))
        #expect(model.testResult == .unauthorized)
    }

    @Test func keychainFailureDoesNotPersistURL() async {
        let urlStore = TestURLStore()
        let model = SettingsModel(urlStore: urlStore, credStore: TestCredentialStore(shouldFailSaving: true),
                                  probe: TestProbe(result: .ok), destinationStore: TestDestinationStore())
        model.serverURL = "https://nest.example"
        model.username = "alice"
        model.password = "secret"

        await model.connect()

        #expect(urlStore.savedURL == nil)
        #expect(model.saveError != nil)
    }

    @Test func concurrentConnectsStartOnlyOneProbe() async {
        let probe = DelayedProbe(result: .ok)
        let model = SettingsModel(urlStore: TestURLStore(), credStore: TestCredentialStore(),
                                  probe: probe, destinationStore: TestDestinationStore())
        model.serverURL = "https://nest.example"
        model.username = "alice"
        model.password = "secret"

        let first = Task { @MainActor in await model.connect() }
        let second = Task { @MainActor in await model.connect() }
        await first.value
        await second.value

        #expect(probe.callCount == 1)
    }
}

private final class TestDestinationStore: SyncDestinationStore, @unchecked Sendable {
    var destination: SyncDestination
    var saved: [SyncDestination] = []

    init(destination: SyncDestination = .server) {
        self.destination = destination
    }

    func load() -> SyncDestination { destination }

    func save(_ destination: SyncDestination) {
        self.destination = destination
        saved.append(destination)
    }
}

private final class TestURLStore: ServerURLStore, @unchecked Sendable {
    var savedURL: URL?
    init(savedURL: URL? = nil) { self.savedURL = savedURL }
    func load() -> URL? { savedURL }
    func save(_ url: URL) { savedURL = url }
}

private final class TestCredentialStore: CredentialSavingStore, @unchecked Sendable {
    var saved: BasicCredentials?
    let shouldFailSaving: Bool

    init(saved: BasicCredentials? = nil, shouldFailSaving: Bool = false) {
        self.saved = saved
        self.shouldFailSaving = shouldFailSaving
    }

    func basicCredentials() async throws -> BasicCredentials? { saved }

    func save(_ credentials: BasicCredentials) throws {
        if shouldFailSaving { throw TestError.saveFailed }
        saved = credentials
    }
}

private struct TestProbe: ConnectionProbing {
    let result: ConnectionResult
    func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult { result }
}

private final class DelayedProbe: ConnectionProbing, @unchecked Sendable {
    private let lock = NSLock()
    private let result: ConnectionResult
    private var calls = 0

    init(result: ConnectionResult) { self.result = result }

    var callCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return calls
    }

    func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult {
        lock.lock()
        calls += 1
        lock.unlock()
        try? await Task.sleep(for: .milliseconds(50))
        return result
    }
}

private enum TestError: Error {
    case saveFailed
}
