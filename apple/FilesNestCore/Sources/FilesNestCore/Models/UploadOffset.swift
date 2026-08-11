public struct UploadOffset: Sendable, Equatable {
    public let offset: Int64
    public let length: Int64?

    public init(offset: Int64, length: Int64?) {
        self.offset = offset
        self.length = length
    }
}
