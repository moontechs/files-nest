import SwiftUI
import FilesNestCore
import ServiceManagement

struct SettingsView: View {
    @ObservedObject var model: SettingsModel
    /// Return to the dashboard (this view lives inside the menu-bar panel, not a window).
    var onDone: () -> Void
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Button { onDone() } label: { Label("Back", systemImage: "chevron.left") }
                    .buttonStyle(.link)
                Spacer()
            }
            Text("Connect your server").font(.title3.weight(.semibold))
            Text("FilesNest keeps your password in your Mac keychain.")
                .font(.caption).foregroundStyle(.secondary)

            Form {
                Section("FilesNest server") {
                    TextField("Server URL", text: $model.serverURL)
                        .textContentType(.URL).autocorrectionDisabled()
                    TextField("Username", text: $model.username).autocorrectionDisabled()
                    SecureField("Password", text: $model.password)
                }
            }

            HStack(spacing: 10) {
                Button("Test Connection") { Task { await model.test() } }
                    .disabled(model.isTesting || !model.hasCredentials)
                if model.isTesting { ProgressView().controlSize(.small) }
                testPill
            }

            Toggle("Launch at login", isOn: $launchAtLogin)
                .onChange(of: launchAtLogin) { _, on in
                    try? on ? SMAppService.mainApp.register() : SMAppService.mainApp.unregister()
                }

            Divider()
            if let saveError = model.saveError {
                Label(saveError, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.red).lineLimit(2)
            }
            HStack {
                Spacer()
                Button("Save & Connect") { if model.save() { onDone() } }
                    .buttonStyle(.borderedProminent).disabled(!model.hasCredentials)
            }
        }
        .padding(16).frame(width: 320)
        .task { await model.load() }
    }

    @ViewBuilder private var testPill: some View {
        switch model.testResult {
        case .ok: Label("Connected", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
        case .unauthorized:
            connectionFailure("The server rejected these credentials.",
                              detail: "Check the username and password, then try again.")
        case .unreachable(let message):
            connectionFailure("Couldn’t reach the server.",
                              detail: "Check the address, network connection, and that the server is online. \(message)")
        case nil: EmptyView()
        }
    }

    private func connectionFailure(_ title: String, detail: String) -> some View {
        HStack(alignment: .top, spacing: 4) {
            Image(systemName: "xmark.circle.fill")
            VStack(alignment: .leading, spacing: 1) {
                Text(title)
                Text(detail).font(.caption2).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .font(.caption)
        .foregroundStyle(.red)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}
