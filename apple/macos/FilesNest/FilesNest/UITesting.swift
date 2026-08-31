import Foundation
import FilesNestCore

enum UITesting {
    static let isEnabled: Bool = {
        let enabled = ProcessInfo.processInfo.arguments.contains("-uiTesting")
            || UserDefaults.standard.bool(forKey: "uiTesting")
            || ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] != nil
        return enabled
    }()

    static let scriptedFolderName = "FilesNestUITestsLocalFolder"

    static func makeDefaults() -> UserDefaults {
        let suiteName = "com.moontechs.FilesNest.ui-testing.\(UUID().uuidString)"
        return UserDefaults(suiteName: suiteName)!
    }

    static func makeConnectionProbe() -> ConnectionProbe {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [UITestConnectionURLProtocol.self]
        return ConnectionProbe(session: URLSession(configuration: configuration))
    }

    @MainActor
    static func makeFolderPicker() -> any LocalFolderPicker {
        let folder = FileManager.default.temporaryDirectory
            .appendingPathComponent(scriptedFolderName, isDirectory: true)
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        return UITestLocalFolderPicker(url: folder)
    }

    nonisolated static func makeFolderBookmark(for url: URL) throws -> Data {
        Data(url.path.utf8)
    }

    fileprivate static func connectionResult(for url: URL?) -> UITestConnectionResult {
        switch url?.host {
        case "unauthorized.filesnest.test": .unauthorized
        case "unreachable.filesnest.test": .unreachable
        default: .ok
        }
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
                client?.urlProtocol(self, didLoad: Data(#"{\"items\":[],\"next_cursor\":\"\"}"#.utf8))
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
    private let lock = NSLock()
    private var value: BasicCredentials?

    func basicCredentials() async throws -> BasicCredentials? {
        lock.withLock { value }
    }

    func save(_ credentials: BasicCredentials) throws {
        lock.withLock { value = credentials }
    }
}
