import Foundation

/// The file operations needed to reconcile a local-folder destination.
public struct LocalFolderPlan: Sendable, Equatable {
    public let uploads: [AssetResource]
    public let deletes: [(path: URL, key: ResourceKey)]

    public init(uploads: [AssetResource], deletes: [(path: URL, key: ResourceKey)]) {
        self.uploads = uploads
        self.deletes = deletes
    }

    public static func == (lhs: LocalFolderPlan, rhs: LocalFolderPlan) -> Bool {
        guard lhs.uploads == rhs.uploads, lhs.deletes.count == rhs.deletes.count else { return false }
        return zip(lhs.deletes, rhs.deletes).allSatisfy { left, right in
            left.path == right.path && left.key == right.key
        }
    }
}
