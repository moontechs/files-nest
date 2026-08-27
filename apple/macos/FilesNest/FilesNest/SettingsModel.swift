import SwiftUI
import Combine
import FilesNestCore

@MainActor
final class SettingsModel: ObservableObject {
    @Published var serverURL = "" { didSet { markDraftAsEdited() } }
    @Published var username = "" { didSet { markDraftAsEdited() } }
    @Published var password = "" { didSet { markDraftAsEdited() } }
    @Published var destination: SyncDestination = .server {
        didSet {
            destinationStore.save(destination)
            markDraftAsEdited()
        }
    }
    @Published var testResult: ConnectionResult?
    @Published var isConnecting = false
    @Published var saveError: String?

    private let urlStore: any ServerURLStore
    private let credStore: KeychainStore
    private let probe: ConnectionProbe
    private let destinationStore: any SyncDestinationStore
    private var hasLoadedInitialValues = false
    private var hasDraftEdits = false
    private var isApplyingInitialValues = false
    var onSaved: (() -> Void)?

    init(urlStore: any ServerURLStore, credStore: KeychainStore, probe: ConnectionProbe,
         destinationStore: any SyncDestinationStore) {
        self.urlStore = urlStore
        self.credStore = credStore
        self.probe = probe
        self.destinationStore = destinationStore
        self.destination = destinationStore.load()
    }

    var hasCredentials: Bool { !username.isEmpty && !password.isEmpty }

    func load() async {
        // SettingsView is recreated whenever the menu-bar panel is dismissed and
        // opened again. Load persisted values only once; subsequent appearances
        // must keep the in-memory draft, including pasted credentials.
        guard !hasLoadedInitialValues else { return }
        hasLoadedInitialValues = true

        let savedURL = urlStore.load()?.absoluteString
        let savedCredentials = try? await credStore.basicCredentials()
        let savedDestination = destinationStore.load()

        // Keychain access awaits. If the user starts typing before it completes,
        // do not overwrite their draft with the older persisted configuration.
        guard !hasDraftEdits else { return }
        isApplyingInitialValues = true
        destination = savedDestination
        if let savedURL { serverURL = savedURL }
        if let savedCredentials {
            username = savedCredentials.username
            password = savedCredentials.password
        }
        isApplyingInitialValues = false
    }

    private func markDraftAsEdited() {
        guard !isApplyingInitialValues else { return }
        hasDraftEdits = true
    }

    func connect() async {
        guard !isConnecting else { return }
        guard let url = URL(string: serverURL),
              !serverURL.isEmpty,
              url.scheme != nil,
              url.host != nil else {
            saveError = "Enter a valid server URL."
            return
        }
        isConnecting = true
        defer { isConnecting = false }
        let result = await probe.probe(baseURL: url,
                                       credentials: .init(username: username, password: password))
        testResult = result
        guard result == .ok else { return }
        do {
            try credStore.save(.init(username: username, password: password))
        } catch {
            saveError = "Couldn't save credentials to the keychain: \(error)"
            return
        }
        urlStore.save(url)
        saveError = nil
        onSaved?()
    }
}
