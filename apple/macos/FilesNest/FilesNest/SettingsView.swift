import AppKit
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

            Group {
                switch model.destination {
                case .server:
                    serverSettings
                case .localFolder:
                    localFolderPlaceholder
                }
            }

            Toggle("Launch at login", isOn: $launchAtLogin)
                .onChange(of: launchAtLogin) { _, on in
                    try? on ? SMAppService.mainApp.register() : SMAppService.mainApp.unregister()
                }

        }
        .padding(16).frame(width: 360)
        .task { await model.load() }
        .onDisappear { NSApp.setActivationPolicy(.accessory) }
    }

    private var serverSettings: some View {
        VStack(alignment: .leading, spacing: 12) {
            Form {
                Section("FilesNest server") {
                    TextField("Server URL", text: $model.serverURL)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                    TextField("Username", text: $model.username).autocorrectionDisabled()
                    SecureField("Password", text: $model.password)
                }
            }

            HStack(spacing: 10) {
                Button("Connect") { Task { await model.connect() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.isConnecting || !model.canConnect)
                if model.isConnecting { ProgressView().controlSize(.small) }
                testPill
            }

            if let saveError = model.saveError {
                Label(saveError, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.red).lineLimit(2)
            }
        }
    }

    private var localFolderPlaceholder: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Local folder sync is coming soon", systemImage: "externaldrive.badge.timemachine")
                .font(.headline)
            Text("You’ll be able to back up directly to a folder or connected drive.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: 120, alignment: .leading)
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
