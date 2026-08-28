import Foundation
import Testing
@testable import FilesNest
import FilesNestCore

@Suite(.serialized)
@MainActor
struct SettingsModelTests {
    @Test func destinationLoadsImmediatelyAndPersistsChanges() {
        let destinationStore = InMemoryDestinationStore(destination: .localFolder)
        let model = makeModel(destinationStore: destinationStore)

        #expect(model.destination == .localFolder)
        model.destination = .server
        #expect(destinationStore.saved == [.server])
    }

    @Test func assigningTheCurrentDestinationDoesNotDiscardPersistedValues() async {
        let url = URL(string: "https://nest.home.example")!
        let destinationStore = InMemoryDestinationStore(destination: .server)
        let model = makeModel(urlStore: InMemoryURLStore(value: url),
                              destinationStore: destinationStore)

        model.destination = .server
        await model.load()

        #expect(model.serverURL == url.absoluteString)
        #expect(destinationStore.saved.isEmpty)
    }

    @Test func assigningInitialTextFieldValuesDoesNotDiscardPersistedValues() async {
        let url = URL(string: "https://nest.home.example")!
        let model = makeModel(urlStore: InMemoryURLStore(value: url))

        model.serverURL = ""
        model.username = ""
        model.password = ""
        await model.load()

        #expect(model.serverURL == url.absoluteString)
    }

    @Test func loadShowsStoredServerURLWithoutCredentials() async {
        let url = URL(string: "https://nest.home.example")!
        let model = makeModel(urlStore: InMemoryURLStore(value: url))

        await model.load()

        #expect(model.serverURL == url.absoluteString)
        #expect(model.username.isEmpty)
        #expect(model.password.isEmpty)
    }

    @Test func connectPersistsOnlyAfterSuccessfulProbe() async {
        ProbeURLProtocol.statusCode = 200
        let credentials = InMemoryCredentialStore()
        let urlStore = InMemoryURLStore()
        let model = makeModel(urlStore: urlStore, credentials: credentials)
        model.serverURL = "https://settings-model.test"
        model.username = "nest"
        model.password = "secret"
        var saved = false
        model.onSaved = { saved = true }

        await model.connect()

        #expect(urlStore.value == URL(string: "https://settings-model.test"))
        #expect(credentials.value == BasicCredentials(username: "nest", password: "secret"))
        #expect(saved)
    }

    @Test func failedConnectLeavesExistingConfigurationUntouched() async {
        ProbeURLProtocol.statusCode = 401
        let existingURL = URL(string: "https://existing.test")!
        let existingCredentials = BasicCredentials(username: "old", password: "old-secret")
        let credentials = InMemoryCredentialStore(value: existingCredentials)
        let urlStore = InMemoryURLStore(value: existingURL)
        let model = makeModel(urlStore: urlStore, credentials: credentials)
        model.serverURL = "https://settings-model.test"
        model.username = "new"
        model.password = "new-secret"

        await model.connect()

        #expect(urlStore.value == existingURL)
        #expect(credentials.value == existingCredentials)
        #expect(model.testResult == .unauthorized)
    }

    @Test func invalidURLReportsErrorWithoutSaving() async {
        let credentials = InMemoryCredentialStore()
        let urlStore = InMemoryURLStore()
        let model = makeModel(urlStore: urlStore, credentials: credentials)
        model.serverURL = "not a URL"
        model.username = "nest"
        model.password = "secret"

        await model.connect()

        #expect(model.saveError == "Enter a valid server URL.")
        #expect(urlStore.value == nil)
        #expect(credentials.value == nil)
    }

    private func makeModel(
        urlStore: InMemoryURLStore = InMemoryURLStore(),
        credentials: InMemoryCredentialStore = InMemoryCredentialStore(),
        destinationStore: InMemoryDestinationStore = InMemoryDestinationStore()
    ) -> SettingsModel {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ProbeURLProtocol.self]
        return SettingsModel(urlStore: urlStore, credStore: credentials,
                             destinationStore: destinationStore,
                             probe: ConnectionProbe(session: URLSession(configuration: config)))
    }
}

private final class InMemoryURLStore: ServerURLStore, @unchecked Sendable {
    var value: URL?
    init(value: URL? = nil) { self.value = value }
    func load() -> URL? { value }
    func save(_ url: URL) { value = url }
}

private final class InMemoryCredentialStore: CredentialSavingStore, @unchecked Sendable {
    var value: BasicCredentials?
    init(value: BasicCredentials? = nil) { self.value = value }
    func basicCredentials() async throws -> BasicCredentials? { value }
    func save(_ credentials: BasicCredentials) throws { value = credentials }
}

private final class InMemoryDestinationStore: SyncDestinationStore, @unchecked Sendable {
    var value: SyncDestination
    var saved: [SyncDestination] = []
    init(destination: SyncDestination = .server) { value = destination }
    func load() -> SyncDestination { value }
    func save(_ destination: SyncDestination) { value = destination; saved.append(destination) }
}

private final class ProbeURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var statusCode = 200

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let response = HTTPURLResponse(url: request.url!, statusCode: Self.statusCode,
                                       httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        if Self.statusCode == 200 {
            client?.urlProtocol(self, didLoad: Data(#"{"items":[],"next_cursor":""}"#.utf8))
        }
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
