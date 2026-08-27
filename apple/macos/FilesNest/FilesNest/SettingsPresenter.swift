import AppKit
import SwiftUI

@MainActor
enum SettingsPresenter {
    static func open(_ openSettings: OpenSettingsAction) {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        openSettings()
    }

    static func open() {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil)
    }

    static func settingsWindowDidClose() {
        NSApp.setActivationPolicy(.accessory)
    }
}
