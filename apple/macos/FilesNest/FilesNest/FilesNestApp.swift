import SwiftUI
import FilesNestCore

@main
struct FilesNestApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model: AppModel

    init() {
        // Composition root: build the object graph once.
        let engine = StubSyncEngine(credentials: KeychainStore())
        _model = StateObject(wrappedValue: AppModel(engine: engine))
    }

    var body: some Scene {
        MenuBarExtra("FilesNest", systemImage: "arrow.triangle.2.circlepath") {
            PanelView(model: model).task { model.begin() }
        }
        .menuBarExtraStyle(.window)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Menu-bar agent: no Dock icon, no main window.
        NSApp.setActivationPolicy(.accessory)
    }
}
