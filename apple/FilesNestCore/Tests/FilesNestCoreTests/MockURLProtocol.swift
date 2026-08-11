import Foundation

final class MockURLProtocol: URLProtocol, @unchecked Sendable {
    typealias Handler = (URLRequest) throws -> (HTTPURLResponse, Data)

    /// Per-host handlers — the only routing mechanism.
    ///
    /// `.serialized` orders tests only WITHIN a suite; separate suites still run
    /// in parallel. A single shared static handler was both overwritten across
    /// suites (500s/empty bodies) and an unsynchronized read/write data race.
    /// Keying by host under `lock` gives every suite its own stub with no shared
    /// mutable state. Every request's host must have a registered handler.
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
        guard let host else { return nil }
        return hostHandlers[host]
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
