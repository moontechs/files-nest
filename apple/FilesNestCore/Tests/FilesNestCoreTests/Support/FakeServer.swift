import Foundation
@testable import FilesNestCore

/// Stateful in-memory stand-in for the Go server, driven through MockURLProtocol
/// so tests exercise the REAL ServerClient + AssetUploader end to end. Records are
/// keyed by an opaque `id-N` (the client treats ids as opaque routing tokens).
final class FakeServer: @unchecked Sendable {
    struct Record {
        var id: String
        var localIdentifier: String   // == ResourceKey.encoded
        var status: String            // uploading | completing | complete | deleted | backend_lost
        var backendID: String
        var filename: String?
        var bundleID: String?
        var creationDate: String?     // ISO-8601
        var offset: Int64
        var length: Int64?
    }

    let host: String
    private let lock = NSLock()
    private var records: [String: Record] = [:]
    private var nextID = 0
    private var _events: [String] = []

    /// Page size for GET /uploads. Default: everything in one page.
    var pageSize = Int.max
    /// Ids whose data ops (HEAD/PATCH/status) return 409 backend_lost. A DELETE
    /// clears the flag, so a re-created record (fresh id) uploads cleanly.
    var backendLostIDs: Set<String> = []
    /// If true, the first data op on any not-yet-flagged id flags it lost first —
    /// models a backend that lost the file the instant its record was created, so
    /// even a recovery upload (fresh id) fails.
    var markLostOnFirstDataOp = false
    /// Ids whose DELETE returns 500, to exercise "record the failure, keep deleting".
    var failDeleteIDs: Set<String> = []
    /// Value returned by GET /config. nil → the route 404s (models a pre-#25 server).
    var configMax: Int?

    init(host: String) { self.host = host }

    /// Ordered log of "METHOD path" for ordering assertions (e.g. deletes after uploads).
    var events: [String] { lock.lock(); defer { lock.unlock() }; return _events }
    func record(id: String) -> Record? { lock.lock(); defer { lock.unlock() }; return records[id] }
    func all() -> [Record] { lock.lock(); defer { lock.unlock() }; return Array(records.values) }

    @discardableResult
    func seed(localIdentifier: String, status: String, offset: Int64 = 0,
              creationDate: String? = "2024-06-15T10:00:00Z", filename: String? = "IMG.jpg",
              bundleID: String? = nil) -> String {
        lock.lock(); defer { lock.unlock() }
        let id = "id-\(nextID)"; nextID += 1
        records[id] = Record(id: id, localIdentifier: localIdentifier, status: status,
                             backendID: "b-\(id)", filename: filename, bundleID: bundleID,
                             creationDate: creationDate, offset: offset, length: nil)
        return id
    }

    func client() -> ServerClient {
        MockURLProtocol.setHandler(forHost: host) { [weak self] req in
            guard let self else { throw URLError(.cancelled) }   // server deallocated mid-request
            return try self.handle(req)
        }
        return ServerClient(baseURL: URL(string: "https://\(host)")!,
                            credentials: FakeCredentialStore(creds: nil),
                            session: MockURLProtocol.makeSession())
    }

    // MARK: routing (runs on URLSession's worker thread; lock guards state)
    private func handle(_ req: URLRequest) throws -> (HTTPURLResponse, Data) {
        let url = req.url!
        let method = req.httpMethod ?? "GET"
        let parts = url.path.split(separator: "/").map(String.init) // uploads / <id> / data|status
        lock.lock(); defer { lock.unlock() }
        _events.append("\(method) \(url.path)")

        func resp(_ status: Int, _ headers: [String: String] = [:], _ body: Data = Data())
            -> (HTTPURLResponse, Data) {
            (HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                             headerFields: headers)!, body)
        }
        func lost() -> (HTTPURLResponse, Data) {
            resp(409, [:], #"{"error":"backend_lost"}"#.data(using: .utf8)!)
        }
        /// Mirrors the server's status guards on data ops (`handlers.go` HEAD guard /
        /// `rejectUploadNotUploading`): only an `uploading` record accepts data. A
        /// completed record is the resume case — the client must read that as
        /// "already done", not as a failure.
        func rejectIfNotUploading(_ r: Record) -> (HTTPURLResponse, Data)? {
            switch r.status {
            case "uploading": return nil
            case "complete": return resp(409, [:], #"{"error":"upload already completed"}"#.data(using: .utf8)!)
            case "deleted": return resp(404, [:], #"{"error":"upload not found"}"#.data(using: .utf8)!)
            default: return resp(409, [:], #"{"error":"upload not in uploading state"}"#.data(using: .utf8)!)
            }
        }
        func json(_ r: Record) -> [String: Any] {
            var o: [String: Any] = ["id": r.id, "local_identifier": r.localIdentifier,
                                    "status": r.status, "backend_id": r.backendID]
            if let f = r.filename { o["filename"] = f }
            if let b = r.bundleID { o["bundle_id"] = b }
            if let c = r.creationDate { o["creation_date"] = c }
            return o
        }

        switch (method, parts.count) {
        case ("POST", 1) where parts[0] == "uploads":
            let body = (try? JSONSerialization.jsonObject(with: req.httpBodyData())) as? [String: Any] ?? [:]
            let loc = body["local_identifier"] as? String ?? ""
            // The real server keys by SafeID(localIdentifier): at most one record
            // per localIdentifier. On conflict it branches on status (handlers.go:258).
            if let existingID = records.first(where: { $0.value.localIdentifier == loc })?.key {
                var existing = records[existingID]!
                switch existing.status {
                case "backend_lost", "deleted":
                    // ReRegister: fresh backend, reset to uploading, offset 0, SAME id.
                    existing.status = "uploading"
                    existing.offset = 0
                    existing.length = nil
                    existing.backendID = "b-\(existingID)-r\(nextID)"; nextID += 1
                    records[existingID] = existing
                    backendLostIDs.remove(existingID)
                    return resp(201, [:], try JSONSerialization.data(withJSONObject: json(existing)))
                default:
                    // Idempotent: still uploading / completing / complete.
                    return resp(200, [:], try JSONSerialization.data(withJSONObject: json(existing)))
                }
            }
            let id = "id-\(nextID)"; nextID += 1
            let rec = Record(id: id, localIdentifier: loc, status: "uploading", backendID: "b-\(id)",
                             filename: body["filename"] as? String, bundleID: body["bundle_id"] as? String,
                             creationDate: body["creation_date"] as? String, offset: 0, length: nil)
            records[id] = rec
            return resp(201, [:], try JSONSerialization.data(withJSONObject: json(rec)))

        case ("GET", 1) where parts[0] == "uploads":
            let sorted = records.values.sorted { ($0.creationDate ?? "", $0.id) < ($1.creationDate ?? "", $1.id) }
            let cursorValue = URLComponents(url: url, resolvingAgainstBaseURL: false)?
                .queryItems?.first { $0.name == "cursor" }?.value
            let start = decodeCursor(cursorValue)
            let clampedStart = min(start, sorted.count)
            let end = min(clampedStart + pageSize, sorted.count)
            let slice = sorted[clampedStart..<end]
            let next = end < sorted.count ? encodeCursor(end) : ""
            let obj: [String: Any] = ["items": slice.map(json), "next_cursor": next]
            return resp(200, [:], try JSONSerialization.data(withJSONObject: obj))

        case ("HEAD", 3) where parts[0] == "uploads" && parts[2] == "data":
            let id = parts[1]
            if markLostOnFirstDataOp && !backendLostIDs.contains(id) { backendLostIDs.insert(id) }
            if backendLostIDs.contains(id) { records[id]?.status = "backend_lost"; return lost() }
            guard let r = records[id] else { return resp(404) }
            if let rejection = rejectIfNotUploading(r) { return rejection }
            return resp(200, ["Upload-Offset": String(r.offset)])

        case ("PATCH", 3) where parts[0] == "uploads" && parts[2] == "data":
            let id = parts[1]
            if markLostOnFirstDataOp && !backendLostIDs.contains(id) { backendLostIDs.insert(id) }
            if backendLostIDs.contains(id) { records[id]?.status = "backend_lost"; return lost() }
            guard var r = records[id] else { return resp(404) }
            if let rejection = rejectIfNotUploading(r) { return rejection }
            let off = Int64(req.value(forHTTPHeaderField: "Upload-Offset") ?? "0") ?? 0
            r.offset = off + req.httpBodyByteCount()
            if let fl = req.value(forHTTPHeaderField: "Upload-Length").flatMap(Int64.init) { r.length = fl }
            records[id] = r
            return resp(204, ["Upload-Offset": String(r.offset)])

        case ("PATCH", 3) where parts[0] == "uploads" && parts[2] == "status":
            let id = parts[1]
            if markLostOnFirstDataOp && !backendLostIDs.contains(id) { backendLostIDs.insert(id) }
            if backendLostIDs.contains(id) { records[id]?.status = "backend_lost"; return lost() }
            guard var r = records[id] else { return resp(404) }
            r.status = "complete"; records[id] = r
            return resp(200)

        case ("DELETE", 2) where parts[0] == "uploads":
            let id = parts[1]
            if failDeleteIDs.contains(id) { return resp(500) }
            records[id]?.status = "deleted"
            backendLostIDs.remove(id)
            return resp(204)

        case ("GET", 1) where parts[0] == "config":
            guard let m = configMax else { return resp(404) }
            let obj: [String: Any] = ["maxConcurrentUploads": m]
            return resp(200, ["Content-Type": "application/json"],
                        try JSONSerialization.data(withJSONObject: obj))

        default:
            return resp(500)
        }
    }

    private func encodeCursor(_ index: Int) -> String {
        Data(String(index).utf8).base64EncodedString()
    }
    private func decodeCursor(_ value: String?) -> Int {
        guard let value, let data = Data(base64Encoded: value),
              let s = String(data: data, encoding: .utf8), let i = Int(s) else { return 0 }
        return i
    }
}

extension URLRequest {
    /// Reads the full body (whether set as `httpBody` or streamed by URLSession).
    /// Distinct from `httpBodyByteCount()` (AssetUploaderTests.swift), which only counts.
    func httpBodyData() -> Data {
        if let body = httpBody { return body }
        guard let stream = httpBodyStream else { return Data() }
        stream.open(); defer { stream.close() }
        var data = Data()
        let size = 64 * 1024
        let buf = UnsafeMutablePointer<UInt8>.allocate(capacity: size)
        defer { buf.deallocate() }
        while stream.hasBytesAvailable {
            let n = stream.read(buf, maxLength: size)
            if n <= 0 { break }
            data.append(buf, count: n)
        }
        return data
    }
}
