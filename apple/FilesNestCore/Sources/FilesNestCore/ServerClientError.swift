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
            if m.contains("offset mismatch") { return .offsetConflict }
            if m.contains("already completed") || m.contains("already deleted") { return .alreadyCompleted }
            if m.contains("not in uploading") { return .notUploading }
            return .unexpectedStatus(code: 409, message: msg)
        case 400: return .badRequest(message: msg ?? "")
        default: return .unexpectedStatus(code: status, message: msg)
        }
    }
}
