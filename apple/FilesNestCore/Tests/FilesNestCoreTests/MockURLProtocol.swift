import Foundation

final class MockURLProtocol: URLProtocol, @unchecked Sendable {
    typealias Handler = (URLRequest) throws -> (HTTPURLResponse, Data)

    /// Legacy single handler. Suites using it must not run concurrently with
    /// each other — prefer `setHandler(forHost:)`.
    nonisolated(unsafe) static var handler: Handler?

    /// Per-host handlers.
    ///
    /// `.serialized` orders tests only WITHIN a suite; separate suites still run
    /// in parallel, so a single shared handler is overwritten across suites.
    /// Observed concretely: running the full suite made ServerClientNetworkTests
    /// and AssetUploaderTests fail with 500s and empty bodies while each passed
    /// in isolation. Keying by host gives every suite its own stub.
    nonisolated(unsafe) private static var hostHandlers: [String: Handler] = [:]
    private static let lock = NSLock()

    static func setHandler(forHost host: String, _ handler: @escaping Handler) {
        lock.lock(); defer { lock.unlock() }
        hostHandlers[host] = handler
    }

    static func removeHandler(forHost host: String) {
        lock.lock(); defer { lock.unlock() }
        hostHandlers[host] = nil
    }

    private static func handler(forHost host: String?) -> Handler? {
        lock.lock(); defer { lock.unlock() }
        if let host, let scoped = hostHandlers[host] { return scoped }
        return handler
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = MockURLProtocol.handler(forHost: request.url?.host) else {
            client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse)); return
        }
        do {
            let (response, data) = try handler(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}

    static func makeSession() -> URLSession {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.protocolClasses = [MockURLProtocol.self]
        return URLSession(configuration: cfg)
    }

    static func respond(status: Int, headers: [String: String] = [:], body: Data = Data(),
                        for url: URL) -> (HTTPURLResponse, Data) {
        (HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!, body)
    }
}
