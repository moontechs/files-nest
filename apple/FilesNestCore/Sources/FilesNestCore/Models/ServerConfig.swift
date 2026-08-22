import Foundation

/// Server-advertised limits, from `GET /config`.
public struct ServerConfig: Decodable, Sendable, Equatable {
    public let maxConcurrentUploads: Int

    public init(maxConcurrentUploads: Int) {
        self.maxConcurrentUploads = maxConcurrentUploads
    }
}
