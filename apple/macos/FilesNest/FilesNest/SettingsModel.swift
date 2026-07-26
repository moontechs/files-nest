import SwiftUI
import Combine
import FilesNestCore

@MainActor
final class SettingsModel: ObservableObject {
    @Published var serverURL = ""
    @Published var username = ""
    @Published var password = ""
    @Published var testResult: ConnectionResult?
    @Published var isTesting = false

    private let urlStore: any ServerURLStore
    private let credStore: KeychainStore
    private let probe: ConnectionProbe
    var onSaved: (() -> Void)?

    init(urlStore: any ServerURLStore, credStore: KeychainStore, probe: ConnectionProbe) {
        self.urlStore = urlStore
        self.credStore = credStore
        self.probe = probe
    }

    var hasCredentials: Bool { !username.isEmpty && !password.isEmpty }

    func load() async {
        if let u = urlStore.load() { serverURL = u.absoluteString }
        if let c = try? await credStore.basicCredentials() {
            username = c.username; password = c.password
        }
    }

    func test() async {
        guard let url = URL(string: serverURL), !serverURL.isEmpty else {
            testResult = .unreachable("Invalid URL"); return
        }
        isTesting = true; defer { isTesting = false }
        testResult = await probe.probe(baseURL: url,
                                       credentials: .init(username: username, password: password))
    }

    func save() {
        guard let url = URL(string: serverURL), !serverURL.isEmpty else { return }
        try? credStore.save(.init(username: username, password: password))
        urlStore.save(url)
        onSaved?()
    }
}
