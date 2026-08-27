import SwiftUI
import FilesNestCore
import ServiceManagement

struct SettingsView: View {
    @ObservedObject var model: SettingsModel
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Picker("Sync to", selection: $model.destination) {
                Text("FilesNest Server").tag(SyncDestination.server)
                Text("Local Folder").tag(SyncDestination.localFolder)
            }
            .pickerStyle(.segmented)

            switch model.destination {
            case .server:
                Form {
                    Section("FilesNest server") {
                        TextField("Server URL", text: $model.serverURL)
                            .textContentType(.URL).autocorrectionDisabled()
                        TextField("Username", text: $model.username).autocorrectionDisabled()
                        SecureField("Password", text: $model.password)
                    }
                }

                HStack(spacing: 10) {
                    Button("Connect") { Task { await model.connect() } }
                        .disabled(model.isConnecting || !model.hasCredentials || model.serverURL.isEmpty)
                    if model.isConnecting { ProgressView().controlSize(.small) }
                    connectionPill
                }
            case .localFolder:
                VStack(alignment: .leading, spacing: 4) {
                    Label("Local folder sync is coming soon",
                          systemImage: "externaldrive.badge.timemachine")
                    Text("Choose FilesNest Server to connect and sync your Photos library.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.vertical, 12)
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
        }
        .padding(16).frame(width: 360)
        .task { await model.load() }
    }

    @ViewBuilder private var connectionPill: some View {
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
