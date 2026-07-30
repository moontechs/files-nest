import SwiftUI
import FilesNestCore

@main
struct FilesNestApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model: AppModel
    @StateObject private var settings: SettingsModel
    private let thumbnails = ThumbnailLoader()
    private let watcher: PhotoLibraryWatcher

    init() {
        let defaults   = UserDefaults.standard
        let urlStore   = UserDefaultsServerURLStore(defaults: defaults)
        let credStore  = KeychainStore()
        let stateStore = UserDefaultsSyncStateStore(defaults: defaults)
        // Shared, TTL-memoized scan so a Sync Now right after the launch count reuses that
        // scan instead of paying a second full enumeration. (Observer-invalidated later.)
        // Change-based invalidation (via PhotoLibraryWatcher) is the primary freshness
        // mechanism; the TTL is a self-healing backstop for a missed observer signal.
        let library    = CachingAssetLibrary(wrapping: PhotosAssetLibrary(), ttl: 300)

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
                                                  library: library,   // shares the launch count's cached scan
                                                  uploader: uploader,
                                                  state: stateStore)
                return try await coordinator.sync(range: range, onProgress: onProgress)
            },
            assess: { progress in
                // Full library scan (drives the determinate "Counting…" state) + server diff →
                // exact at-rest Pending via SyncPlanner. Cached so a warm launch is instant.
                let scan = try await library.resources(in: .all, onProgress: progress.report)
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    // Signed out: no server to diff against — everything local is pending.
                    let a = Assessment(backedUp: 0, pending: scan.count, resourceTotal: scan.count)
                    stateStore.saveAssessment(a); return a
                }
                let client = ServerClient(baseURL: url, credentials: credStore)
                var records: [UploadRecord] = []
                var cursor: String? = nil
                repeat {
                    let page = try await client.listUploads(cursor: cursor)
                    records += page.items
                    cursor = page.nextCursor
                } while cursor != nil
                let plan = SyncPlanner.plan(library: scan, server: records, range: .all)
                let a = Assessment(backedUp: records.filter { $0.status == .complete }.count,
                                   pending: plan.uploads.count,
                                   resourceTotal: scan.count)
                stateStore.saveAssessment(a)
                return a
            },
            cachedAssessment: { stateStore.loadAssessment() })

        // Continuously watch the photo library: on a debounced change, invalidate the cached
        // scan and nudge the engine to count + back up (auto-sync scheduler).
        let watcher = PhotoLibraryWatcher(library: library, engine: engine)
        watcher.startObserving()
        self.watcher = watcher

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
            PanelView(model: model, settings: settings, thumbnails: thumbnails).task { model.begin() }
        }
        .menuBarExtraStyle(.window)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)   // menu-bar agent, no Dock icon
    }
}

/// Thrown by the sync `perform` closure when no server URL or credentials are set.
struct NotSignedInError: Error {}
