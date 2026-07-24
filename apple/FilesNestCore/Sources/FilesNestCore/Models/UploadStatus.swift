/// Mirrors `store.Status` in the Go server. `completing` is a transient state
/// the server sets while moving the file from incoming to organized storage —
/// omitting it would make valid records fail to decode.
public enum UploadStatus: String, Codable, Sendable {
    case uploading
    case completing
    case complete
    case deleted
    case backendLost = "backend_lost"
}
