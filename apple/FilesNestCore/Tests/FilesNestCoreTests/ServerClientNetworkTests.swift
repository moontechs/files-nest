import Testing
import Foundation
@testable import FilesNestCore

/// Serialized: these tests share `MockURLProtocol.handler` (static), and Swift
/// Testing runs tests in parallel by default.
@Suite(.serialized)
struct ServerClientNetworkTests {

    func makeClient(creds: BasicCredentials? = nil) -> ServerClient {
        ServerClient(baseURL: URL(string: "https://h.test")!,
                     credentials: FakeCredentialStore(creds: creds),
                     session: MockURLProtocol.makeSession())
    }

    @Test func createUploadPostsBodyAndDecodes() async throws {
        nonisolated(unsafe) var captured: URLRequest?
        nonisolated(unsafe) var bodyData: Data?
        MockURLProtocol.handler = { req in
            captured = req
            bodyData = req.httpBodyStreamData()
            return MockURLProtocol.respond(status: 201,
                body: #"{"id":"NEW","local_identifier":"L","status":"uploading","backend_id":"b","upload_url":"/uploads/NEW/data"}"#.data(using: .utf8)!,
                for: req.url!)
        }
        let client = makeClient()
        let rec = try await client.createUpload(.init(localIdentifier: "L", filename: "IMG.jpg",
            creationDate: Date(timeIntervalSince1970: 1_710_498_600), bundleID: nil))
        #expect(captured?.httpMethod == "POST")
        #expect(captured?.url?.absoluteString == "https://h.test/uploads")
        #expect(rec.id == "NEW")
        #expect(rec.status == .uploading)
        let obj = try JSONSerialization.jsonObject(with: #require(bodyData)) as! [String: Any]
        #expect(obj["local_identifier"] as? String == "L")
        #expect(obj["creation_date"] as? String == "2024-03-15T10:30:00Z")
    }

    @Test func listUploadsDecodesPageAndCursor() async throws {
        MockURLProtocol.handler = { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":[{"id":"a","local_identifier":"l","status":"complete","backend_id":"b"}],"next_cursor":"c2"}"#.data(using: .utf8)!,
                for: req.url!)
        }
        let page = try await makeClient().listUploads(cursor: "c1")
        #expect(page.items.count == 1)
        #expect(page.items[0].status == .complete)
        #expect(page.nextCursor == "c2")
    }

    @Test func listUploadsTreatsEmptyCursorAsNil() async throws {
        MockURLProtocol.handler = { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":[],"next_cursor":""}"#.data(using: .utf8)!, for: req.url!)
        }
        let page = try await makeClient().listUploads(cursor: nil)
        #expect(page.items.isEmpty)
        #expect(page.nextCursor == nil)
    }

    @Test func listUploadsHandlesNullItems() async throws {
        // Go marshals a nil slice as null.
        MockURLProtocol.handler = { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":null,"next_cursor":""}"#.data(using: .utf8)!, for: req.url!)
        }
        let page = try await makeClient().listUploads(cursor: nil)
        #expect(page.items.isEmpty)
        #expect(page.nextCursor == nil)
    }

    @Test func getUpload404MapsNotFound() async throws {
        MockURLProtocol.handler = { req in
            MockURLProtocol.respond(status: 404,
                body: #"{"error":"upload not found"}"#.data(using: .utf8)!, for: req.url!)
        }
        let client = makeClient()
        await #expect(throws: ServerClientError.notFound) {
            try await client.getUpload(id: "X")
        }
    }
}

extension URLRequest {
    /// URLProtocol delivers the body as a stream, not `httpBody`.
    func httpBodyStreamData() -> Data? {
        if let b = httpBody { return b }
        guard let stream = httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        let size = 4096
        let buf = UnsafeMutablePointer<UInt8>.allocate(capacity: size)
        defer { buf.deallocate() }
        while stream.hasBytesAvailable {
            let read = stream.read(buf, maxLength: size)
            if read <= 0 { break }
            data.append(buf, count: read)
        }
        return data
    }
}
