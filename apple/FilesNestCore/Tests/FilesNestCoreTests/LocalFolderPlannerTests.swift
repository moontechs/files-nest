import Foundation
import Testing
@testable import FilesNestCore

struct LocalFolderPlannerTests {
    private let root = URL(fileURLWithPath: "/tmp/filesnest-destination")

    private func asset(_ id: String, filename: String = "IMG.jpg") -> AssetResource {
        AssetResource(
            key: ResourceKey(localIdentifier: id, kind: .photo),
            filename: filename,
            creationDate: ISO8601DateFormatter().date(from: "2024-06-15T23:30:00Z")!,
            bundleID: nil
        )
    }

    @Test func expectedPathIsDeterministicAndDateOrganized() {
        let path = LocalFolderPlanner.expectedPath(for: asset("A"), destinationRoot: root)
        #expect(path.lastPathComponent == "IMG_\(safeID("A#photo")).jpg")
        #expect(path == LocalFolderPlanner.expectedPath(for: asset("A"), destinationRoot: root))
    }

    @Test func sameFilenameAndDateStillProduceDistinctPaths() {
        let first = LocalFolderPlanner.expectedPath(for: asset("A"), destinationRoot: root)
        let second = LocalFolderPlanner.expectedPath(for: asset("B"), destinationRoot: root)
        #expect(first != second)
        #expect(first.pathExtension == "jpg")
        #expect(second.pathExtension == "jpg")
    }

    @Test func planDeletesOnlyOrphans() {
        let current = asset("current")
        let expected = LocalFolderPlanner.expectedPath(for: current, destinationRoot: root)
        let orphan = root.appendingPathComponent("2024/06/15/old_thing.jpg")
        let deletes = LocalFolderPlanner.planDeletes(
            library: [current], actualPaths: [expected, orphan], destinationRoot: root
        )
        #expect(deletes.map(\.path) == [orphan])
    }

    @Test func emptyActualPathsHaveNoDeletes() {
        #expect(LocalFolderPlanner.planDeletes(library: [asset("A")], actualPaths: [], destinationRoot: root).isEmpty)
    }
}
