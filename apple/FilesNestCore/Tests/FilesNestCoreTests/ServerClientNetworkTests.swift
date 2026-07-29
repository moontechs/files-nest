import Testing
import Foundation
@testable import FilesNestCore

/// Serialized: every test registers the same per-host handler (`h.test`), and Swift
/// Testing runs a suite's tests in parallel by default.
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
        MockURLProtocol.setHandler(forHost: "h.test") { req in
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
        MockURLProtocol.setHandler(forHost: "h.test") { req in
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
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":[],"next_cursor":""}"#.data(using: .utf8)!, for: req.url!)
        }
        let page = try await makeClient().listUploads(cursor: nil)
        #expect(page.items.isEmpty)
        #expect(page.nextCursor == nil)
    }

    @Test func listUploadsHandlesNullItems() async throws {
        // Go marshals a nil slice as null.
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":null,"next_cursor":""}"#.data(using: .utf8)!, for: req.url!)
        }
        let page = try await makeClient().listUploads(cursor: nil)
        #expect(page.items.isEmpty)
        #expect(page.nextCursor == nil)
    }

    @Test func getUpload404MapsNotFound() async throws {
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 404,
                body: #"{"error":"upload not found"}"#.data(using: .utf8)!, for: req.url!)
        }
        let client = makeClient()
        await #expect(throws: ServerClientError.notFound) {
            try await client.getUpload(id: "X")
        }
    }

    // MARK: TUS data endpoints

    @Test func headParsesOffsetAndLength() async throws {
        nonisolated(unsafe) var captured: URLRequest?
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            captured = req
            return MockURLProtocol.respond(status: 200,
                headers: ["Upload-Offset": "500", "Upload-Length": "2048", "Tus-Resumable": "1.0.0"],
                for: req.url!)
        }
        let result = try await makeClient().offset(forUploadID: "ID")
        #expect(captured?.httpMethod == "HEAD")
        #expect(captured?.url?.absoluteString == "https://h.test/uploads/ID/data")
        #expect(result.offset == 500)
        #expect(result.length == 2048)
    }

    @Test func headDeferredLengthIsNil() async throws {
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 200,
                headers: ["Upload-Offset": "0", "Upload-Defer-Length": "1", "Tus-Resumable": "1.0.0"],
                for: req.url!)
        }
        let result = try await makeClient().offset(forUploadID: "ID")
        #expect(result.offset == 0)
        #expect(result.length == nil)
    }

    @Test func head409BackendLostMapsTyped() async throws {
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 409,
                body: #"{"error":"backend_lost"}"#.data(using: .utf8)!, for: req.url!)
        }
        let client = makeClient()
        await #expect(throws: ServerClientError.backendLost) {
            try await client.offset(forUploadID: "ID")
        }
    }

    @Test func patchSendsTusHeadersAndReturnsNewOffset() async throws {
        nonisolated(unsafe) var captured: URLRequest?
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            captured = req
            return MockURLProtocol.respond(status: 204, headers: ["Upload-Offset": "1024"], for: req.url!)
        }
        let newOffset = try await makeClient().patchData(uploadID: "ID", offset: 512,
            data: Data(repeating: 7, count: 512), finalLength: nil)
        #expect(newOffset == 1024)
        #expect(captured?.httpMethod == "PATCH")
        #expect(captured?.value(forHTTPHeaderField: "Content-Type") == "application/offset+octet-stream")
        #expect(captured?.value(forHTTPHeaderField: "Upload-Offset") == "512")
        #expect(captured?.value(forHTTPHeaderField: "Tus-Resumable") == "1.0.0")
        #expect(captured?.value(forHTTPHeaderField: "Upload-Length") == nil)
    }

    @Test func patchFinalChunkDeclaresUploadLength() async throws {
        nonisolated(unsafe) var captured: URLRequest?
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            captured = req
            return MockURLProtocol.respond(status: 204, headers: ["Upload-Offset": "2048"], for: req.url!)
        }
        _ = try await makeClient().patchData(uploadID: "ID", offset: 1024,
            data: Data(count: 1024), finalLength: 2048)
        #expect(captured?.value(forHTTPHeaderField: "Upload-Length") == "2048")
    }

    @Test func patch409OffsetMismatchMapsTyped() async throws {
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 409,
                body: #"{"error":"offset mismatch: client=5, server=10"}"#.data(using: .utf8)!,
                for: req.url!)
        }
        let client = makeClient()
        await #expect(throws: ServerClientError.offsetConflict) {
            try await client.patchData(uploadID: "ID", offset: 5, data: Data(count: 8), finalLength: nil)
        }
    }

    // MARK: Status transition and deletion

    @Test func markCompletePatchesStatus() async throws {
        nonisolated(unsafe) var captured: URLRequest?
        nonisolated(unsafe) var bodyData: Data?
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            captured = req
            bodyData = req.httpBodyStreamData()
            // The Go handler returns 204 No Content on a successful transition.
            return MockURLProtocol.respond(status: 204, for: req.url!)
        }
        try await makeClient().markComplete(uploadID: "ID")
        #expect(captured?.httpMethod == "PATCH")
        #expect(captured?.url?.absoluteString == "https://h.test/uploads/ID/status")
        let obj = try JSONSerialization.jsonObject(with: #require(bodyData)) as! [String: Any]
        #expect(obj["status"] as? String == "complete")
    }

    @Test func markCompleteBackendLostThrows() async throws {
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            MockURLProtocol.respond(status: 409,
                body: #"{"error":"backend_lost"}"#.data(using: .utf8)!, for: req.url!)
        }
        let client = makeClient()
        await #expect(throws: ServerClientError.backendLost) {
            try await client.markComplete(uploadID: "ID")
        }
    }

    @Test func deleteUploadSendsDelete() async throws {
        nonisolated(unsafe) var captured: URLRequest?
        MockURLProtocol.setHandler(forHost: "h.test") { req in
            captured = req
            return MockURLProtocol.respond(status: 204, for: req.url!)
        }
        try await makeClient().deleteUpload(id: "ID")
        #expect(captured?.httpMethod == "DELETE")
        #expect(captured?.url?.absoluteString == "https://h.test/uploads/ID")
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
