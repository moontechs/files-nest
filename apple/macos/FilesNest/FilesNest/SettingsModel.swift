import SwiftUI
import Combine
import FilesNestCore

protocol CredentialSavingStore: CredentialStore {
    func save(_ credentials: BasicCredentials) throws
}

extension KeychainStore: CredentialSavingStore {}

protocol ConnectionProbing: Sendable {
    func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult
}

extension ConnectionProbe: ConnectionProbing {}

@MainActor
final class SettingsModel: ObservableObject {
    @Published var serverURL = "" { didSet { markDraftAsEdited() } }
    @Published var username = "" { didSet { markDraftAsEdited() } }
    @Published var password = "" { didSet { markDraftAsEdited() } }
    @Published var destination: SyncDestination = .server {
        didSet {
            guard destination != oldValue else { return }
            destinationStore.save(destination)
            markDraftAsEdited()
            onSaved?()
        }
    }
    @Published var testResult: ConnectionResult?
    @Published var isConnecting = false
    @Published var saveError: String?

    private let urlStore: any ServerURLStore
    private let credStore: any CredentialSavingStore
    private let probe: any ConnectionProbing
    private let destinationStore: any SyncDestinationStore
    private var hasLoadedInitialValues = false
    private var hasDraftEdits = false
    private var isApplyingInitialValues = false
    var onSaved: (() -> Void)?

    init(urlStore: any ServerURLStore, credStore: any CredentialSavingStore, probe: any ConnectionProbing,
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
              url.host != nil,
              url.user == nil,
              url.password == nil else {
            saveError = "Enter a valid server URL."
            return
        }
        let credentials = BasicCredentials(username: username, password: password)
        isConnecting = true
        defer { isConnecting = false }
        let result = await probe.probe(baseURL: url, credentials: credentials)
        testResult = result
        guard result == .ok else { return }
        do {
            try credStore.save(credentials)
        } catch {
            saveError = "Couldn't save credentials to the keychain: \(error)"
            return
        }
        urlStore.save(url)
        saveError = nil
        onSaved?()
    }
}
