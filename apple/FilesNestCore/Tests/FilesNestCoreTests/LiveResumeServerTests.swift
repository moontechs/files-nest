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

    /// Walks every page — the store accumulates records across runs, so a single page is
    /// not enough to find a just-uploaded item.
    private func allRecords(_ client: ServerClient) async throws -> [UploadRecord] {
        var out: [UploadRecord] = []
        var cursor: String? = nil
        repeat {
            let page = try await client.listUploads(cursor: cursor)
            out += page.items
            cursor = page.nextCursor
        } while cursor != nil
        return out
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
        let records = try await allRecords(client)
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

    /// Checklist row: "Relaunch goes straight to Backing up (no Counting 0 of N), uploads the
    /// remaining files, then briefly reconciles." Drives the WHOLE engine against a real
    /// server, asserting the order of states — the one thing a fake server cannot vouch for.
    @Test(.timeLimit(.minutes(1)))
    func coldLaunchWithASavedListUploadsBeforeAnyCount() async throws {
        guard let c = config else { return }
        let state = InMemorySyncStateStore()
        let ids = (0..<3).map { "live-launch-\($0)-\(UUID().uuidString)" }
        state.saveRemainingUploads(ids.map(resource))

        let order = OrderRecorder()
        let creds = StaticCredentialStore(c.creds)
        let client = ServerClient(baseURL: c.url, credentials: creds)
        let make: @Sendable () -> SyncCoordinator = {
            SyncCoordinator(client: client,
                            library: FakeAssetLibrary(items: [], error: nil),
                            uploader: AssetUploader(client: client,
                                                    source: FakeAssetDataSource(totalBytes: 250, blobSize: 100)),
                            state: state, configuredConcurrency: 2,
                            now: { Date(timeIntervalSince1970: 1_700_000_000) })
        }
        let engine = LiveSyncEngine(
            credentials: creds, state: state,
            perform: { range, onProgress in
                order.mark("sync"); return try await make().sync(range: range, onProgress: onProgress)
            },
            resume: { resources, onProgress in
                order.mark("resume"); return try await make().resume(resources: resources, onProgress: onProgress)
            },
            assess: { _, _ in
                order.mark("assess"); return Assessment(backedUp: 3, pending: 0, resourceTotal: 3)
            })

        // Record every status the panel would render, in order.
        let seen = OrderRecorder()
        let watcher = Task {
            for await s in engine.statusStream() {
                switch s {
                case .counting(_, _, let purpose): seen.mark("counting(\(purpose == .verify ? "verify" : "survey"))")
                case .syncing: seen.mark("syncing")
                case .watching: seen.mark("watching")
                default: break
                }
            }
        }
        await engine.start()
        while !seen.marks.contains("watching") { await Task.yield() }
        watcher.cancel()

        // Uploading came first; the only count is the verification pass that follows it.
        #expect(order.marks.first == "resume")
        #expect(seen.marks.first == "syncing")
        #expect(!seen.marks.contains("counting(survey)"))
        #expect(seen.marks.contains("counting(verify)"))

        // The files really landed on the server.
        let records = try await allRecords(client)
        for id in ids {
            #expect(records.first { $0.localIdentifier == "\(id)#photo" }?.status == .complete)
        }
    }
}
