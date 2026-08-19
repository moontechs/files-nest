import Foundation

public struct ServerClient: Sendable {
    let baseURL: URL
    let credentials: any CredentialStore
    let session: URLSession

    public init(baseURL: URL, credentials: any CredentialStore, session: URLSession? = nil) {
        self.baseURL = baseURL
        self.credentials = credentials
        self.session = session ?? Self.makeNonPersistentSession()
    }

    private static func makeNonPersistentSession() -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSession(configuration: configuration)
    }

    // MARK: URL construction (client-side; server's upload_url is ignored)

    func dataURL(for id: String) -> URL {
        baseURL.appendingPathComponent("uploads").appendingPathComponent(id).appendingPathComponent("data")
    }
    func uploadsURL() -> URL { baseURL.appendingPathComponent("uploads") }
    func uploadURL(id: String) -> URL { baseURL.appendingPathComponent("uploads").appendingPathComponent(id) }

    // MARK: Request building + sending

    func authorizedRequest(_ url: URL, method: String) async throws -> URLRequest {
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let c = try await credentials.basicCredentials() {
            let token = Data("\(c.username):\(c.password)".utf8).base64EncodedString()
            req.setValue("Basic \(token)", forHTTPHeaderField: "Authorization")
        }
        return req
    }

    @discardableResult
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            // Cancellation must stay cancellation. URLSession reports a cancelled
            // task as URLError(.cancelled) (-999), and wrapping that in .transport
            // forced callers to string-match the very thing this typed error exists
            // to avoid — SyncCoordinator needs to tell "user cancelled" apart from
            // "network died" to decide whether to retry.
            if error is CancellationError { throw error }
            if Task.isCancelled { throw CancellationError() }
            throw ServerClientError.transport(String(describing: error))
        }
        guard let http = response as? HTTPURLResponse else {
            throw ServerClientError.transport("non-HTTP response")
        }
        if let err = ServerClientError.map(status: http.statusCode, body: data) { throw err }
        return (data, http)
    }

    func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw ServerClientError.decoding(String(describing: error))
        }
    }

    // MARK: Upload lifecycle

    /// POST /uploads — returns the record for both 201 (created/re-registered)
    /// and 200 (already exists); callers branch on `record.status`.
    public func createUpload(_ request: CreateUploadRequest) async throws -> UploadRecord {
        var req = try await authorizedRequest(uploadsURL(), method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        req.httpBody = try encoder.encode(request)
        let (data, _) = try await send(req)
        return try decode(UploadRecord.self, from: data)
    }

    /// GET /uploads — cursor-paginated. `nextCursor` is nil when there are no more pages.
    public func listUploads(cursor: String?) async throws -> UploadPage {
        var comps = URLComponents(url: uploadsURL(), resolvingAgainstBaseURL: false)!
        if let cursor { comps.queryItems = [URLQueryItem(name: "cursor", value: cursor)] }
        let req = try await authorizedRequest(comps.url!, method: "GET")
        let (data, _) = try await send(req)
        // `items` is optional: Go marshals a nil slice as `null`, not `[]`.
        struct Wire: Decodable {
            let items: [UploadRecord]?
            let nextCursor: String?
            enum CodingKeys: String, CodingKey {
                case items
                case nextCursor = "next_cursor"
            }
        }
        let wire = try decode(Wire.self, from: data)
        let cursor = (wire.nextCursor?.isEmpty ?? true) ? nil : wire.nextCursor
        return UploadPage(items: wire.items ?? [], nextCursor: cursor)
    }

    /// GET /uploads/{id}
    public func getUpload(id: String) async throws -> UploadRecord {
        let req = try await authorizedRequest(uploadURL(id: id), method: "GET")
        let (data, _) = try await send(req)
        return try decode(UploadRecord.self, from: data)
    }

    // MARK: TUS data endpoints

    /// HEAD /uploads/{id}/data — the current offset, for resuming.
    /// `length` is nil when the server reports `Upload-Defer-Length` (size not yet declared).
    public func offset(forUploadID id: String) async throws -> UploadOffset {
        let req = try await authorizedRequest(dataURL(for: id), method: "HEAD")
        let (_, http) = try await send(req)
        guard let offsetString = http.value(forHTTPHeaderField: "Upload-Offset"),
              let offset = Int64(offsetString) else {
            throw ServerClientError.decoding("missing or invalid Upload-Offset header")
        }
        let length = http.value(forHTTPHeaderField: "Upload-Length").flatMap(Int64.init)
        return UploadOffset(offset: offset, length: length)
    }

    /// PATCH /uploads/{id}/data — appends `data` at `offset`.
    /// Pass `finalLength` on the last chunk to declare the total size for a
    /// deferred-length upload. Returns the server's new `Upload-Offset`.
    ///
    /// Note: `data` is a single already-bounded chunk — this method never
    /// accumulates, so its memory cost is O(one chunk).
    @discardableResult
    public func patchData(uploadID id: String, offset: Int64, data: Data,
                          finalLength: Int64?) async throws -> Int64 {
        var req = try await authorizedRequest(dataURL(for: id), method: "PATCH")
        req.setValue("application/offset+octet-stream", forHTTPHeaderField: "Content-Type")
        req.setValue(String(offset), forHTTPHeaderField: "Upload-Offset")
        req.setValue("1.0.0", forHTTPHeaderField: "Tus-Resumable")
        if let finalLength {
            req.setValue(String(finalLength), forHTTPHeaderField: "Upload-Length")
        }
        req.httpBody = data
        let (_, http) = try await send(req)
        guard let offsetString = http.value(forHTTPHeaderField: "Upload-Offset"),
              let newOffset = Int64(offsetString) else {
            throw ServerClientError.decoding("missing Upload-Offset in PATCH response")
        }
        return newOffset
    }

    // MARK: Status transition and deletion

    /// PATCH /uploads/{id}/status — the server only accepts "complete"; it moves
    /// the file from incoming to organized storage before flipping the status.
    public func markComplete(uploadID id: String) async throws {
        let url = uploadURL(id: id).appendingPathComponent("status")
        var req = try await authorizedRequest(url, method: "PATCH")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = #"{"status":"complete"}"#.data(using: .utf8)
        try await send(req)
    }

    /// DELETE /uploads/{id} — the server performs the TUS termination.
    public func deleteUpload(id: String) async throws {
        let req = try await authorizedRequest(uploadURL(id: id), method: "DELETE")
        try await send(req)
    }
}
