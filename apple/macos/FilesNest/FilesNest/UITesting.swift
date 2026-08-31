import Foundation
import FilesNestCore

enum UITesting {
    static let isEnabled = ProcessInfo.processInfo.arguments.contains("-uiTesting")

    static func makeDefaults() -> UserDefaults {
        let suiteName = "com.moontechs.FilesNest.ui-testing.\(UUID().uuidString)"
        return UserDefaults(suiteName: suiteName)!
    }
}

final class UITestCredentialStore: CredentialSavingStore, @unchecked Sendable {
    private let lock = NSLock()
    private var value: BasicCredentials?

    func basicCredentials() async throws -> BasicCredentials? {
        lock.withLock { value }
    }

    func save(_ credentials: BasicCredentials) throws {
        lock.withLock { value = credentials }
    }
}
