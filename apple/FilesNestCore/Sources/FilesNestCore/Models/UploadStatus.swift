public enum UploadStatus: String, Codable, Sendable {
    case uploading, complete, deleted
    case backendLost = "backend_lost"
}
