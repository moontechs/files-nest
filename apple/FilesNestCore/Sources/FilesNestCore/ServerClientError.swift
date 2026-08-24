import Foundation

public enum ServerClientError: Error, Sendable, Equatable {
    case unauthorized
    case notFound
    case backendLost
    case alreadyCompleted
    case alreadyDeleted
    case notUploading
    case offsetConflict
    /// 409 `upload_incomplete` — the backend upload isn't finalized yet, so the
    /// status transition was rejected. Recoverable: finish the data upload first.
    case uploadIncomplete
    case badRequest(message: String)
    case requestTooLarge
    case unexpectedStatus(code: Int, message: String?)
    case decoding(String)
    case transport(String)
    /// 503 — server is at its concurrency cap. `retryAfter` is the `Retry-After`
    /// header in seconds (nil when absent). Recoverable: back off and retry.
    case serviceUnavailable(retryAfter: Int?)

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
            // The server overloads 409; the error string is the discriminator.
            // Order matters: the PATCH-data handler emits a combined
            // "already completed or deleted" message, which must be matched
            // before the individual "already deleted"/"already completed" cases.
            let m = msg ?? ""
            if m.contains("upload_incomplete") { return .uploadIncomplete }
            if m.contains("backend_lost") { return .backendLost }
            if m.contains("offset mismatch") { return .offsetConflict }
            if m.contains("already completed or deleted") { return .alreadyCompleted }
            if m.contains("already deleted") { return .alreadyDeleted }
            if m.contains("already completed") { return .alreadyCompleted }
            if m.contains("not in uploading") { return .notUploading }
            return .unexpectedStatus(code: 409, message: msg)
        case 400: return .badRequest(message: msg ?? "")
        default: return .unexpectedStatus(code: status, message: msg)
        }
    }
}

extension ServerClientError {
    var isRetryable: Bool {
        switch self {
        case .transport, .serviceUnavailable:
            return true
        default:
            return false
        }
    }

    var retryAfter: Double? {
        guard case .serviceUnavailable(let seconds) = self else { return nil }
        return seconds.map { Double(max(0, $0)) }
    }
}
