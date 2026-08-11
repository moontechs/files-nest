import Foundation

public struct SyncPlan: Sendable, Equatable {
    public let uploads: [PlannedUpload]
    public let deletes: [PlannedDelete]
    public let skipped: Int

    public init(uploads: [PlannedUpload], deletes: [PlannedDelete], skipped: Int) {
        self.uploads = uploads
        self.deletes = deletes
        self.skipped = skipped
    }
}

public struct PlannedUpload: Sendable, Equatable {
    public enum Mode: Sendable, Equatable {
        case create                     // no server record for this key
        case resume(uploadID: String)   // server status=uploading → resume from HEAD offset
        case recover(uploadID: String)  // server status=backend_lost → delete→create→upload from 0
    }
    public let resource: AssetResource
    public let mode: Mode

    public init(resource: AssetResource, mode: Mode) {
        self.resource = resource
        self.mode = mode
    }
}

public struct PlannedDelete: Sendable, Equatable {
    public let uploadID: String
    public let key: ResourceKey

    public init(uploadID: String, key: ResourceKey) {
        self.uploadID = uploadID
        self.key = key
    }
}
