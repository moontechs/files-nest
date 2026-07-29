import SwiftUI
import Photos
import FilesNestCore

@main
struct FilesNestApp: App {
    /// Cheap local library size — a plain `PHAsset` count (no per-resource enumeration).
    /// Returns 0 until Photos access is granted. Used for the at-rest pending estimate.
    static func libraryAssetCount() -> Int {
        let status = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        guard status == .authorized || status == .limited else { return 0 }
        return PHAsset.fetchAssets(with: nil).count
    }

    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model: AppModel
    @StateObject private var settings: SettingsModel

    init() {
        let defaults   = UserDefaults.standard
        let urlStore   = UserDefaultsServerURLStore(defaults: defaults)
        let credStore  = KeychainStore()
        let stateStore = UserDefaultsSyncStateStore(defaults: defaults)

        let engine = LiveSyncEngine(
            credentials: credStore,
            state: stateStore,
            perform: { range, onProgress in
                // Read URL + creds at sync time so a Settings change takes effect.
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    throw NotSignedInError()
                }
                let client   = ServerClient(baseURL: url, credentials: credStore)
                let uploader = AssetUploader(client: client, source: PhotosAssetDataSource())
                let coordinator = SyncCoordinator(client: client,
                                                  library: PhotosAssetLibrary(),
                                                  uploader: uploader,
                                                  state: stateStore)
                return try await coordinator.sync(range: range, onProgress: onProgress)
            },
            refreshCounts: {
                // Cheap local library size (asset count, not the expensive per-resource scan).
                let libraryTotal = Self.libraryAssetCount()
                // Live "Backed up" = count of completed uploads on the server.
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    return (backedUp: 0, libraryTotal: libraryTotal)
                }
                let client = ServerClient(baseURL: url, credentials: credStore)
                var count = 0
                var cursor: String? = nil
                repeat {
                    let page = try await client.listUploads(cursor: cursor)
                    count += page.items.filter { $0.status == .complete }.count
                    cursor = page.nextCursor
                } while cursor != nil
                return (backedUp: count, libraryTotal: libraryTotal)
            })

        let appModel = AppModel(engine: engine)
        let settingsModel = SettingsModel(urlStore: urlStore,
                                          credStore: KeychainStore(),
                                          probe: ConnectionProbe())
        settingsModel.onSaved = { appModel.restart() }
        _model = StateObject(wrappedValue: appModel)
        _settings = StateObject(wrappedValue: settingsModel)
    }

    var body: some Scene {
        MenuBarExtra("FilesNest", systemImage: "arrow.triangle.2.circlepath") {
            MenuBarRoot(model: model, settings: settings)
        }
        .menuBarExtraStyle(.window)

        #if DEBUG
        // A persistent window mirroring the panel, so the popup doesn't auto-dismiss
        // during manual testing. Observes the same models — no separate state.
        // Opened on demand by MenuBarRoot (a menu-bar-primary app won't auto-present it).
        Window("FilesNest (debug)", id: "filesnest-debug-panel") {
            PanelView(model: model, settings: settings)
        }
        .windowResizability(.contentSize)
        #endif
    }
}

/// The menu-bar popup content. Starts the engine, and in DEBUG opens the persistent
/// debug window so it stays visible while testing (the popup itself auto-dismisses).
private struct MenuBarRoot: View {
    @ObservedObject var model: AppModel
    @ObservedObject var settings: SettingsModel
    #if DEBUG
    @Environment(\.openWindow) private var openWindow
    #endif

    var body: some View {
        PanelView(model: model, settings: settings)
            .task { model.begin() }
            #if DEBUG
            .task { openWindow(id: "filesnest-debug-panel") }
            #endif
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        #if DEBUG
        NSApp.setActivationPolicy(.regular)     // show/focus the debug window during testing
        #else
        NSApp.setActivationPolicy(.accessory)   // production: menu-bar agent, no Dock icon
        #endif
    }
}

/// Thrown by the sync `perform` closure when no server URL or credentials are set.
struct NotSignedInError: Error {}
