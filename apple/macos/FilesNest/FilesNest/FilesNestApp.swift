import SwiftUI
import FilesNestCore

@main
struct FilesNestApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model: AppModel
    @StateObject private var settings: SettingsModel
    private let thumbnails = ThumbnailLoader()
    private let watcher: PhotoLibraryWatcher
    private let destinationStore: any SyncDestinationStore
    private let urlStore: any ServerURLStore
    private let credStore: any CredentialSavingStore

    init() {
        let defaults   = UserDefaults.standard
        let urlStore   = UserDefaultsServerURLStore(defaults: defaults)
        let credStore  = CachingCredentialStore(wrapping: KeychainStore())
        let destinationStore = UserDefaultsSyncDestinationStore(defaults: defaults)
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
                guard await isDestinationReady(destinationStore.load(), urlStore: urlStore,
                                               credStore: credStore),
                      let url = urlStore.load() else {
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
            resume: { resources, onProgress in
                // Re-drive the persisted not-yet-uploaded list: no scan, no diff, so a launch or
                // Resume starts backing up immediately. Cold launches verify afterwards; an
                // unchanged Pause resumes its known plan without another full library scan.
                guard await isDestinationReady(destinationStore.load(), urlStore: urlStore,
                                               credStore: credStore),
                      let url = urlStore.load() else {
                    throw NotSignedInError()
                }
                let client   = ServerClient(baseURL: url, credentials: credStore)
                let uploader = AssetUploader(client: client, source: PhotosAssetDataSource())
                let coordinator = SyncCoordinator(client: client,
                                                  library: library,
                                                  uploader: uploader,
                                                  state: stateStore)
                return try await coordinator.resume(resources: resources, onProgress: onProgress)
            },
            assess: { range, progress in
                // Scan (drives the determinate "Counting…" state) + server diff → exact at-rest
                // Pending via SyncPlanner. `.all` on launch/restart; `.modifiedSince` on a change.
                // Cached so a warm launch is instant.
                let scan = try await library.resources(in: range, onProgress: progress.report)
                guard await isDestinationReady(destinationStore.load(), urlStore: urlStore,
                                               credStore: credStore),
                      let url = urlStore.load() else {
                    // Signed out: no server to diff against — everything local is pending.
                    let a = Assessment(backedUp: 0, pending: scan.count, resourceTotal: scan.count)
                    stateStore.saveAssessment(a); return a
                }
                // Assessment is intentionally fail-fast: unlike an upload, it has no
                // reconnect progress state to present while it waits.
                let client = ServerClient(baseURL: url, credentials: credStore, maxPatchRetries: 0)
                var records: [UploadRecord] = []
                var cursor: String? = nil
                repeat {
                    let page = try await client.listUploads(cursor: cursor)
                    records += page.items
                    cursor = page.nextCursor
                } while cursor != nil
                let plan = SyncPlanner.plan(library: scan, server: records, range: range)
                // A windowed scan only sees recent items, so its count is not the whole-library
                // total — keep the last full resourceTotal rather than clobbering it.
                let resourceTotal = (range == .all)
                    ? scan.count
                    : (stateStore.loadAssessment()?.resourceTotal ?? scan.count)
                let a = Assessment(backedUp: records.filter { $0.status == .complete }.count,
                                   pending: plan.uploads.count,
                                   resourceTotal: resourceTotal)
                stateStore.saveAssessment(a)
                return a
            },
            cachedAssessment: { stateStore.loadAssessment() },
            isReady: {
                await isDestinationReady(destinationStore.load(), urlStore: urlStore,
                                         credStore: credStore)
            })

        // Continuously watch the photo library: on a debounced change, invalidate the cached
        // scan and nudge the engine to count + back up (auto-sync scheduler).
        let watcher = PhotoLibraryWatcher(library: library, engine: engine)
        watcher.startObserving()
        self.watcher = watcher

        // Start the engine at launch — reconcile credentials and run launch catch-up — so it
        // does not depend on the menu-bar panel ever being opened. The panel only subscribes to
        // the engine's streams (AppModel.begin).
        Task { await engine.start() }

        let appModel = AppModel(engine: engine)
        let settingsModel = SettingsModel(urlStore: urlStore,
                                          credStore: credStore,
                                          destinationStore: destinationStore,
                                          probe: ConnectionProbe())
        settingsModel.onSaved = { appModel.restart() }
        self.destinationStore = destinationStore
        self.urlStore = urlStore
        self.credStore = credStore
        _model = StateObject(wrappedValue: appModel)
        _settings = StateObject(wrappedValue: settingsModel)
    }

    var body: some Scene {
        MenuBarExtra("FilesNest", systemImage: "arrow.triangle.2.circlepath") {
            PanelView(model: model, destinationStore: destinationStore, thumbnails: thumbnails)
                .task { model.begin() }
        }
        .menuBarExtraStyle(.window)

        Window("", id: "settings-anchor") {
            SettingsAnchorView(destinationStore: destinationStore, urlStore: urlStore,
                               credStore: credStore)
        }
        .windowStyle(.hiddenTitleBar)

        Settings {
            SettingsView(model: settings)
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)   // menu-bar agent, no Dock icon
    }
}

/// Thrown by the sync `perform` closure when no server URL or credentials are set.
struct NotSignedInError: Error {}
