import Testing
import Foundation
@testable import FilesNestCore

struct FakeCredentialStore: CredentialStore {
    var creds: BasicCredentials?
    func basicCredentials() async throws -> BasicCredentials? { creds }
}

@Test func defaultSessionDoesNotCacheResponsesOrRedirects() {
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
                              credentials: FakeCredentialStore(creds: nil))

    #expect(client.session.configuration.urlCache == nil)
    #expect(client.session.configuration.requestCachePolicy == .reloadIgnoringLocalCacheData)
}

@Test func retryPolicyUsesFifteenRetriesAndCapsAtOneMinute() {
    #expect(ServerClient.defaultMaxRetries == 15)
    #expect(ServerClient.backoffDelay(forRetry: 0) == 1)
    #expect(ServerClient.backoffDelay(forRetry: 5) == 32)
    #expect(ServerClient.backoffDelay(forRetry: 6) == 60)
    #expect(ServerClient.backoffDelay(forRetry: 14) == 60)
    #expect(ServerClient.retryDelay(for: .serviceUnavailable(retryAfter: 120), retry: 0) == 60)
    for _ in 0..<100 {
        #expect(ServerClient.jittered(60) <= ServerClient.maxBackoffDelay)
    }
}

@Test func fakeCredentialStoreReturnsValue() async throws {
    let store = FakeCredentialStore(creds: .init(username: "u", password: "p"))
    #expect(try await store.basicCredentials() == .init(username: "u", password: "p"))
}

@Test func dataURLJoinsWithoutDoubleSlash() throws {
    for base in ["https://h.test", "https://h.test/", "https://h.test/api", "https://h.test/api/"] {
        let client = ServerClient(baseURL: URL(string: base)!,
                                  credentials: FakeCredentialStore(creds: nil),
                                  session: MockURLProtocol.makeSession())
        let url = client.dataURL(for: "ID1")
        #expect(url.absoluteString.hasSuffix("/uploads/ID1/data"))
        #expect(!url.absoluteString.contains("//uploads"))
    }
}

@Test func authHeaderPresentWhenCredsExist() async throws {
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: .init(username: "u", password: "p")),
        session: MockURLProtocol.makeSession())
    let req = try await client.authorizedRequest(URL(string: "https://h.test/uploads")!, method: "GET")
    let expected = "Basic " + Data("u:p".utf8).base64EncodedString()
    #expect(req.value(forHTTPHeaderField: "Authorization") == expected)
}

@Test func noAuthHeaderWhenCredsNil() async throws {
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    let req = try await client.authorizedRequest(URL(string: "https://h.test/uploads")!, method: "GET")
    #expect(req.value(forHTTPHeaderField: "Authorization") == nil)
}

@Test func configDecodesMaxConcurrentUploads() async throws {
    let host = "sc-config.test"
    // NB: don't call #expect inside the handler — it runs on URLSession's worker
    // thread where swift-testing can't associate it. The decoded result below is
    // the assertion; the handler only serves /config for this host.
    MockURLProtocol.setHandler(forHost: host) { req in
        let body = #"{"maxConcurrentUploads": 7}"#.data(using: .utf8)!
        return MockURLProtocol.respond(status: 200,
                                       headers: ["Content-Type": "application/json"],
                                       body: body, for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession())
    let cfg = try await client.config()
    #expect(cfg == ServerConfig(maxConcurrentUploads: 7))
}

@Test func configThrowsNotFoundOnOldServer() async throws {
    let host = "sc-config-404.test"
    MockURLProtocol.setHandler(forHost: host) { req in
        MockURLProtocol.respond(status: 404, body: Data(), for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.notFound) { _ = try await client.config() }
}
