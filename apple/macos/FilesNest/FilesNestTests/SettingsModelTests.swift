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

        model.destination = .localFolder

        #expect(destinationStore.saved == [.localFolder])
    }

    @Test func connectWithInvalidURLDoesNotProbe() async {
        let model = SettingsModel(urlStore: TestURLStore(),
                                  credStore: KeychainStore(),
                                  probe: ConnectionProbe(),
                                  destinationStore: TestDestinationStore())
        model.serverURL = "not a URL"
        await model.connect()

        #expect(model.saveError == "Enter a valid server URL.")
        #expect(!model.isConnecting)
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

private struct TestURLStore: ServerURLStore, Sendable {
    func load() -> URL? { nil }
    func save(_ url: URL) {}
}
