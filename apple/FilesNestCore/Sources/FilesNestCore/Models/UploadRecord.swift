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
    /// Set by the server once the file is moved into organized storage.
    public let organizedPath: String?

    enum CodingKeys: String, CodingKey {
        case id, status, filename
        case localIdentifier = "local_identifier"
        case backendID = "backend_id"
        case bundleID = "bundle_id"
        case creationDate = "creation_date"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case organizedPath = "organized_path"
    }
}
