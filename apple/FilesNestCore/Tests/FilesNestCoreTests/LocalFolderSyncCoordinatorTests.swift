import Foundation
import Testing
@testable import FilesNestCore

@Suite(.serialized)
struct LocalFolderSyncCoordinatorTests {
    private func temporaryDirectory() throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("filesnest-coordinator-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func resource(_ id: String, filename: String = "IMG.jpg") -> AssetResource {
        AssetResource(key: ResourceKey(localIdentifier: id, kind: .photo), filename: filename,
                      creationDate: Date(timeIntervalSince1970: 1_700_000_000), bundleID: nil)
    }

    private func coordinator(library: any AssetLibrary, root: URL, bookmark: Data = Data([1]),
                             source: any AssetDataSource = FakeAssetDataSource(totalBytes: 10, blobSize: 10),
                             state: InMemorySyncStateStore = InMemorySyncStateStore()) -> LocalFolderSyncCoordinator {
        LocalFolderSyncCoordinator(library: library,
                                   writer: LocalFolderWriter(source: source, volumeFreeSpace: { _ in Int64.max }),
                                   root: root, bookmark: bookmark, state: state)
    }

    @Test func fullSyncWritesMissingSkipsExistingAndDeletesOnlyManagedFiles() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let missing = resource("missing")
        let existing = resource("existing")
        let existingPath = LocalFolderPlanner.expectedPath(for: existing, destinationRoot: root)
        try FileManager.default.createDirectory(at: existingPath.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("old".utf8).write(to: existingPath)
        let orphan = root.appendingPathComponent("2023/01/02/orphan_\(safeID("removed")).jpg")
        try FileManager.default.createDirectory(at: orphan.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data().write(to: orphan)
        let unrelated = root.appendingPathComponent("2023/01/02/family.jpg")
        try Data().write(to: unrelated)
        let protectedDirectory = root.appendingPathComponent("2023/01/02/important")
        try FileManager.default.createDirectory(at: protectedDirectory, withIntermediateDirectories: true)

        let report = try await coordinator(library: FakeAssetLibrary(items: [missing, existing]), root: root).sync(range: .all)

        #expect(report.uploaded == [missing.key])
        #expect(report.skipped == 1)
        #expect(!FileManager.default.fileExists(atPath: orphan.path))
        #expect(FileManager.default.fileExists(atPath: unrelated.path))
        #expect(FileManager.default.fileExists(atPath: protectedDirectory.path))
        #expect(FileManager.default.fileExists(atPath: LocalFolderPlanner.expectedPath(for: missing, destinationRoot: root).path))
    }

    @Test func incrementalSyncDoesNotDeleteOrphans() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let orphan = root.appendingPathComponent("2023/01/02/orphan.jpg")
        try FileManager.default.createDirectory(at: orphan.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data().write(to: orphan)

        let report = try await coordinator(library: FakeAssetLibrary(), root: root).sync(range: .modifiedSince(Date()))

        #expect(report.deleted.isEmpty)
        #expect(FileManager.default.fileExists(atPath: orphan.path))
    }

    @Test func directoryAtExpectedPathIsNotCountedAsBackedUp() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let item = resource("directory")
        let path = LocalFolderPlanner.expectedPath(for: item, destinationRoot: root)
        try FileManager.default.createDirectory(at: path, withIntermediateDirectories: true)

        let report = try await coordinator(library: FakeAssetLibrary(items: [item]), root: root).sync(range: .all)

        #expect(report.skipped == 0)
        #expect(report.failed.map(\.key) == [item.key])
    }

    @Test func resumeUsesSavedDestinationWithoutScanning() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let item = resource("resume")
        let bookmark = Data([8])
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([item], destination: bookmark, session: state.remainingUploadsSession())
        let library = FakeAssetLibrary(items: [], error: FakeSourceError.injected)

        let report = try await coordinator(library: library, root: root, bookmark: bookmark, state: state).resume(resources: [item])

        #expect(report.uploaded == [item.key])
        #expect(library.requestedRanges.isEmpty)
        #expect(state.loadRemainingUploads().isEmpty)
    }

    @Test func failedWriteDoesNotStopLaterResources() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let bad = resource("bad")
        let good = resource("good")

        let report = try await coordinator(library: FakeAssetLibrary(items: [bad, good]), root: root,
                                           source: SelectiveFailingSource()).sync(range: .all)

        #expect(report.failed.map(\.key) == [bad.key])
        #expect(report.uploaded == [good.key])
    }

    @Test func resumeRejectsDifferentSavedDestination() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([], destination: Data([1]), session: state.remainingUploadsSession())

        await #expect(throws: LocalFolderSyncError.destinationChanged) {
            try await coordinator(library: FakeAssetLibrary(), root: root, bookmark: Data([2]), state: state).resume(resources: [])
        }
    }

    @Test func resumeAcceptsRefreshedBookmarkForSameFolder() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let item = resource("resume")
        let queuedBookmark = Data([1])
        let refreshedBookmark = Data([2])
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([item], destination: queuedBookmark, session: state.remainingUploadsSession())
        let coordinator = LocalFolderSyncCoordinator(
            library: FakeAssetLibrary(items: [], error: FakeSourceError.injected),
            writer: LocalFolderWriter(source: FakeAssetDataSource(totalBytes: 10, blobSize: 10), volumeFreeSpace: { _ in Int64.max }),
            root: root,
            bookmark: refreshedBookmark,
            state: state,
            resolveBookmark: { bookmark in bookmark == queuedBookmark ? root : nil }
        )

        let report = try await coordinator.resume(resources: [item])

        #expect(report.uploaded == [item.key])
    }

    @Test func missingDestinationIsTypedError() async throws {
        let root = try temporaryDirectory()
        try FileManager.default.removeItem(at: root)
        await #expect(throws: LocalFolderSyncError.unavailableDestination) {
            try await coordinator(library: FakeAssetLibrary(), root: root).sync(range: .all)
        }
    }

    @Test func destinationRemovedDuringRunIsTypedError() async throws {
        let root = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: root) }
        let first = resource("first")
        let second = resource("second")

        await #expect(throws: LocalFolderSyncError.unavailableDestination) {
            try await coordinator(library: FakeAssetLibrary(items: [first, second]), root: root,
                                  source: RemovesRootAfterFirstRead(root: root)).sync(range: .all)
        }
    }
}

private struct SelectiveFailingSource: AssetDataSource {
    func read(assetID: String, from offset: Int64, into sink: @Sendable (Data) async throws -> Void) async throws {
        if assetID.hasPrefix("bad#") { throw FakeSourceError.injected }
        try await sink(Data("ok".utf8))
    }
}

private struct RemovesRootAfterFirstRead: AssetDataSource {
    let root: URL

    func read(assetID: String, from offset: Int64, into sink: @Sendable (Data) async throws -> Void) async throws {
        try await sink(Data("ok".utf8))
        if assetID.hasPrefix("first#") {
            try FileManager.default.removeItem(at: root)
        }
    }
}
