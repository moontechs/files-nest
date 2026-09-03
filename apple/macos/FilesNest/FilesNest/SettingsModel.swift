import SwiftUI
import Combine
import AppKit
import FilesNestCore

@MainActor
protocol LocalFolderPicker { func chooseFolder() -> URL? }

@MainActor
struct OpenPanelLocalFolderPicker: LocalFolderPicker {
    static func makePanel() -> NSOpenPanel {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        panel.allowsMultipleSelection = false
        panel.prompt = "Choose Folder"
        return panel
    }

    func chooseFolder() -> URL? {
        let panel = Self.makePanel()
        return panel.runModal() == .OK ? panel.url : nil
    }
}

@MainActor
final class SettingsModel: ObservableObject {
    @Published var serverURL = "" { didSet { if serverURL != oldValue { markDraftAsEdited() } } }
    @Published var username = "" { didSet { if username != oldValue { markDraftAsEdited() } } }
    @Published var password = "" { didSet { if password != oldValue { markDraftAsEdited() } } }
    @Published var destination: SyncDestination = .server {
        didSet {
            guard destination != oldValue else { return }
            markDraftAsEdited()
            destinationStore.save(destination)
            onSaved?()
        }
    }
    @Published var testResult: ConnectionResult?
    @Published var isConnecting = false
    @Published var saveError: String?
    @Published private(set) var selectedFolderPath: String?

    private let urlStore: any ServerURLStore
    private let credStore: any CredentialSavingStore
    private let destinationStore: any SyncDestinationStore
    private let probe: ConnectionProbe
    private let localFolderStore: any LocalFolderStore
    private let folderPicker: any LocalFolderPicker
    private let folderBookmarkCreator: (URL) throws -> Data
    private var hasLoadedInitialValues = false
    private var hasDraftEdits = false
    private var isApplyingInitialValues = false
    var onSaved: (() -> Void)?

    init(
        urlStore: any ServerURLStore,
        credStore: any CredentialSavingStore,
        destinationStore: any SyncDestinationStore,
        probe: ConnectionProbe,
        localFolderStore: any LocalFolderStore,
        folderPicker: (any LocalFolderPicker)? = nil,
        folderBookmarkCreator: ((URL) throws -> Data)? = nil
    ) {
        self.urlStore = urlStore
        self.credStore = credStore
        self.destinationStore = destinationStore
        self.probe = probe
        self.localFolderStore = localFolderStore
        self.folderPicker = folderPicker ?? OpenPanelLocalFolderPicker()
        self.folderBookmarkCreator = folderBookmarkCreator ?? { url in
            try url.bookmarkData(options: [.withSecurityScope],
                                 includingResourceValuesForKeys: nil,
                                 relativeTo: nil)
        }
        self._destination = Published(initialValue: destinationStore.load())
        self._selectedFolderPath = Published(initialValue: resolveLocalFolder(store: localFolderStore)?.path)
    }

    func chooseLocalFolder() {
        guard let url = folderPicker.chooseFolder() else { return }
        do {
            let bookmark = try folderBookmarkCreator(url)
            localFolderStore.save(bookmark)
            selectedFolderPath = url.path
            saveError = nil
            onSaved?()
        } catch {
            saveError = "Couldn't save the selected folder: \(error)"
        }
    }

    var hasCredentials: Bool { !username.isEmpty && !password.isEmpty }
    var canConnect: Bool { !serverURL.isEmpty && hasCredentials }

    func load() async {
        // SettingsView is recreated whenever the menu-bar panel is dismissed and
        // opened again. Load persisted values only once; subsequent appearances
        // must keep the in-memory draft, including pasted credentials.
        guard !hasLoadedInitialValues else { return }
        hasLoadedInitialValues = true

        // The server URL is not a secret, so show it immediately. Keychain reads can
        // take noticeably longer; they must not leave the whole form blank meanwhile.
        guard !hasDraftEdits else { return }
        isApplyingInitialValues = true
        destination = destinationStore.load()
        if let savedURL = urlStore.load()?.absoluteString { serverURL = savedURL }
        isApplyingInitialValues = false

        let savedCredentials = try? await credStore.basicCredentials()

        // Keychain access awaits. If the user starts typing before it completes,
        // do not overwrite their draft with the older persisted credentials.
        guard !hasDraftEdits else { return }
        isApplyingInitialValues = true
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

    /// Verifies the entered connection before saving it, so failed attempts never
    /// overwrite a previously working configuration.
    func connect() async {
        guard !isConnecting else { return }
        guard let url = URL(string: serverURL),
              !serverURL.isEmpty,
              url.scheme != nil,
              url.host != nil else {
            saveError = "Enter a valid server URL."
            return
        }
        guard hasCredentials else {
            saveError = "Enter a username and password."
            return
        }

        isConnecting = true
        defer { isConnecting = false }
        let credentials = BasicCredentials(username: username, password: password)
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
