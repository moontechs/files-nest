import Photos
import FilesNestCore

/// Watches the photo library and, after a burst settles, refreshes the cached scan and
/// nudges the engine to count + back up. PhotoKit lives here (app target); Core stays
/// PhotoKit-free and only learns "the library changed" via `engine.libraryDidChange()`.
final class PhotoLibraryWatcher: NSObject, PHPhotoLibraryChangeObserver {
    private let library: CachingAssetLibrary
    private let engine: any SyncEngine
    private let debounce: Duration
    private var debounceTask: Task<Void, Never>?   // MainActor-isolated below

    init(library: CachingAssetLibrary, engine: any SyncEngine, debounce: Duration = .seconds(2)) {
        self.library = library
        self.engine = engine
        self.debounce = debounce
        super.init()
    }

    /// Register for change notifications. Harmless before photo-library authorization —
    /// it simply won't fire until access is granted (the app requests auth on first scan).
    func startObserving() { PHPhotoLibrary.shared().register(self) }

    // Called by PhotoKit on an arbitrary queue.
    nonisolated func photoLibraryDidChange(_ changeInstance: PHChange) {
        Task { @MainActor in self.scheduleFlush() }
    }

    @MainActor private func scheduleFlush() {
        debounceTask?.cancel()   // coalesce a burst into one flush after quiescence
        debounceTask = Task { [library, engine, debounce] in
            try? await Task.sleep(for: debounce)
            if Task.isCancelled { return }
            await library.invalidate()        // fresh scan next; app owns invalidation
            await engine.libraryDidChange()   // then notify Core
        }
    }

    deinit { PHPhotoLibrary.shared().unregisterChangeObserver(self) }
}
