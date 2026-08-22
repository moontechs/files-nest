import Foundation

/// One uploadable resource of a Photos asset. A Live Photo yields two of these
/// (`#photo` and `#pairedVideo`) sharing `bundleID`. `key.encoded` is the diff key.
public struct AssetResource: Sendable, Equatable, Codable {
    public let key: ResourceKey
    public let filename: String
    public let creationDate: Date
    public let bundleID: String?

    public init(key: ResourceKey, filename: String, creationDate: Date, bundleID: String?) {
        self.key = key
        self.filename = filename
        self.creationDate = creationDate
        self.bundleID = bundleID
    }
}
