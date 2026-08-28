import AppKit
import SwiftUI

@MainActor
enum SettingsPresenter {
    static func open(_ openSettings: OpenSettingsAction) {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        openSettings()
    }
}
