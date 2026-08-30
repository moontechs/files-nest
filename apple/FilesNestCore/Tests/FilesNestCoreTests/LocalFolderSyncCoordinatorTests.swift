import Foundation
import Testing
@testable import FilesNestCore

@Suite(.serialized)
struct LocalFolderSyncCoordinatorTests {
    final class Store: LocalFolderStore, @unchecked Sendable {
        var bookmark: Data?
        init(_ bookmark: Data? = nil) { self.bookmark = bookmark }
        func load() -> Data? { bookmark }
        func save(_ bookmark: Data) { self.bookmark = bookmark }
        func clear() { bookmark = nil }
    }

    @Test func missingDestinationIsTypedError() async {
        let store = Store()
        let writer = LocalFolderWriter(source: FakeAssetDataSource(totalBytes: 0, blobSize: 1))
        let coordinator = LocalFolderSyncCoordinator(library: FakeAssetLibrary(), writer: writer, store: store)
        do {
            _ = try await coordinator.sync(range: .all)
            Issue.record("expected unavailable destination")
        } catch let error as LocalFolderSyncError {
            #expect(error == .unavailableDestination)
        } catch {
            Issue.record("unexpected error: \(error)")
        }
    }

    @Test func resumeRejectsChangedBookmark() async {
        let store = Store(Data([1]))
        let writer = LocalFolderWriter(source: FakeAssetDataSource(totalBytes: 0, blobSize: 1))
        let coordinator = LocalFolderSyncCoordinator(library: FakeAssetLibrary(), writer: writer, store: store)
        store.bookmark = Data([2])
        do {
            _ = try await coordinator.resume(resources: [])
            Issue.record("expected destination changed")
        } catch let error as LocalFolderSyncError {
            #expect(error == .destinationChanged)
        } catch {
            Issue.record("unexpected error: \(error)")
        }
    }
}
