import Testing
import Foundation
@testable import FilesNestCore

@Suite(.serialized)
struct FakeServerTests {
    func date(_ s: String) -> Date { ISO8601DateFormatter().date(from: s)! }

    @Test func createThenListReturnsRecordKeyedByEncodedResourceKey() async throws {
        let server = FakeServer(host: "fs-create.test")
        let client = server.client()
        let key = ResourceKey(localIdentifier: "A", kind: .photo).encoded

        let created = try await client.createUpload(
            CreateUploadRequest(localIdentifier: key, filename: "IMG.jpg",
                                creationDate: date("2024-06-15T10:00:00Z"), bundleID: nil))
        #expect(created.status == .uploading)
        #expect(created.localIdentifier == key)

        let page = try await client.listUploads(cursor: nil)
        #expect(page.items.map(\.localIdentifier) == [key])
        #expect(page.nextCursor == nil)
    }

    @Test func fullUploadFlowThroughRealUploaderMarksComplete() async throws {
        let server = FakeServer(host: "fs-upload.test")
        let client = server.client()
        let rec = try await client.createUpload(
            CreateUploadRequest(localIdentifier: "A#photo", filename: "IMG.jpg",
                                creationDate: date("2024-06-15T10:00:00Z"), bundleID: nil))

        let uploader = AssetUploader(client: client,
                                     source: FakeAssetDataSource(totalBytes: 250, blobSize: 100))
        try await uploader.upload(assetID: "A#photo", uploadID: rec.id)

        #expect(server.record(id: rec.id)?.status == "complete")
        #expect(server.record(id: rec.id)?.offset == 250)
    }

    @Test func pagingFollowsCursor() async throws {
        let server = FakeServer(host: "fs-page.test")
        server.pageSize = 1
        for i in 0..<3 {
            server.seed(localIdentifier: "K\(i)#photo", status: "complete",
                        creationDate: "2024-06-1\(i)T10:00:00Z")
        }
        let client = server.client()
        var seen: [String] = []
        var cursor: String? = nil
        repeat {
            let page = try await client.listUploads(cursor: cursor)
            seen += page.items.map(\.localIdentifier)
            cursor = page.nextCursor
        } while cursor != nil
        #expect(seen.count == 3)
    }

    @Test func backendLostInjectionReturns409() async throws {
        let server = FakeServer(host: "fs-lost.test")
        let id = server.seed(localIdentifier: "A#photo", status: "uploading")
        server.backendLostIDs = [id]
        let client = server.client()
        await #expect(throws: ServerClientError.backendLost) {
            _ = try await client.offset(forUploadID: id)
        }
    }

    @Test func fakeAssetLibraryReturnsItemsAndThrows() async throws {
        let items = [AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                   filename: "IMG.jpg", creationDate: date("2024-06-15T10:00:00Z"),
                                   bundleID: nil)]
        let lib = FakeAssetLibrary(items: items, error: nil)
        #expect(try await lib.resources(in: .all).count == 1)

        struct Boom: Error {}
        let failing = FakeAssetLibrary(items: [], error: Boom())
        await #expect(throws: Boom.self) { _ = try await failing.resources(in: .all) }
    }
}
