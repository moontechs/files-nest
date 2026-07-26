import Foundation
@testable import FilesNestCore

struct FakeAssetLibrary: AssetLibrary {
    var items: [AssetResource] = []
    var error: (any Error)? = nil

    func resources(in range: SyncRange) async throws -> [AssetResource] {
        if let error { throw error }
        return items
    }
}
