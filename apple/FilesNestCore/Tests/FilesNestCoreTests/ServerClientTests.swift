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
