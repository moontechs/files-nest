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
}
