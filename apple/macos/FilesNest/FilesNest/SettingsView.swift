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
            Text("FilesNest Settings").font(.headline)

            Form {
                TextField("Server URL", text: $model.serverURL)
                    .textContentType(.URL).autocorrectionDisabled()
                TextField("Username", text: $model.username).autocorrectionDisabled()
                SecureField("Password", text: $model.password)
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
                Button("Save") { if model.save() { onDone() } }
                    .buttonStyle(.borderedProminent).disabled(!model.hasCredentials)
            }
        }
        .padding(16).frame(width: 320)
        .task { await model.load() }
    }

    @ViewBuilder private var testPill: some View {
        switch model.testResult {
        case .ok: Label("Connected", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
        case .unauthorized: Label("401 Unauthorized", systemImage: "xmark.circle.fill").foregroundStyle(.red)
        case .unreachable(let m): Label(m, systemImage: "xmark.circle.fill").foregroundStyle(.red).lineLimit(1)
        case nil: EmptyView()
        }
    }
}
