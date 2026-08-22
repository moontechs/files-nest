import Testing
import Foundation
@testable import FilesNestCore

/// Exercises resume against a REAL server, so the FakeServer's 409 fidelity is
/// checked against reality rather than assumed. Skips cleanly when
/// FILESNEST_LIVE_SERVER is unset, so `swift test` stays hermetic.
///
///   PORT=8099 STORAGE_PATH=/tmp/fn-server-data BACKUP_USER=test BACKUP_PASS=test123 go run .
///   FILESNEST_LIVE_SERVER=http://localhost:8099 FILESNEST_LIVE_USER=test \
///     FILESNEST_LIVE_PASS=test123 swift test --filter LiveResume
@Suite(.serialized)
struct LiveResumeServerTests {
    private var config: (url: URL, creds: BasicCredentials)? {
        let env = ProcessInfo.processInfo.environment
        guard let raw = env["FILESNEST_LIVE_SERVER"], let url = URL(string: raw) else { return nil }
        return (url, BasicCredentials(username: env["FILESNEST_LIVE_USER"] ?? "",
                                      password: env["FILESNEST_LIVE_PASS"] ?? ""))
    }

    private func resource(_ id: String) -> AssetResource {
        AssetResource(key: ResourceKey(localIdentifier: id, kind: .photo),
                      filename: "\(id).jpg",
                      creationDate: Date(timeIntervalSince1970: 1_700_000_000),
                      bundleID: nil)
    }

    private func coordinator(_ c: (url: URL, creds: BasicCredentials)) -> (SyncCoordinator, ServerClient) {
        let client = ServerClient(baseURL: c.url, credentials: StaticCredentialStore(c.creds))
        // A library that FAILS if enumerated — proves resume never scans.
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [], error: FakeSourceError.injected),
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 250, blobSize: 100)),
            state: InMemorySyncStateStore(),
            configuredConcurrency: 2,
            now: { Date(timeIntervalSince1970: 1_700_000_000) })
        return (coord, client)
    }

    @Test func resumeUploadsASavedListToARealServer() async throws {
        guard let c = config else { return }   // no live server configured → skip
        let (coord, client) = coordinator(c)
        let ids = ["live-A-\(UUID().uuidString)", "live-B-\(UUID().uuidString)"]

        let report = try await coord.resume(resources: ids.map(resource))

        #expect(Set(report.uploaded.map(\.localIdentifier)) == Set(ids))
        #expect(report.failed.isEmpty)
        let records = try await client.listUploads(cursor: nil).items
        for id in ids {
            let rec = records.first { $0.localIdentifier == "\(id)#photo" }
            #expect(rec?.status == .complete)   // really finalized on the server
        }
    }

    /// The resume case that matters: a saved entry already fully uploaded. The real
    /// server 409s the HEAD with "already completed"; that must count as SUCCESS.
    @Test func alreadyCompletedOnARealServerCountsAsUploaded() async throws {
        guard let c = config else { return }
        let (coord, _) = coordinator(c)
        let id = "live-dup-\(UUID().uuidString)"

        _ = try await coord.resume(resources: [resource(id)])     // first pass completes it
        let second = try await coord.resume(resources: [resource(id)])   // re-drive a stale saved entry

        #expect(second.uploaded.map(\.localIdentifier) == [id])   // not a failure
        #expect(second.failed.isEmpty)
    }
}
