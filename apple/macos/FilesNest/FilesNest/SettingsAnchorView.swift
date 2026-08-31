import SwiftUI
import FilesNestCore

struct SettingsAnchorView: View {
    let model: AppModel
    let destinationStore: any SyncDestinationStore
    let urlStore: any ServerURLStore
    let credStore: any CredentialStore
    let localFolderStore: any LocalFolderStore
    let thumbnails: ThumbnailLoader
    @Environment(\.openSettings) private var openSettings
    @State private var hasCheckedInitialReadiness = false

    var body: some View {
        if UITesting.isEnabled {
            PanelView(model: model, destinationStore: destinationStore, thumbnails: thumbnails)
                .task { model.begin() }
        } else {
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
}
