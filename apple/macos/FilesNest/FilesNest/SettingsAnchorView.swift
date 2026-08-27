import SwiftUI
import FilesNestCore

struct SettingsAnchorView: View {
    @Environment(\.openSettings) private var openSettings
    @State private var hasCheckedReadiness = false

    let urlStore: any ServerURLStore
    let credStore: any CredentialStore
    let destinationStore: any SyncDestinationStore

    var body: some View {
        Color.clear
            .task {
                guard !hasCheckedReadiness else { return }
                hasCheckedReadiness = true

                let ready = await isDestinationReady(destinationStore.load(),
                                                      urlStore: urlStore,
                                                      credStore: credStore)
                if !ready {
                    SettingsPresenter.open(openSettings)
                }
            }
    }
}
