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

// MARK: - Task 6: backend_lost recovery
extension SyncCoordinatorTests {
    // Proactive: planner sees a backend_lost record → re-register (POST) → upload.
    // The server ReRegisters IN PLACE (same id), so no delete and no duplicate.
    @Test func backendLostRecordIsRecoveredProactively() async throws {
        let server = FakeServer(host: "sc-recover-proactive.test")
        let lostID = server.seed(localIdentifier: "A#photo", status: "backend_lost")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(report.failed.isEmpty)
        // Same record, resurrected in place (SafeID is deterministic — id is reused).
        #expect(server.all().count == 1)
        #expect(server.record(id: lostID)?.status == "complete")
        #expect(server.record(id: lostID)?.offset == 250)
        // Recovery must NOT delete — deleting would create the stranding tombstone.
        #expect(!server.events.contains { $0.hasPrefix("DELETE") })
    }

    // Reactive: planner sees `uploading`, but the server has since lost the backend
    // (HEAD returns 409). Coordinator re-registers the SAME record mid-flight.
    @Test func backendLostDuringResumeIsRecoveredReactively() async throws {
        let server = FakeServer(host: "sc-recover-reactive.test")
        let staleID = server.seed(localIdentifier: "A#photo", status: "uploading", offset: 100)
        server.backendLostIDs = [staleID]   // HEAD/PATCH on this id → 409 backend_lost
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(report.failed.isEmpty)
        #expect(server.all().count == 1)
        #expect(server.record(id: staleID)?.status == "complete")
        #expect(server.record(id: staleID)?.offset == 250) // re-registered from offset 0, fully uploaded
    }

    // Recovery that fails again → the item lands in `failed`, sync continues, and
    // — crucially — NO `deleted` tombstone is created for the still-present asset.
    @Test func recoveryThatFailsAgainRecordsFailure() async throws {
        // markLostOnFirstDataOp: even the re-registered record is lost on its first
        // data op, so the single recovery attempt fails too.
        let server = FakeServer(host: "sc-recover-fail.test")
        server.markLostOnFirstDataOp = true
        server.seed(localIdentifier: "A#photo", status: "backend_lost")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded.isEmpty)
        #expect(report.failed.map(\.key) == [ResourceKey(localIdentifier: "A", kind: .photo)])
        // No stranding tombstone: recovery never deletes, so the record is not `deleted`.
        #expect(server.all().allSatisfy { $0.status != "deleted" })
    }

    // No stranding across syncs: a failed recovery leaves a resumable record (not a
    // `deleted` tombstone the planner would skip), so the NEXT sync completes it.
    @Test func recoveryFailureLeavesResumableRecordForNextSync() async throws {
        let server = FakeServer(host: "sc-recover-twosync.test")
        server.markLostOnFirstDataOp = true
        server.seed(localIdentifier: "A#photo", status: "backend_lost")

        let r1 = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)
        #expect(r1.failed.count == 1)
        #expect(server.all().allSatisfy { $0.status != "deleted" }) // asset not stranded

        // Server stops losing the backend; the still-present asset must complete now.
        server.markLostOnFirstDataOp = false
        server.backendLostIDs = []
        let r2 = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)
        #expect(r2.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(server.all().count == 1)
        #expect(server.all().contains { $0.status == "complete" })
    }
}

// MARK: - Task 7: failure policy (skip-and-continue + cancellation)
extension SyncCoordinatorTests {
    // One item's source read fails; the sync does NOT abort — both items are
    // attempted and reported.
    @Test func failedItemIsRecordedAndSyncContinues() async throws {
        let server = FakeServer(host: "sc-continue.test")
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [resource("A", date: "2024-01-01T00:00:00Z"),
                                              resource("B", date: "2024-02-01T00:00:00Z")], error: nil),
            uploader: AssetUploader(client: client,
                                    source: FakeAssetDataSource(totalBytes: 1000, blobSize: 100, failAfterBlobs: 1)),
            state: InMemorySyncStateStore(),
            now: { Date(timeIntervalSince1970: 0) })

        let report = try await coord.sync(range: .all)
        // Neither completed, but BOTH were attempted (no fail-fast) and reported.
        #expect(report.uploaded.isEmpty)
        #expect(Set(report.failed.map { $0.key.localIdentifier }) == ["A", "B"])
        // Both create calls happened → two records exist server-side (proof it didn't abort after A).
        #expect(server.all().count == 2)
    }

    // A delete failure is recorded, and the remaining deletes still run.
    @Test func failedDeleteIsRecordedAndOthersContinue() async throws {
        let server = FakeServer(host: "sc-deletefail.test")
        server.failDeleteIDs = ["id-0"]                                          // id-0 → DELETE 500
        server.seed(localIdentifier: "GONE1#photo", status: "complete")         // id-0 → delete fails
        server.seed(localIdentifier: "GONE2#photo", status: "complete")         // id-1 → delete ok
        let report = try await makeCoordinator(server: server, library: []).sync(range: .all)

        #expect(report.deleted == [ResourceKey(localIdentifier: "GONE2", kind: .photo)])
        #expect(report.failed.map { $0.key.localIdentifier } == ["GONE1"])
    }

    // Cancellation stops promptly and propagates (not swallowed into `failed`).
    @Test func cancellationStopsAndThrows() async throws {
        let server = FakeServer(host: "sc-cancel.test")
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [resource("A")], error: nil),
            uploader: AssetUploader(client: client,
                                    source: FakeAssetDataSource(totalBytes: 10_000_000, blobSize: 1000)),
            state: InMemorySyncStateStore(),
            now: { Date(timeIntervalSince1970: 0) })

        let task = Task { try await coord.sync(range: .all) }
        try await Task.sleep(for: .milliseconds(30))
        task.cancel()
        await #expect(throws: CancellationError.self) { _ = try await task.value }
    }
}

// MARK: - Task 8: range-scoped delete integration
extension SyncCoordinatorTests {
    // End-to-end proof of spec §5.3 through the coordinator: a January sync must
    // NOT delete a February backup even though listUploads returns everything.
    @Test func januarySyncDoesNotDeleteFebruaryBackup() async throws {
        let server = FakeServer(host: "sc-range.test")
        let febID = server.seed(localIdentifier: "FEB#photo", status: "complete",
                                creationDate: "2024-02-10T12:00:00Z")
        let jan = date("2024-01-01T00:00:00Z")...date("2024-01-31T23:59:59Z")
        let report = try await makeCoordinator(
            server: server,
            library: [resource("JAN", date: "2024-01-15T09:00:00Z")]).sync(range: .dates(jan))

        #expect(report.uploaded == [ResourceKey(localIdentifier: "JAN", kind: .photo)])
        #expect(report.deleted.isEmpty)
        #expect(server.record(id: febID)?.status == "complete") // untouched
    }

    // The coordinator must forward the requested range to the library (enumeration
    // is range-scoped at the source, not just in the planner).
    @Test func coordinatorForwardsRangeToLibrary() async throws {
        let server = FakeServer(host: "sc-range-forward.test")
        let lib = FakeAssetLibrary(items: [], error: nil)
        let client = server.client()
        let coord = SyncCoordinator(
            client: client, library: lib,
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 10, blobSize: 10)),
            state: InMemorySyncStateStore(), now: { Date(timeIntervalSince1970: 0) })
        let jan = date("2024-01-01T00:00:00Z")...date("2024-01-31T23:59:59Z")
        _ = try await coord.sync(range: .dates(jan))
        #expect(lib.requestedRanges == [.dates(jan)])
    }
}

// MARK: - Progress hook
extension SyncCoordinatorTests {
    @Test func progressFiresOncePerUploadInPlanOrder() async throws {
        let server = FakeServer(host: "sc-progress.test")
        let a = AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                              filename: "A.jpg", creationDate: date("2024-01-01T00:00:00Z"), bundleID: nil)
        let b = AssetResource(key: ResourceKey(localIdentifier: "B", kind: .photo),
                              filename: "B.jpg", creationDate: date("2024-02-01T00:00:00Z"), bundleID: nil)
        let box = ProgressBox()

        _ = try await makeCoordinator(server: server, library: [a, b])
            .sync(range: .all, onProgress: { box.append($0) })

        #expect(box.values == [
            SyncProgress(completed: 0, total: 2, currentItemName: "A.jpg", bytesRemaining: nil),
            SyncProgress(completed: 1, total: 2, currentItemName: "B.jpg", bytesRemaining: nil),
        ])
    }

    @Test func progressNotFiredWhenNothingToUpload() async throws {
        let server = FakeServer(host: "sc-progress-empty.test")
        let box = ProgressBox()
        _ = try await makeCoordinator(server: server, library: [])
            .sync(range: .all, onProgress: { box.append($0) })
        #expect(box.values.isEmpty)
    }
}

/// Thread-safe collector for the `@Sendable` progress callback.
final class ProgressBox: @unchecked Sendable {
    private let lock = NSLock()
    private var _values: [SyncProgress] = []
    func append(_ p: SyncProgress) { lock.lock(); _values.append(p); lock.unlock() }
    var values: [SyncProgress] { lock.lock(); defer { lock.unlock() }; return _values }
}
