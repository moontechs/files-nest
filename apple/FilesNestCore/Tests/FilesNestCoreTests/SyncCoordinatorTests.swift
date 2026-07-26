import Testing
import Foundation
@testable import FilesNestCore

@Suite(.serialized)
struct SyncCoordinatorTests {
    func date(_ s: String) -> Date { ISO8601DateFormatter().date(from: s)! }

    func resource(_ localID: String, kind: ResourceKind = .photo,
                  date iso: String = "2024-06-15T10:00:00Z") -> AssetResource {
        AssetResource(key: ResourceKey(localIdentifier: localID, kind: kind),
                      filename: "IMG.jpg", creationDate: date(iso), bundleID: nil)
    }

    func makeCoordinator(server: FakeServer, library: [AssetResource],
                         state: InMemorySyncStateStore = InMemorySyncStateStore(),
                         now: Date = Date(timeIntervalSince1970: 1_700_000_000)) -> SyncCoordinator {
        let client = server.client()
        return SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: library, error: nil),
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 250, blobSize: 100)),
            state: state,
            now: { now })
    }

    @Test func newAssetIsCreatedUploadedAndReported() async throws {
        let server = FakeServer(host: "sc-create.test")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(report.failed.isEmpty)
        let recs = server.all()
        #expect(recs.count == 1)
        #expect(recs[0].status == "complete")
        #expect(recs[0].offset == 250)
        #expect(recs[0].localIdentifier == "A#photo")
    }

    @Test func uploadingRecordResumesFromServerOffset() async throws {
        let server = FakeServer(host: "sc-resume.test")
        let id = server.seed(localIdentifier: "A#photo", status: "uploading", offset: 100)
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded.count == 1)
        #expect(server.all().count == 1)
        #expect(server.record(id: id)?.status == "complete")
        #expect(server.record(id: id)?.offset == 250)
    }

    @Test func completeRecordIsSkipped() async throws {
        let server = FakeServer(host: "sc-skip.test")
        server.seed(localIdentifier: "A#photo", status: "complete")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded.isEmpty)
        #expect(report.skipped == 1)
    }

    @Test func absentServerRecordIsDeletedAfterUploads() async throws {
        let server = FakeServer(host: "sc-delete.test")
        let goneID = server.seed(localIdentifier: "GONE#photo", status: "complete")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(report.deleted == [ResourceKey(localIdentifier: "GONE", kind: .photo)])
        #expect(server.record(id: goneID)?.status == "deleted")

        // Ordering: the DELETE must come after all data/status PATCHes.
        let deleteIdx = server.events.firstIndex { $0.hasPrefix("DELETE") }!
        let lastUploadIdx = server.events.lastIndex { $0.contains("/data") || $0.hasSuffix("/status") }!
        #expect(deleteIdx > lastUploadIdx)
    }

    @Test func lastSyncStartedIsPersistedAtStart() async throws {
        let server = FakeServer(host: "sc-state.test")
        let state = InMemorySyncStateStore()
        let now = Date(timeIntervalSince1970: 1_700_000_123)
        _ = try await makeCoordinator(server: server, library: [resource("A")], state: state, now: now)
            .sync(range: .all)
        #expect(state.loadLastSyncStarted() == now)
    }

    @Test func enumerationErrorPropagates() async throws {
        struct Boom: Error {}
        let server = FakeServer(host: "sc-enum.test")
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [], error: Boom()),
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 10, blobSize: 10)),
            state: InMemorySyncStateStore(),
            now: { Date(timeIntervalSince1970: 0) })
        await #expect(throws: Boom.self) { _ = try await coord.sync(range: .all) }
    }
}
