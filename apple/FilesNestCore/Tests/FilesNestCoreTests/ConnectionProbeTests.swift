import Testing
import Foundation
@testable import FilesNestCore

@Suite(.serialized)
struct ConnectionProbeTests {
    let host = "probe.test"
    var baseURL: URL { URL(string: "https://\(host)")! }
    let creds = BasicCredentials(username: "u", password: "p")

    @Test func reachableAndAuthedIsOk() async {
        MockURLProtocol.setHandler(forHost: host) { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":[],"next_cursor":""}"#.data(using: .utf8)!, for: req.url!)
        }
        defer { MockURLProtocol.removeHandler(forHost: host) }
        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        #expect(await probe.probe(baseURL: baseURL, credentials: creds) == .ok)
    }

    @Test func rejectedCredsIsUnauthorized() async {
        MockURLProtocol.setHandler(forHost: host) { req in
            MockURLProtocol.respond(status: 401, body: Data(), for: req.url!)
        }
        defer { MockURLProtocol.removeHandler(forHost: host) }
        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        #expect(await probe.probe(baseURL: baseURL, credentials: creds) == .unauthorized)
    }

    @Test func transportFailureIsUnreachable() async {
        MockURLProtocol.setHandler(forHost: host) { _ in throw URLError(.cannotConnectToHost) }
        defer { MockURLProtocol.removeHandler(forHost: host) }
        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        if case .unreachable = await probe.probe(baseURL: baseURL, credentials: creds) {} else {
            Issue.record("expected .unreachable")
        }
    }

    @Test func serviceUnavailableIsReportedWithoutRetrying() async {
        let calls = Counter503()
        MockURLProtocol.setHandler(forHost: host) { req in
            _ = calls.next()
            return MockURLProtocol.respond(status: 503, headers: ["Retry-After": "0"],
                                           body: Data(), for: req.url!)
        }
        defer { MockURLProtocol.removeHandler(forHost: host) }

        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        if case .unreachable = await probe.probe(baseURL: baseURL, credentials: creds) {} else {
            Issue.record("expected .unreachable")
        }
        #expect(calls.count == 1)
    }
}
