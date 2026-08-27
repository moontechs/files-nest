import AppKit
import SwiftUI

struct SettingsWindowCloseObserver: NSViewRepresentable {
    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> CloseObserverView {
        CloseObserverView { context.coordinator.observe($0) }
    }

    func updateNSView(_ nsView: CloseObserverView, context: Context) {}

    final class Coordinator {
        private var observer: NSObjectProtocol?
        private weak var observedWindow: NSWindow?

        deinit {
            if let observer { NotificationCenter.default.removeObserver(observer) }
        }

        func observe(_ window: NSWindow?) {
            guard observedWindow !== window else { return }
            if let observer { NotificationCenter.default.removeObserver(observer) }
            observedWindow = window
            guard let window else { return }
            observer = NotificationCenter.default.addObserver(
                forName: NSWindow.willCloseNotification,
                object: window,
                queue: .main
            ) { _ in
                Task { @MainActor in SettingsPresenter.settingsWindowDidClose() }
            }
        }
    }
}

final class CloseObserverView: NSView {
    private let onWindowChanged: (NSWindow?) -> Void

    init(onWindowChanged: @escaping (NSWindow?) -> Void) {
        self.onWindowChanged = onWindowChanged
        super.init(frame: .zero)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        onWindowChanged(window)
    }
}
