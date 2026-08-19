import SwiftUI
import Combine
import FilesNestCore

@MainActor
final class SettingsModel: ObservableObject {
    @Published var serverURL = "" { didSet { markDraftAsEdited() } }
    @Published var username = "" { didSet { markDraftAsEdited() } }
    @Published var password = "" { didSet { markDraftAsEdited() } }
    @Published var testResult: ConnectionResult?
    @Published var isTesting = false
    @Published var saveError: String?

    private let urlStore: any ServerURLStore
    private let credStore: KeychainStore
    private let probe: ConnectionProbe
    private var hasLoadedInitialValues = false
    private var hasDraftEdits = false
    private var isApplyingInitialValues = false
    var onSaved: (() -> Void)?

    init(urlStore: any ServerURLStore, credStore: KeychainStore, probe: ConnectionProbe) {
        self.urlStore = urlStore
        self.credStore = credStore
        self.probe = probe
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

        // Keychain access awaits. If the user starts typing before it completes,
        // do not overwrite their draft with the older persisted configuration.
        guard !hasDraftEdits else { return }
        isApplyingInitialValues = true
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

    func test() async {
        guard let url = URL(string: serverURL), !serverURL.isEmpty else {
            testResult = .unreachable("Invalid URL"); return
        }
        isTesting = true; defer { isTesting = false }
        testResult = await probe.probe(baseURL: url,
                                       credentials: .init(username: username, password: password))
    }

    /// Persists URL + credentials. Returns `false` (and sets `saveError`) if the
    /// credential write fails, so the caller can keep Settings open and show why —
    /// a silently swallowed keychain error previously left the app stuck signed-out.
    @discardableResult
    func save() -> Bool {
        guard let url = URL(string: serverURL), !serverURL.isEmpty else {
            saveError = "Enter a valid server URL."
            return false
        }
        do {
            try credStore.save(.init(username: username, password: password))
        } catch {
            saveError = "Couldn't save credentials to the keychain: \(error)"
            return false
        }
        urlStore.save(url)
        saveError = nil
        onSaved?()
        return true
    }
}
