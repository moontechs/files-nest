import Foundation
@testable import FilesNestCore

/// Thread-safe collector for `onProgress` callbacks (which are @Sendable, sync).
final class ProgressRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _items: [SyncProgress] = []
    var items: [SyncProgress] { lock.lock(); defer { lock.unlock() }; return _items }
    func record(_ p: SyncProgress) { lock.lock(); defer { lock.unlock() }; _items.append(p) }
}
