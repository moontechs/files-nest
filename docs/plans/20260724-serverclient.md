# ServerClient Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `ServerClient`, the HTTP client in `FilesNestCore` that speaks the Go server's TUS + Basic-Auth API, as a fully unit-tested SwiftPM package.

**Architecture:** A `Sendable` struct with 7 `async throws` methods over an injected `URLSession`. Requests are built client-side; responses map to `Codable & Sendable` models or a typed `ServerClientError` (409 disambiguated by error string). Auth comes through a `CredentialStore` protocol seam so the client never touches Keychain. Tests stub the wire with a `URLProtocol` subclass — no network, no server.

**Tech Stack:** Swift 6, Foundation (`URLSession`, `Codable`), Swift Testing (`import Testing`), SwiftPM.

## Global Constraints

- Swift 6 language mode, complete concurrency checking, **zero warnings** (`swift-tools-version: 6.0`).
- Platforms: `.macOS(.v15)`, `.iOS(.v17)`.
- Pure Foundation only — **no** Keychain, PhotoKit, SwiftUI, or app-target dependency.
- Wire JSON is **snake_case**; requests send snake_case via explicit `CodingKeys`.
- Dates on the wire are **RFC3339** strings.
- All tests run headless via `swift test` (URLProtocol stubs, no network).
- Data endpoint URL is built client-side as `baseURL/uploads/{id}/data`; the server's `upload_url` is ignored.
- Commit after every green task.

Working dir: `apple/FilesNestCore/`. Branch: `apple-clients`.

---

## File Structure

```
apple/FilesNestCore/
  Package.swift
  Sources/FilesNestCore/
    Models/
      UploadStatus.swift        // enum
      UploadRecord.swift        // decoded server record
      CreateUploadRequest.swift // POST body
      UploadPage.swift          // list response
      UploadOffset.swift        // HEAD result
      BasicCredentials.swift    // username/password
    CredentialStore.swift       // auth seam protocol
    ServerClientError.swift     // typed errors + mapping
    ServerClient.swift          // the client
  Tests/FilesNestCoreTests/
    MockURLProtocol.swift       // URLProtocol stub + helpers
    ModelCodingTests.swift
    ServerClientErrorTests.swift
    ServerClientTests.swift
```

---

### Task 1: SwiftPM package skeleton

**Files:**
- Create: `apple/FilesNestCore/Package.swift`
- Delete: `apple/FilesNestCore/.gitkeep`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/FilesNestCore.swift` (temporary marker, removed in Task 2)
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/SmokeTests.swift`

**Interfaces:**
- Produces: a buildable package named `FilesNestCore` with a `FilesNestCore` library target and `FilesNestCoreTests` test target.

- [ ] **Step 1: Write `Package.swift`**

```swift
// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "FilesNestCore",
    platforms: [.macOS(.v15), .iOS(.v17)],
    products: [
        .library(name: "FilesNestCore", targets: ["FilesNestCore"]),
    ],
    targets: [
        .target(name: "FilesNestCore"),
        .testTarget(name: "FilesNestCoreTests", dependencies: ["FilesNestCore"]),
    ]
)
```

- [ ] **Step 2: Add a temporary marker source** (`Sources/FilesNestCore/FilesNestCore.swift`)

```swift
// Placeholder; replaced by real types in Task 2.
enum FilesNestCore {}
```

- [ ] **Step 3: Write a smoke test** (`Tests/FilesNestCoreTests/SmokeTests.swift`)

```swift
import Testing
@testable import FilesNestCore

@Test func packageBuilds() {
    #expect(Bool(true))
}
```

- [ ] **Step 4: Build and test**

Run: `cd apple/FilesNestCore && swift test`
Expected: builds with no warnings; 1 test passes.

- [ ] **Step 5: Remove the `.gitkeep`, commit**

```bash
git rm apple/FilesNestCore/.gitkeep
git add apple/FilesNestCore
git commit -m "feat(core): scaffold FilesNestCore SwiftPM package (Swift 6)"
```

---

### Task 2: Data models + Codable

**Files:**
- Create: `Sources/FilesNestCore/Models/UploadStatus.swift`, `UploadRecord.swift`, `CreateUploadRequest.swift`, `UploadPage.swift`, `UploadOffset.swift`, `BasicCredentials.swift`
- Delete: `Sources/FilesNestCore/FilesNestCore.swift`
- Test: `Tests/FilesNestCoreTests/ModelCodingTests.swift`

**Interfaces:**
- Produces:
  - `enum UploadStatus: String, Codable, Sendable { case uploading, complete, deleted, backendLost }` (raw `backend_lost`)
  - `struct UploadRecord: Codable, Sendable, Equatable` — `id, localIdentifier: String; status: UploadStatus; backendID: String; filename, bundleID, creationDate, createdAt, updatedAt: String?`
  - `struct CreateUploadRequest: Codable, Sendable, Equatable` — `localIdentifier, filename: String; creationDate: Date; bundleID: String?`
  - `struct UploadPage: Sendable, Equatable` — `items: [UploadRecord]; nextCursor: String?`
  - `struct UploadOffset: Sendable, Equatable` — `offset: Int64; length: Int64?`
  - `struct BasicCredentials: Sendable, Equatable` — `username, password: String`

- [ ] **Step 1: Write failing decode test** (`ModelCodingTests.swift`)

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Test func decodeUploadRecordFromServerJSON() throws {
    let json = """
    {"id":"abc","local_identifier":"LID","status":"uploading","backend_id":"b1",
     "filename":"IMG_1.jpg","bundle_id":"","creation_date":"2024-03-15T10:30:00Z",
     "created_at":"2024-03-15T10:30:00Z","updated_at":"2024-03-15T10:30:00Z"}
    """.data(using: .utf8)!
    let rec = try JSONDecoder().decode(UploadRecord.self, from: json)
    #expect(rec.id == "abc")
    #expect(rec.localIdentifier == "LID")
    #expect(rec.status == .uploading)
    #expect(rec.filename == "IMG_1.jpg")
}

@Test func decodeBackendLostStatus() throws {
    let rec = try JSONDecoder().decode(UploadRecord.self,
        from: #"{"id":"x","local_identifier":"l","status":"backend_lost","backend_id":""}"#.data(using: .utf8)!)
    #expect(rec.status == .backendLost)
}

@Test func encodeCreateRequestUsesSnakeCaseAndRFC3339() throws {
    let req = CreateUploadRequest(
        localIdentifier: "LID", filename: "IMG_1.jpg",
        creationDate: Date(timeIntervalSince1970: 1_710_498_600), bundleID: nil)
    let enc = JSONEncoder(); enc.dateEncodingStrategy = .iso8601
    let obj = try JSONSerialization.jsonObject(with: enc.encode(req)) as! [String: Any]
    #expect(obj["local_identifier"] as? String == "LID")
    #expect(obj["creation_date"] as? String == "2024-03-15T10:30:00Z")
    #expect(obj["bundle_id"] == nil)  // nil omitted
}
```

- [ ] **Step 2: Run, verify failure**

Run: `swift test --filter ModelCodingTests`
Expected: FAIL (types don't exist).

- [ ] **Step 3: Implement the models**

`UploadStatus.swift`:
```swift
public enum UploadStatus: String, Codable, Sendable {
    case uploading, complete, deleted
    case backendLost = "backend_lost"
}
```

`UploadRecord.swift`:
```swift
public struct UploadRecord: Codable, Sendable, Equatable {
    public let id: String
    public let localIdentifier: String
    public let status: UploadStatus
    public let backendID: String
    public let filename: String?
    public let bundleID: String?
    public let creationDate: String?
    public let createdAt: String?
    public let updatedAt: String?

    enum CodingKeys: String, CodingKey {
        case id, status, filename
        case localIdentifier = "local_identifier"
        case backendID = "backend_id"
        case bundleID = "bundle_id"
        case creationDate = "creation_date"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}
```

`CreateUploadRequest.swift`:
```swift
import Foundation

public struct CreateUploadRequest: Codable, Sendable, Equatable {
    public let localIdentifier: String
    public let filename: String
    public let creationDate: Date
    public let bundleID: String?

    public init(localIdentifier: String, filename: String, creationDate: Date, bundleID: String?) {
        self.localIdentifier = localIdentifier
        self.filename = filename
        self.creationDate = creationDate
        self.bundleID = bundleID
    }

    enum CodingKeys: String, CodingKey {
        case filename
        case localIdentifier = "local_identifier"
        case creationDate = "creation_date"
        case bundleID = "bundle_id"
    }
}
```

`UploadPage.swift`:
```swift
public struct UploadPage: Sendable, Equatable {
    public let items: [UploadRecord]
    public let nextCursor: String?
}
```

`UploadOffset.swift`:
```swift
public struct UploadOffset: Sendable, Equatable {
    public let offset: Int64
    public let length: Int64?
}
```

`BasicCredentials.swift`:
```swift
public struct BasicCredentials: Sendable, Equatable {
    public let username: String
    public let password: String
    public init(username: String, password: String) {
        self.username = username
        self.password = password
    }
}
```

Then delete the placeholder: `rm Sources/FilesNestCore/FilesNestCore.swift`.

- [ ] **Step 4: Run tests, verify pass**

Run: `swift test --filter ModelCodingTests`
Expected: PASS. Also `swift build` — zero warnings.

- [ ] **Step 5: Commit**

```bash
git add apple/FilesNestCore
git commit -m "feat(core): add ServerClient data models"
```

---

### Task 3: Typed errors + response mapping

**Files:**
- Create: `Sources/FilesNestCore/ServerClientError.swift`
- Test: `Tests/FilesNestCoreTests/ServerClientErrorTests.swift`

**Interfaces:**
- Produces:
  - `enum ServerClientError: Error, Sendable, Equatable` with cases: `unauthorized, notFound, backendLost, alreadyCompleted, notUploading, offsetConflict, badRequest(message: String), requestTooLarge, unexpectedStatus(code: Int, message: String?), decoding(String), transport(String)`
  - `static func map(status: Int, body: Data) -> ServerClientError?` — returns the error for a non-2xx status (nil for 2xx). Uses substring matching on the `{"error":...}` body for 409.

- [ ] **Step 1: Write failing tests**

```swift
import Testing
import Foundation
@testable import FilesNestCore

private func body(_ s: String) -> Data { #"{"error":"\#(s)"}"#.data(using: .utf8)! }

@Test func map409BackendLost() {
    #expect(ServerClientError.map(status: 409, body: body("backend_lost")) == .backendLost)
}
@Test func map409OffsetMismatchPrefix() {
    #expect(ServerClientError.map(status: 409, body: body("offset mismatch: client=5, server=10")) == .offsetConflict)
}
@Test func map409AlreadyCompleted() {
    #expect(ServerClientError.map(status: 409, body: body("upload already completed or deleted")) == .alreadyCompleted)
}
@Test func map409NotUploading() {
    #expect(ServerClientError.map(status: 409, body: body("upload not in uploading state")) == .notUploading)
}
@Test func mapStandardCodes() {
    #expect(ServerClientError.map(status: 401, body: Data()) == .unauthorized)
    #expect(ServerClientError.map(status: 404, body: body("upload not found")) == .notFound)
    #expect(ServerClientError.map(status: 413, body: Data()) == .requestTooLarge)
    #expect(ServerClientError.map(status: 400, body: body("bad filename")) == .badRequest(message: "bad filename"))
}
@Test func mapSuccessReturnsNil() {
    #expect(ServerClientError.map(status: 204, body: Data()) == nil)
}
```

- [ ] **Step 2: Run, verify failure** — `swift test --filter ServerClientErrorTests` → FAIL.

- [ ] **Step 3: Implement**

```swift
import Foundation

public enum ServerClientError: Error, Sendable, Equatable {
    case unauthorized
    case notFound
    case backendLost
    case alreadyCompleted
    case notUploading
    case offsetConflict
    case badRequest(message: String)
    case requestTooLarge
    case unexpectedStatus(code: Int, message: String?)
    case decoding(String)
    case transport(String)

    static func errorMessage(from body: Data) -> String? {
        struct E: Decodable { let error: String }
        return (try? JSONDecoder().decode(E.self, from: body))?.error
    }

    static func map(status: Int, body: Data) -> ServerClientError? {
        if (200..<300).contains(status) { return nil }
        let msg = errorMessage(from: body)
        switch status {
        case 401: return .unauthorized
        case 404: return .notFound
        case 413: return .requestTooLarge
        case 409:
            let m = msg ?? ""
            if m.contains("backend_lost") { return .backendLost }
            if m.hasPrefix("offset mismatch") || m.contains("offset mismatch") { return .offsetConflict }
            if m.contains("already completed") || m.contains("already deleted") { return .alreadyCompleted }
            if m.contains("not in uploading") { return .notUploading }
            return .unexpectedStatus(code: 409, message: msg)
        case 400: return .badRequest(message: msg ?? "")
        default: return .unexpectedStatus(code: status, message: msg)
        }
    }
}
```

- [ ] **Step 4: Run tests, verify pass** — `swift test --filter ServerClientErrorTests` → PASS.

- [ ] **Step 5: Commit**

```bash
git add apple/FilesNestCore
git commit -m "feat(core): add ServerClientError with 409 branching"
```

---

### Task 4: CredentialStore seam

**Files:**
- Create: `Sources/FilesNestCore/CredentialStore.swift`
- Test: add `FakeCredentialStore` + a test to `Tests/FilesNestCoreTests/ServerClientTests.swift` (created here, extended later)

**Interfaces:**
- Produces: `public protocol CredentialStore: Sendable { func basicCredentials() async throws -> BasicCredentials? }`

- [ ] **Step 1: Write the protocol**

```swift
public protocol CredentialStore: Sendable {
    func basicCredentials() async throws -> BasicCredentials?
}
```

- [ ] **Step 2: Add a test fake** (top of `ServerClientTests.swift`)

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct FakeCredentialStore: CredentialStore {
    var creds: BasicCredentials?
    func basicCredentials() async throws -> BasicCredentials? { creds }
}

@Test func fakeCredentialStoreReturnsValue() async throws {
    let store = FakeCredentialStore(creds: .init(username: "u", password: "p"))
    #expect(try await store.basicCredentials() == .init(username: "u", password: "p"))
}
```

- [ ] **Step 3: Run** — `swift test --filter ServerClientTests` → PASS.

- [ ] **Step 4: Commit**

```bash
git add apple/FilesNestCore
git commit -m "feat(core): add CredentialStore auth seam"
```

---

### Task 5: MockURLProtocol + ServerClient skeleton (URL join + auth header)

**Files:**
- Create: `Tests/FilesNestCoreTests/MockURLProtocol.swift`
- Create: `Sources/FilesNestCore/ServerClient.swift`
- Modify: `Tests/FilesNestCoreTests/ServerClientTests.swift`

**Interfaces:**
- Produces:
  - `MockURLProtocol` with `static var handler: (@Sendable (URLRequest) throws -> (HTTPURLResponse, Data))?` and a helper `makeSession()`.
  - `struct ServerClient: Sendable { init(baseURL: URL, credentials: any CredentialStore, session: URLSession) }`
  - `dataURL(for id: String) -> URL` = `baseURL/uploads/{id}/data` (internal, tested directly)
  - `authorizedRequest(_ url: URL, method: String) async throws -> URLRequest` that adds `Authorization: Basic ...` when creds exist (internal, tested directly)
- Consumes: `CredentialStore`, `BasicCredentials`.

- [ ] **Step 1: Write `MockURLProtocol`**

```swift
import Foundation

final class MockURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let handler = MockURLProtocol.handler else {
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
```

- [ ] **Step 2: Write failing tests for URL join + auth header**

```swift
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
```

*(These test `dataURL` and `authorizedRequest` directly via `@testable import` — no network, no forward dependency on later tasks.)*

- [ ] **Step 3: Implement the skeleton**

```swift
import Foundation

public struct ServerClient: Sendable {
    let baseURL: URL
    let credentials: any CredentialStore
    let session: URLSession

    public init(baseURL: URL, credentials: any CredentialStore, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.credentials = credentials
        self.session = session
    }

    func dataURL(for id: String) -> URL {
        baseURL.appendingPathComponent("uploads").appendingPathComponent(id).appendingPathComponent("data")
    }
    func uploadsURL() -> URL { baseURL.appendingPathComponent("uploads") }
    func uploadURL(id: String) -> URL { baseURL.appendingPathComponent("uploads").appendingPathComponent(id) }

    func authorizedRequest(_ url: URL, method: String) async throws -> URLRequest {
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let c = try await credentials.basicCredentials() {
            let token = Data("\(c.username):\(c.password)".utf8).base64EncodedString()
            req.setValue("Basic \(token)", forHTTPHeaderField: "Authorization")
        }
        return req
    }

    // Shared send + status check + decode.
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let data: Data, response: URLResponse
        do { (data, response) = try await session.data(for: request) }
        catch { throw ServerClientError.transport(String(describing: error)) }
        guard let http = response as? HTTPURLResponse else { throw ServerClientError.transport("non-HTTP response") }
        if let err = ServerClientError.map(status: http.statusCode, body: data) { throw err }
        return (data, http)
    }

    func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do { return try JSONDecoder().decode(T.self, from: data) }
        catch { throw ServerClientError.decoding(String(describing: error)) }
    }
}
```

- [ ] **Step 4: Run** — `swift test --filter ServerClientTests` → URL-join and auth-header tests pass (they exercise `dataURL` / `authorizedRequest` directly, no network).

- [ ] **Step 5: Commit**

```bash
git add apple/FilesNestCore
git commit -m "feat(core): ServerClient skeleton with URL join + Basic auth"
```

---

### Task 6: createUpload + listUploads + getUpload

**Files:**
- Modify: `Sources/FilesNestCore/ServerClient.swift`
- Modify: `Tests/FilesNestCoreTests/ServerClientTests.swift`

**Interfaces:**
- Produces:
  - `func createUpload(_ request: CreateUploadRequest) async throws -> UploadRecord`
  - `func listUploads(cursor: String?) async throws -> UploadPage`
  - `func getUpload(id: String) async throws -> UploadRecord`

- [ ] **Step 1: Write failing tests**

```swift
@Test func createUploadPostsBodyAndDecodes() async throws {
    nonisolated(unsafe) var captured: URLRequest?
    nonisolated(unsafe) var bodyData: Data?
    MockURLProtocol.handler = { req in
        captured = req; bodyData = req.httpBodyStreamData()
        return MockURLProtocol.respond(status: 201,
            body: #"{"id":"NEW","local_identifier":"L","status":"uploading","backend_id":"b","upload_url":"/uploads/NEW/data"}"#.data(using: .utf8)!,
            for: req.url!)
    }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    let rec = try await client.createUpload(.init(localIdentifier: "L", filename: "IMG.jpg",
        creationDate: Date(timeIntervalSince1970: 1_710_498_600), bundleID: nil))
    #expect(captured?.httpMethod == "POST")
    #expect(captured?.url?.absoluteString == "https://h.test/uploads")
    #expect(rec.id == "NEW"); #expect(rec.status == .uploading)
    let obj = try JSONSerialization.jsonObject(with: bodyData!) as! [String: Any]
    #expect(obj["local_identifier"] as? String == "L")
}

@Test func getUpload404MapsNotFound() async throws {
    MockURLProtocol.handler = { req in
        MockURLProtocol.respond(status: 404, body: #"{"error":"upload not found"}"#.data(using: .utf8)!, for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.notFound) { try await client.getUpload(id: "X") }
}

@Test func listUploadsDecodesPageAndCursor() async throws {
    MockURLProtocol.handler = { req in
        MockURLProtocol.respond(status: 200,
            body: #"{"items":[{"id":"a","local_identifier":"l","status":"complete","backend_id":"b"}],"next_cursor":"c2"}"#.data(using: .utf8)!,
            for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    let page = try await client.listUploads(cursor: "c1")
    #expect(page.items.count == 1); #expect(page.items[0].status == .complete)
    #expect(page.nextCursor == "c2")
}
```

Add this helper at the bottom of the test file (URLProtocol delivers `httpBodyStream`, not `httpBody`):
```swift
extension URLRequest {
    func httpBodyStreamData() -> Data? {
        if let b = httpBody { return b }
        guard let stream = httpBodyStream else { return nil }
        stream.open(); defer { stream.close() }
        var data = Data(); let size = 4096; let buf = UnsafeMutablePointer<UInt8>.allocate(capacity: size)
        defer { buf.deallocate() }
        while stream.hasBytesAvailable {
            let read = stream.read(buf, maxLength: size); if read <= 0 { break }
            data.append(buf, count: read)
        }
        return data
    }
}
```

- [ ] **Step 2: Run, verify failure** — `swift test --filter ServerClientTests` → FAIL.

- [ ] **Step 3: Implement the three methods** (append to `ServerClient`)

```swift
public func createUpload(_ request: CreateUploadRequest) async throws -> UploadRecord {
    var req = try await authorizedRequest(uploadsURL(), method: "POST")
    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    let enc = JSONEncoder(); enc.dateEncodingStrategy = .iso8601
    req.httpBody = try enc.encode(request)
    let (data, _) = try await send(req)
    return try decode(UploadRecord.self, from: data)
}

public func listUploads(cursor: String?) async throws -> UploadPage {
    var comps = URLComponents(url: uploadsURL(), resolvingAgainstBaseURL: false)!
    if let cursor { comps.queryItems = [URLQueryItem(name: "cursor", value: cursor)] }
    let req = try await authorizedRequest(comps.url!, method: "GET")
    let (data, _) = try await send(req)
    struct Wire: Decodable { let items: [UploadRecord]; let next_cursor: String? }
    let w = try decode(Wire.self, from: data)
    return UploadPage(items: w.items, nextCursor: (w.next_cursor?.isEmpty ?? true) ? nil : w.next_cursor)
}

public func getUpload(id: String) async throws -> UploadRecord {
    let req = try await authorizedRequest(uploadURL(id: id), method: "GET")
    let (data, _) = try await send(req)
    return try decode(UploadRecord.self, from: data)
}
```

- [ ] **Step 4: Run tests, verify pass** — `swift test --filter ServerClientTests` → PASS (incl. Task 5's auth tests).

- [ ] **Step 5: Commit**

```bash
git add apple/FilesNestCore
git commit -m "feat(core): createUpload, listUploads, getUpload"
```

---

### Task 7: offset (HEAD)

**Files:**
- Modify: `Sources/FilesNestCore/ServerClient.swift`, `Tests/FilesNestCoreTests/ServerClientTests.swift`

**Interfaces:**
- Produces: `func offset(forUploadID id: String) async throws -> UploadOffset`

- [ ] **Step 1: Write failing tests**

```swift
@Test func headParsesOffsetAndLength() async throws {
    MockURLProtocol.handler = { req in
        #expect(req.httpMethod == "HEAD")
        return MockURLProtocol.respond(status: 200,
            headers: ["Upload-Offset": "500", "Upload-Length": "2048", "Tus-Resumable": "1.0.0"],
            for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    let o = try await client.offset(forUploadID: "ID")
    #expect(o.offset == 500); #expect(o.length == 2048)
}

@Test func headDeferredLengthIsNil() async throws {
    MockURLProtocol.handler = { req in
        MockURLProtocol.respond(status: 200,
            headers: ["Upload-Offset": "0", "Upload-Defer-Length": "1", "Tus-Resumable": "1.0.0"], for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    let o = try await client.offset(forUploadID: "ID")
    #expect(o.offset == 0); #expect(o.length == nil)
}

@Test func head409BackendLost() async throws {
    MockURLProtocol.handler = { req in
        MockURLProtocol.respond(status: 409, body: #"{"error":"backend_lost"}"#.data(using: .utf8)!, for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.backendLost) { try await client.offset(forUploadID: "ID") }
}
```

- [ ] **Step 2: Run, verify failure.**

- [ ] **Step 3: Implement**

```swift
public func offset(forUploadID id: String) async throws -> UploadOffset {
    let req = try await authorizedRequest(dataURL(for: id), method: "HEAD")
    let (_, http) = try await send(req)
    func header(_ n: String) -> String? { http.value(forHTTPHeaderField: n) }
    guard let offStr = header("Upload-Offset"), let off = Int64(offStr) else {
        throw ServerClientError.decoding("missing/invalid Upload-Offset")
    }
    let len = header("Upload-Length").flatMap(Int64.init)
    return UploadOffset(offset: off, length: len)
}
```

- [ ] **Step 4: Run tests, verify pass.**

- [ ] **Step 5: Commit** — `git commit -m "feat(core): offset(forUploadID:) via TUS HEAD"`

---

### Task 8: patchData (PATCH)

**Files:**
- Modify: `Sources/FilesNestCore/ServerClient.swift`, `Tests/FilesNestCoreTests/ServerClientTests.swift`

**Interfaces:**
- Produces: `func patchData(uploadID id: String, offset: Int64, data: Data, finalLength: Int64?) async throws -> Int64`

- [ ] **Step 1: Write failing tests**

```swift
@Test func patchSendsTusHeadersAndReturnsNewOffset() async throws {
    nonisolated(unsafe) var captured: URLRequest?
    MockURLProtocol.handler = { req in captured = req
        return MockURLProtocol.respond(status: 204, headers: ["Upload-Offset": "1024"], for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    let newOff = try await client.patchData(uploadID: "ID", offset: 512, data: Data(repeating: 7, count: 512), finalLength: nil)
    #expect(newOff == 1024)
    #expect(captured?.httpMethod == "PATCH")
    #expect(captured?.value(forHTTPHeaderField: "Content-Type") == "application/offset+octet-stream")
    #expect(captured?.value(forHTTPHeaderField: "Upload-Offset") == "512")
    #expect(captured?.value(forHTTPHeaderField: "Tus-Resumable") == "1.0.0")
    #expect(captured?.value(forHTTPHeaderField: "Upload-Length") == nil)
}

@Test func patchFinalChunkDeclaresUploadLength() async throws {
    nonisolated(unsafe) var captured: URLRequest?
    MockURLProtocol.handler = { req in captured = req
        return MockURLProtocol.respond(status: 204, headers: ["Upload-Offset": "2048"], for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    _ = try await client.patchData(uploadID: "ID", offset: 1024, data: Data(count: 1024), finalLength: 2048)
    #expect(captured?.value(forHTTPHeaderField: "Upload-Length") == "2048")
}

@Test func patch409OffsetMismatch() async throws {
    MockURLProtocol.handler = { req in
        MockURLProtocol.respond(status: 409,
            body: #"{"error":"offset mismatch: client=5, server=10"}"#.data(using: .utf8)!, for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.offsetConflict) {
        try await client.patchData(uploadID: "ID", offset: 5, data: Data(count: 8), finalLength: nil)
    }
}
```

- [ ] **Step 2: Run, verify failure.**

- [ ] **Step 3: Implement**

```swift
public func patchData(uploadID id: String, offset: Int64, data: Data, finalLength: Int64?) async throws -> Int64 {
    var req = try await authorizedRequest(dataURL(for: id), method: "PATCH")
    req.setValue("application/offset+octet-stream", forHTTPHeaderField: "Content-Type")
    req.setValue(String(offset), forHTTPHeaderField: "Upload-Offset")
    req.setValue("1.0.0", forHTTPHeaderField: "Tus-Resumable")
    if let finalLength { req.setValue(String(finalLength), forHTTPHeaderField: "Upload-Length") }
    req.httpBody = data
    let (_, http) = try await send(req)
    guard let offStr = http.value(forHTTPHeaderField: "Upload-Offset"), let newOff = Int64(offStr) else {
        throw ServerClientError.decoding("missing Upload-Offset in PATCH response")
    }
    return newOff
}
```

- [ ] **Step 4: Run tests, verify pass.**

- [ ] **Step 5: Commit** — `git commit -m "feat(core): patchData via TUS PATCH"`

---

### Task 9: markComplete + deleteUpload

**Files:**
- Modify: `Sources/FilesNestCore/ServerClient.swift`, `Tests/FilesNestCoreTests/ServerClientTests.swift`

**Interfaces:**
- Produces: `func markComplete(uploadID id: String) async throws`, `func deleteUpload(id: String) async throws`

- [ ] **Step 1: Write failing tests**

```swift
@Test func markCompletePatchesStatus() async throws {
    nonisolated(unsafe) var captured: URLRequest?
    nonisolated(unsafe) var bodyData: Data?
    MockURLProtocol.handler = { req in captured = req; bodyData = req.httpBodyStreamData()
        return MockURLProtocol.respond(status: 200,
            body: #"{"id":"ID","local_identifier":"l","status":"complete","backend_id":"b"}"#.data(using: .utf8)!, for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    try await client.markComplete(uploadID: "ID")
    #expect(captured?.httpMethod == "PATCH")
    #expect(captured?.url?.absoluteString == "https://h.test/uploads/ID/status")
    let obj = try JSONSerialization.jsonObject(with: bodyData!) as! [String: Any]
    #expect(obj["status"] as? String == "complete")
}

@Test func markCompleteBackendLostThrows() async throws {
    MockURLProtocol.handler = { req in
        MockURLProtocol.respond(status: 409, body: #"{"error":"backend_lost"}"#.data(using: .utf8)!, for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.backendLost) { try await client.markComplete(uploadID: "ID") }
}

@Test func deleteUploadSendsDelete() async throws {
    nonisolated(unsafe) var captured: URLRequest?
    MockURLProtocol.handler = { req in captured = req
        return MockURLProtocol.respond(status: 204, for: req.url!) }
    let client = ServerClient(baseURL: URL(string: "https://h.test")!,
        credentials: FakeCredentialStore(creds: nil), session: MockURLProtocol.makeSession())
    try await client.deleteUpload(id: "ID")
    #expect(captured?.httpMethod == "DELETE")
    #expect(captured?.url?.absoluteString == "https://h.test/uploads/ID")
}
```

- [ ] **Step 2: Run, verify failure.**

- [ ] **Step 3: Implement**

```swift
public func markComplete(uploadID id: String) async throws {
    let url = uploadURL(id: id).appendingPathComponent("status")
    var req = try await authorizedRequest(url, method: "PATCH")
    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    req.httpBody = #"{"status":"complete"}"#.data(using: .utf8)
    _ = try await send(req)
}

public func deleteUpload(id: String) async throws {
    let req = try await authorizedRequest(uploadURL(id: id), method: "DELETE")
    _ = try await send(req)
}
```

- [ ] **Step 4: Run tests, verify pass.**

- [ ] **Step 5: Commit** — `git commit -m "feat(core): markComplete + deleteUpload"`

---

### Task 10: Full suite green + warnings sweep

**Files:** none new — verification task.

- [ ] **Step 1: Run the whole suite** — `cd apple/FilesNestCore && swift test` → all tests pass.
- [ ] **Step 2: Warnings sweep** — `swift build -Xswiftc -warnings-as-errors` → builds clean (no concurrency or unused warnings).
- [ ] **Step 3: Confirm no forbidden deps** — `grep -rE "import (Security|Photos|SwiftUI|AppKit|UIKit)" Sources/` returns nothing.
- [ ] **Step 4: Commit** (if the sweep required fixes) — `git commit -m "chore(core): ServerClient warnings sweep"`

---

## Verification checklist (maps to spec §9)

- [ ] All 7 endpoints implemented as `async throws` (Tasks 6–9).
- [ ] Typed error mapping incl. 409 branching (Task 3, exercised in 6–9).
- [ ] Basic Auth via injected `CredentialStore` (Tasks 4–5).
- [ ] Swift 6 language mode, zero warnings (Tasks 1, 10).
- [ ] `swift test` green (Task 10).
- [ ] No Keychain / PhotoKit / UI deps (Task 10 Step 3).
