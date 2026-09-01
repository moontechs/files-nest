import Foundation
import FilesNestCore

enum UITesting {
    enum Fixture: String {
        case standard
        case syncing
        case counting
        case verifying
        case reconnecting
        case protected
        case needsAttention
        case error
        case failed
    }

    enum FolderScenario: String {
        case selected
        case cancelled
        case bookmarkFailure
    }

    static let isEnabled: Bool = {
        let enabled = ProcessInfo.processInfo.arguments.contains("-uiTesting")
            || UserDefaults.standard.bool(forKey: "uiTesting")
            || ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil
        return enabled
    }()

    static let scriptedFolderName = "FilesNestUITestsLocalFolder"

    nonisolated private static func argumentValue(after flag: String) -> String? {
        let arguments = ProcessInfo.processInfo.arguments
        guard let index = arguments.firstIndex(of: flag), arguments.indices.contains(index + 1) else {
            return nil
        }
        return arguments[index + 1]
    }

    static let fixture: Fixture = {
        Fixture(rawValue: argumentValue(after: "-uiFixture") ?? "") ?? .standard
    }()

    static let folderScenario: FolderScenario = {
        FolderScenario(rawValue: argumentValue(after: "-uiFolderScenario") ?? "") ?? .selected
    }()

    static func makeDefaults() -> UserDefaults {
        let session = argumentValue(after: "-uiTestSession") ?? UUID().uuidString
        let suiteName = "com.moontechs.FilesNest.ui-testing.\(session)"
        return UserDefaults(suiteName: suiteName)!
    }

    static func makeConnectionProbe() -> ConnectionProbe {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [UITestConnectionURLProtocol.self]
        return ConnectionProbe(session: URLSession(configuration: configuration))
    }

    @MainActor
    static func makeFolderPicker() -> any LocalFolderPicker {
        guard folderScenario != .cancelled else { return UITestLocalFolderPicker(url: nil) }
        let folder = FileManager.default.temporaryDirectory
            .appendingPathComponent(scriptedFolderName, isDirectory: true)
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        return UITestLocalFolderPicker(url: folder)
    }

    static func makeFolderBookmark(for url: URL) throws -> Data {
        guard folderScenario != .bookmarkFailure else {
            throw NSError(domain: "FilesNestUITests", code: 1,
                          userInfo: [NSLocalizedDescriptionKey: "The test bookmark store is unavailable."])
        }
        return Data(url.path.utf8)
    }

    fileprivate static func connectionResult(for url: URL?) -> UITestConnectionResult {
        switch url?.host {
        case "unauthorized.filesnest.test": .unauthorized
        case "unreachable.filesnest.test": .unreachable
        default: .ok
        }
    }
}

/// Deterministic panel state for XCUITest. It is intentionally app-target-only:
/// core tests exercise the actual engine; UI tests only need observable states.
final class UITestSyncEngine: SyncEngine, @unchecked Sendable {
    private let credentials: any CredentialStore
    private let fixture: UITesting.Fixture
    private let lock = NSLock()
    private var status = SyncStatus.signedOut
    private var summary = SyncSummary.empty
    private var statusContinuations: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]
    private var summaryContinuations: [UUID: AsyncStream<SyncSummary>.Continuation] = [:]

    init(credentials: any CredentialStore, fixture: UITesting.Fixture) {
        self.credentials = credentials
        self.fixture = fixture
    }

    func statusStream() -> AsyncStream<SyncStatus> {
        AsyncStream { continuation in
            let id = UUID()
            lock.withLock {
                continuation.yield(status)
                statusContinuations[id] = continuation
            }
            continuation.onTermination = { [weak self] _ in
                self?.lock.withLock { self?.statusContinuations[id] = nil }
            }
        }
    }

    func summaryStream() -> AsyncStream<SyncSummary> {
        AsyncStream { continuation in
            let id = UUID()
            lock.withLock {
                continuation.yield(summary)
                summaryContinuations[id] = continuation
            }
            continuation.onTermination = { [weak self] _ in
                self?.lock.withLock { self?.summaryContinuations[id] = nil }
            }
        }
    }

    func start() async {
        switch fixture {
        case .syncing:
            publish(.syncing(progress), summary: readySummary)
        case .counting:
            publish(.counting(done: 2, total: 5, purpose: .survey), summary: readySummary)
        case .verifying:
            publish(.counting(done: 4, total: 5, purpose: .verify), summary: readySummary)
        case .reconnecting:
            publish(.reconnecting(reconnectingProgress), summary: readySummary)
        case .protected:
            publish(.watching(lastSync: Date(timeIntervalSince1970: 0)), summary: protectedSummary)
        case .needsAttention:
            publish(.watching(lastSync: nil), summary: readySummary)
        case .error:
            publish(.error(message: "The server is unavailable."), summary: readySummary)
        case .failed:
            publish(.watching(lastSync: Date(timeIntervalSince1970: 0)), summary: failedSummary)
        case .standard:
            let credentials = try? await credentials.basicCredentials()
            publish(credentials == nil ? .signedOut : .watching(lastSync: nil), summary: .empty)
        }
    }

    func pause() async { publish(.paused(pending: 3), summary: currentSummary) }
    func resume() async { publish(.syncing(progress), summary: currentSummary) }
    func syncNow() async { publish(.syncing(progress), summary: currentSummary) }
    func libraryDidChange() async {}
    func reconcile() async { await start() }

    private var progress: SyncProgress {
        SyncProgress(completed: 2, total: 5, currentItemName: "IMG_2045.HEIC",
                     bytesRemaining: 51_000_000, currentItemID: "ui-test-photo", inFlight: 1)
    }

    private var reconnectingProgress: SyncProgress {
        SyncProgress(completed: 2, total: 5, currentItemName: "IMG_2045.HEIC",
                     bytesRemaining: 51_000_000, currentItemID: "ui-test-photo", inFlight: 0,
                     retry: RetryProgress(retryAt: Date().addingTimeInterval(30), waitingRequests: 2))
    }

    private var readySummary: SyncSummary {
        SyncSummary(backedUp: 12, pending: 3, failed: [], resourceTotal: 15)
    }

    private var protectedSummary: SyncSummary {
        SyncSummary(backedUp: 15, pending: 0, failed: [], resourceTotal: 15)
    }

    private var failedSummary: SyncSummary {
        SyncSummary(backedUp: 12, pending: 1,
                    failed: [FailedItem(key: ResourceKey(localIdentifier: "failed-photo", kind: .photo),
                                        filename: "IMG_2045.HEIC", reason: "The server is unavailable.")],
                    resourceTotal: 13)
    }

    private var currentSummary: SyncSummary { lock.withLock { summary } }

    private func publish(_ newStatus: SyncStatus, summary newSummary: SyncSummary) {
        let continuations: ([AsyncStream<SyncStatus>.Continuation], [AsyncStream<SyncSummary>.Continuation]) = lock.withLock {
            status = newStatus
            summary = newSummary
            return (Array(statusContinuations.values), Array(summaryContinuations.values))
        }
        continuations.0.forEach { $0.yield(newStatus) }
        continuations.1.forEach { $0.yield(newSummary) }
    }
}

fileprivate enum UITestConnectionResult { case ok, unauthorized, unreachable }

private final class UITestConnectionURLProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let result = UITesting.connectionResult(for: request.url)
        switch result {
        case .unreachable:
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
        case .ok, .unauthorized:
            let statusCode = result == .ok ? 200 : 401
            let response = HTTPURLResponse(url: request.url!, statusCode: statusCode,
                                           httpVersion: nil, headerFields: nil)!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            if statusCode == 200 {
                client?.urlProtocol(self, didLoad: Data(#"{"items":[],"next_cursor":""}"#.utf8))
            }
            client?.urlProtocolDidFinishLoading(self)
        }
    }

    override func stopLoading() {}
}

@MainActor
private final class UITestLocalFolderPicker: LocalFolderPicker {
    private let url: URL?

    init(url: URL?) { self.url = url }

    func chooseFolder() -> URL? { url }
}

final class UITestCredentialStore: CredentialSavingStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let usernameKey = "com.filesnest.uiTesting.username"
    private let passwordKey = "com.filesnest.uiTesting.password"

    init(defaults: UserDefaults) { self.defaults = defaults }

    func basicCredentials() async throws -> BasicCredentials? {
        guard let username = defaults.string(forKey: usernameKey),
              let password = defaults.string(forKey: passwordKey) else { return nil }
        return BasicCredentials(username: username, password: password)
    }

    func save(_ credentials: BasicCredentials) throws {
        defaults.set(credentials.username, forKey: usernameKey)
        defaults.set(credentials.password, forKey: passwordKey)
    }
}
