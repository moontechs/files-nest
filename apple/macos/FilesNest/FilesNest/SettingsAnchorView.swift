import SwiftUI
import FilesNestCore

struct SettingsAnchorView: View {
    let destinationStore: any SyncDestinationStore
    let urlStore: any ServerURLStore
    let credStore: any CredentialStore
    let localFolderStore: any LocalFolderStore
    @Environment(\.openSettings) private var openSettings
    @State private var hasCheckedInitialReadiness = false

    var body: some View {
        Color.clear
            .task {
                guard !hasCheckedInitialReadiness else { return }
                hasCheckedInitialReadiness = true
                let ready = await isDestinationReady(
                    destinationStore.load(), urlStore: urlStore, credStore: credStore,
                    localFolderStore: localFolderStore)
                if !ready { SettingsPresenter.open(openSettings) }
            }
    }
}
