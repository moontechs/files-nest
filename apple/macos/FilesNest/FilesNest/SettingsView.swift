import AppKit
import SwiftUI
import FilesNestCore
import ServiceManagement

struct SettingsView: View {
    @ObservedObject var model: SettingsModel
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 5) {
                Text("Backup destination")
                    .font(.headline)
                Text("Choose where FilesNest sends future backups. You can switch at any time; settings for the other destination stay saved.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            Picker("Backup destination", selection: $model.destination) {
                Text("FilesNest Server").tag(SyncDestination.server)
                Text("Local Folder").tag(SyncDestination.localFolder)
            }
            .pickerStyle(.segmented)
            .accessibilityHint("Changes where future backups are sent immediately.")

            GroupBox {
                switch model.destination {
                case .server:
                    serverSettings
                case .localFolder:
                    localFolderSettings
                }
            }
            .accessibilityElement(children: .contain)

            Toggle("Launch at login", isOn: $launchAtLogin)
                .onChange(of: launchAtLogin) { _, on in
                    try? on ? SMAppService.mainApp.register() : SMAppService.mainApp.unregister()
                }

        }
        .padding(20).frame(width: 420)
        .task { await model.load() }
        .onDisappear { NSApp.setActivationPolicy(.accessory) }
    }

    private var serverSettings: some View {
        VStack(alignment: .leading, spacing: 14) {
            activeDestination("Backing up to FilesNest Server", systemImage: "server.rack")

            Form {
                Section("Server connection") {
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

    private var localFolderSettings: some View {
        VStack(alignment: .leading, spacing: 14) {
            activeDestination("Backing up to Local Folder", systemImage: "folder.badge.gearshape")

            VStack(alignment: .leading, spacing: 4) {
                Text("Save backups here")
                    .font(.subheadline.weight(.medium))
                if let selectedFolderPath = model.selectedFolderPath {
                    Text(selectedFolderPath)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .textSelection(.enabled)
                } else {
                    Text("Choose a folder to finish setting up this destination.")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            }

            Button(model.selectedFolderPath == nil ? "Choose Folder…" : "Choose Different Folder…") {
                model.chooseLocalFolder()
            }
                .buttonStyle(.borderedProminent)

            Label("Your saved server address and credentials are kept. Switch back to FilesNest Server above whenever you want to use them again.",
                  systemImage: "checkmark.shield")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let saveError = model.saveError {
                Label(saveError, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.red).lineLimit(2)
            }
        }
        .frame(maxWidth: .infinity, minHeight: 170, alignment: .leading)
    }

    private func activeDestination(_ title: LocalizedStringKey, systemImage: String) -> some View {
        Label(title, systemImage: systemImage)
            .font(.headline)
            .accessibilityAddTraits(.isHeader)
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
