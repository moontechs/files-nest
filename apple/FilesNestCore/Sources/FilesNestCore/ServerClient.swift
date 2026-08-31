import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

public struct ServerClient: Sendable {
    public static let defaultMaxRetries = 15
    static let maxBackoffDelay: Double = 60
    enum RetryEvent: Sendable {
        case waiting(id: UUID, retryAt: Date)
        case finished(id: UUID)
    }

    let baseURL: URL
    let credentials: any CredentialStore
    let session: URLSession
    /// Maximum retries after the initial request. Kept under its original name so
    /// existing callers can tune the policy while it now applies to every request.
    let maxPatchRetries: Int
    private let retryObserver: (@Sendable (RetryEvent) -> Void)?

    public init(baseURL: URL, credentials: any CredentialStore,
                session: URLSession? = nil, maxPatchRetries: Int = Self.defaultMaxRetries) {
        self.baseURL = baseURL
        self.credentials = credentials
        self.session = session ?? Self.makeNonPersistentSession()
        self.maxPatchRetries = maxPatchRetries
        self.retryObserver = nil
    }

    private init(baseURL: URL, credentials: any CredentialStore, session: URLSession,
                 maxPatchRetries: Int, retryObserver: @escaping @Sendable (RetryEvent) -> Void) {
        self.baseURL = baseURL
        self.credentials = credentials
        self.session = session
        self.maxPatchRetries = maxPatchRetries
        self.retryObserver = retryObserver
    }

    func reportingRetries(to observer: @escaping @Sendable (RetryEvent) -> Void) -> ServerClient {
        ServerClient(baseURL: baseURL, credentials: credentials, session: session,
                     maxPatchRetries: maxPatchRetries, retryObserver: observer)
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
    func configURL() -> URL { baseURL.appendingPathComponent("config") }

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
        var retry = 0
        let retryID = UUID()
        var reportedRetry = false
        defer {
            if reportedRetry { retryObserver?(.finished(id: retryID)) }
        }
        while true {
            do {
                return try await sendOnce(request)
            } catch is CancellationError {
                throw CancellationError()
            } catch let error as ServerClientError where error.isRetryable {
                guard let delay = Self.nextRetryDelay(for: error, retry: &retry,
                                                      maxRetries: maxPatchRetries) else { throw error }
                reportedRetry = true
                retryObserver?(.waiting(id: retryID, retryAt: Date().addingTimeInterval(delay)))
                try await Task.sleep(for: .seconds(delay))
            }
        }
    }

    static func backoffDelay(forRetry retry: Int) -> Double {
        // 1, 2, 4, 8, 16, 32, then one minute for the remaining retries.
        min(pow(2, Double(retry)), maxBackoffDelay)
    }

    static func retryDelay(for error: ServerClientError, retry: Int) -> Double {
        min(error.retryAfter ?? backoffDelay(forRetry: retry), maxBackoffDelay)
    }

    static func jittered(_ delay: Double) -> Double {
        guard delay > 0 else { return 0 }
        return min(delay * Double.random(in: 0.8...1.2), maxBackoffDelay)
    }

    private static func nextRetryDelay(for error: ServerClientError, retry: inout Int,
                                       maxRetries: Int) -> Double? {
        guard retry < maxRetries else { return nil }
        let delay = jittered(retryDelay(for: error, retry: retry))
        retry += 1
        return delay
    }

    @discardableResult
    private func sendOnce(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        // Darwin's URLSession observes the enclosing Task's cancellation and
        // throws promptly from `data(for:)`; swift-corelibs-foundation on Linux
        // does not reliably do the same, so a request racing a `task.cancel()`
        // can complete normally there instead of being abandoned. Check
        // explicitly rather than depending on that platform-specific cooperation.
        try Task.checkCancellation()
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
        if http.statusCode == 503 {
            let retryAfter = http.value(forHTTPHeaderField: "Retry-After").flatMap { Int($0) }
            throw ServerClientError.serviceUnavailable(retryAfter: retryAfter)
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

    /// GET /config — server-advertised limits (e.g. the concurrency cap).
    /// Throws `.notFound` on a server that predates the endpoint.
    public func config() async throws -> ServerConfig {
        let req = try await authorizedRequest(configURL(), method: "GET")
        let (data, _) = try await send(req)
        return try decode(ServerConfig.self, from: data)
    }

    // MARK: TUS data endpoints

    /// HEAD /uploads/{id}/data — the current offset, for resuming.
    /// `length` is nil when the server reports `Upload-Defer-Length` (size not yet declared).
    public func offset(forUploadID id: String) async throws -> UploadOffset {
        let req = try await authorizedRequest(dataURL(for: id), method: "HEAD")
        let http: HTTPURLResponse
        do {
            (_, http) = try await send(req)
        } catch ServerClientError.unexpectedStatus(let code, _) where code == 409 {
            throw await conflictReason(forUploadID: id)
        }
        guard let offsetString = http.value(forHTTPHeaderField: "Upload-Offset"),
              let offset = Int64(offsetString) else {
            throw ServerClientError.decoding("missing or invalid Upload-Offset header")
        }
        let length = http.value(forHTTPHeaderField: "Upload-Length").flatMap(Int64.init)
        return UploadOffset(offset: offset, length: length)
    }

    /// Why a HEAD returned 409. The server writes a `{"error":...}` discriminator, but
    /// HTTP forbids a body on a HEAD response, so it never reaches us and the status code
    /// alone is ambiguous (completed / deleted / lost backend all 409). Re-read the record
    /// to recover the reason — without this, an already-complete upload looks like an
    /// unknown failure and a lost backend never triggers re-registration.
    private func conflictReason(forUploadID id: String) async -> ServerClientError {
        let record: UploadRecord
        do {
            record = try await getUpload(id: id)
        } catch ServerClientError.notFound {
            return .alreadyDeleted        // deleted between the HEAD and this read
        } catch {
            return .unexpectedStatus(code: 409, message: nil)   // can't tell; surface as-is
        }
        switch record.status {
        case .complete:               return .alreadyCompleted
        case .deleted:                return .alreadyDeleted
        case .backendLost:            return .backendLost
        case .uploading, .completing: return .notUploading
        }
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
        try await sendPatch(uploadID: id, offset: offset, data: data, finalLength: finalLength)
    }

    private func sendPatch(uploadID id: String, offset: Int64, data: Data,
                           finalLength: Int64?) async throws -> Int64 {
        var req = try await authorizedRequest(dataURL(for: id), method: "PATCH")
        req.setValue("application/offset+octet-stream", forHTTPHeaderField: "Content-Type")
        req.setValue(String(offset), forHTTPHeaderField: "Upload-Offset")
        req.setValue("1.0.0", forHTTPHeaderField: "Tus-Resumable")
        if let finalLength {
            req.setValue(String(finalLength), forHTTPHeaderField: "Upload-Length")
        }
        req.httpBody = data
        var retry = 0
        let retryID = UUID()
        var reportedRetry = false
        defer {
            if reportedRetry { retryObserver?(.finished(id: retryID)) }
        }
        while true {
            do {
                let (_, http) = try await sendOnce(req)
                guard let offsetString = http.value(forHTTPHeaderField: "Upload-Offset"),
                      let newOffset = Int64(offsetString) else {
                    throw ServerClientError.decoding("missing Upload-Offset in PATCH response")
                }
                return newOffset
            } catch is CancellationError {
                throw CancellationError()
            } catch let error as ServerClientError where error.isRetryable {
                if case .transport = error {
                    // A lost response does not tell us whether the server appended this chunk.
                    // Re-read its authoritative TUS offset before replaying a stateful PATCH.
                    let serverOffset = try await self.offset(forUploadID: id).offset
                    if serverOffset == offset + Int64(data.count) { return serverOffset }
                    guard serverOffset == offset else { throw ServerClientError.offsetConflict }
                }
                guard let delay = Self.nextRetryDelay(for: error, retry: &retry,
                                                      maxRetries: maxPatchRetries) else { throw error }
                reportedRetry = true
                retryObserver?(.waiting(id: retryID, retryAt: Date().addingTimeInterval(delay)))
                try await Task.sleep(for: .seconds(delay))
            }
        }
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
