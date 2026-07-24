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
