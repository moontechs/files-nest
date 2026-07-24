public struct UploadPage: Sendable, Equatable {
    public let items: [UploadRecord]
    public let nextCursor: String?

    public init(items: [UploadRecord], nextCursor: String?) {
        self.items = items
        self.nextCursor = nextCursor
    }
}
