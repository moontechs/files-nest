import Foundation

/// The PhotoKit resource kinds this client can address. Closed set, and no
/// case contains '#', which is what makes `ResourceKey` parsing unambiguous.
public enum ResourceKind: String, Sendable, CaseIterable, Codable {
    case photo
    case pairedVideo
    case fullSizePhoto
    case video
    case audio
    case alternatePhoto
}

public enum ResourceKeyError: Error, Equatable {
    case missingSeparator
    case unknownKind(String)
    case emptyLocalIdentifier
}

/// Addresses one *resource* of one asset. A Live Photo's JPEG and MOV share a
/// `localIdentifier` but differ in `kind`, so they encode to distinct keys.
/// See docs/design/20260724-photosassetdatasource.md §5.
public struct ResourceKey: Sendable, Equatable, Codable {
    public let localIdentifier: String
    public let kind: ResourceKind

    public init(localIdentifier: String, kind: ResourceKind) {
        self.localIdentifier = localIdentifier
        self.kind = kind
    }

    public var encoded: String { "\(localIdentifier)#\(kind.rawValue)" }

    /// Parses on the LAST '#'. `localIdentifier` may contain '#'; `kind` cannot,
    /// so the final separator is always the real one.
    public init(parsing string: String) throws {
        guard let hash = string.lastIndex(of: "#") else {
            throw ResourceKeyError.missingSeparator
        }
        let idPart = String(string[string.startIndex..<hash])
        let kindPart = String(string[string.index(after: hash)...])
        guard !idPart.isEmpty else { throw ResourceKeyError.emptyLocalIdentifier }
        guard let kind = ResourceKind(rawValue: kindPart) else {
            throw ResourceKeyError.unknownKind(kindPart)
        }
        self.localIdentifier = idPart
        self.kind = kind
    }
}
