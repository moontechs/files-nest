import Testing
import Foundation
@testable import FilesNestCore

struct SyncPlannerTests {
    // Helpers -----------------------------------------------------------------
    func date(_ s: String) -> Date { ISO8601DateFormatter().date(from: s)! }

    func res(_ id: String, kind: ResourceKind = .photo, date iso: String = "2024-06-15T10:00:00Z",
             name: String = "IMG.jpg") -> AssetResource {
        AssetResource(key: ResourceKey(localIdentifier: id, kind: kind),
                      filename: name, creationDate: date(iso), bundleID: nil)
    }

    func rec(_ localID: String, kind: ResourceKind = .photo, status: UploadStatus,
             id: String = "srv", date: String? = "2024-06-15T10:00:00Z") -> UploadRecord {
        UploadRecord(id: id,
                     localIdentifier: ResourceKey(localIdentifier: localID, kind: kind).encoded,
                     status: status, backendID: "b", filename: "IMG.jpg", bundleID: nil,
                     creationDate: date, createdAt: nil, updatedAt: nil, organizedPath: nil)
    }

    // Upload-side decisions ---------------------------------------------------
    @Test func newAssetBecomesCreate() {
        let plan = SyncPlanner.plan(library: [res("A")], server: [], range: .all)
        #expect(plan.uploads.count == 1)
        #expect(plan.uploads[0].mode == .create)
        #expect(plan.deletes.isEmpty)
    }

    @Test func uploadingRecordBecomesResume() {
        let plan = SyncPlanner.plan(library: [res("A")],
                                    server: [rec("A", status: .uploading, id: "U1")], range: .all)
        #expect(plan.uploads[0].mode == .resume(uploadID: "U1"))
    }

    @Test func backendLostRecordBecomesRecover() {
        let plan = SyncPlanner.plan(library: [res("A")],
                                    server: [rec("A", status: .backendLost, id: "U2")], range: .all)
        #expect(plan.uploads[0].mode == .recover(uploadID: "U2"))
    }

    @Test(arguments: [UploadStatus.complete, .completing, .deleted])
    func inSyncOrGoneStatusesAreSkipped(_ status: UploadStatus) {
        let plan = SyncPlanner.plan(library: [res("A")],
                                    server: [rec("A", status: status)], range: .all)
        #expect(plan.uploads.isEmpty)
        #expect(plan.skipped == 1)
    }

    // Delete-side decisions ---------------------------------------------------
    @Test(arguments: [UploadStatus.uploading, .complete, .backendLost])
    func serverRecordAbsentFromLibraryIsDeleted(_ status: UploadStatus) {
        let plan = SyncPlanner.plan(library: [],
                                    server: [rec("GONE", status: status, id: "D1")], range: .all)
        #expect(plan.deletes.count == 1)
        #expect(plan.deletes[0].uploadID == "D1")
        #expect(plan.deletes[0].key == ResourceKey(localIdentifier: "GONE", kind: .photo))
    }

    @Test(arguments: [UploadStatus.deleted, .completing])
    func deletedOrCompletingAbsentRecordsAreLeftAlone(_ status: UploadStatus) {
        let plan = SyncPlanner.plan(library: [],
                                    server: [rec("GONE", status: status)], range: .all)
        #expect(plan.deletes.isEmpty)
    }

    // Incremental (.modifiedSince) — upload-only ------------------------------
    @Test func modifiedSinceUploadsMissingButNeverDeletes() {
        // A server record absent from the (windowed) library must NOT be deleted under .modifiedSince.
        let janRec = rec("JAN", status: .complete, id: "J1", date: "2024-01-10T12:00:00Z")
        let plan = SyncPlanner.plan(library: [], server: [janRec], range: .modifiedSince(date("2024-01-01T00:00:00Z")))
        #expect(plan.deletes.isEmpty)
    }

    @Test func modifiedSinceStillPlansUploadsForScannedItems() {
        // Upload side is identical to .all: a scanned library item not on the server is an upload.
        let plan = SyncPlanner.plan(library: [res("NEW")], server: [], range: .modifiedSince(date("2024-01-01T00:00:00Z")))
        #expect(plan.uploads.map { $0.resource.key.encoded } == [ResourceKey(localIdentifier: "NEW", kind: .photo).encoded])
        #expect(plan.deletes.isEmpty)
    }

    // Range scoping (spec §5.3) ----------------------------------------------
    @Test func datesRangeDoesNotDeleteRecordsOutsideWindow() {
        // Library scoped to January; a February server record must survive.
        let jan = date("2024-01-01T00:00:00Z")...date("2024-01-31T23:59:59Z")
        let feb = rec("FEB", status: .complete, id: "F1", date: "2024-02-10T12:00:00Z")
        let plan = SyncPlanner.plan(library: [], server: [feb], range: .dates(jan))
        #expect(plan.deletes.isEmpty)
    }

    @Test func datesRangeDeletesRecordsInsideWindow() {
        let jan = date("2024-01-01T00:00:00Z")...date("2024-01-31T23:59:59Z")
        let janRec = rec("JAN", status: .complete, id: "J1", date: "2024-01-10T12:00:00Z")
        let plan = SyncPlanner.plan(library: [], server: [janRec], range: .dates(jan))
        #expect(plan.deletes.map(\.uploadID) == ["J1"])
    }

    @Test func datesRangeEndpointsAreInclusive() {
        let start = date("2024-01-01T00:00:00Z"); let end = date("2024-01-31T23:59:59Z")
        let onStart = rec("S", status: .complete, id: "S1", date: "2024-01-01T00:00:00Z")
        let onEnd = rec("E", status: .complete, id: "E1", date: "2024-01-31T23:59:59Z")
        let plan = SyncPlanner.plan(library: [], server: [onStart, onEnd], range: .dates(start...end))
        #expect(Set(plan.deletes.map(\.uploadID)) == ["S1", "E1"])
    }

    @Test func nilCreationDateNeverDeletedUnderDatesButDeletedUnderAll() {
        let jan = date("2024-01-01T00:00:00Z")...date("2024-01-31T23:59:59Z")
        let noDate = rec("NODATE", status: .complete, id: "N1", date: nil)
        #expect(SyncPlanner.plan(library: [], server: [noDate], range: .dates(jan)).deletes.isEmpty)
        #expect(SyncPlanner.plan(library: [], server: [noDate], range: .all).deletes.map(\.uploadID) == ["N1"])
    }

    // Live Photo (spec §5.4) --------------------------------------------------
    @Test func livePhotoYieldsTwoCreates() {
        let photo = res("LP", kind: .photo)
        let video = res("LP", kind: .pairedVideo)
        let plan = SyncPlanner.plan(library: [photo, video], server: [], range: .all)
        #expect(plan.uploads.count == 2)
        #expect(plan.uploads.allSatisfy { $0.mode == .create })
        #expect(Set(plan.uploads.map { $0.resource.key.kind }) == [.photo, .pairedVideo])
    }

    @Test func deletedLivePhotoYieldsTwoDeletes() {
        let server = [rec("LP", kind: .photo, status: .complete, id: "P"),
                      rec("LP", kind: .pairedVideo, status: .complete, id: "V")]
        let plan = SyncPlanner.plan(library: [], server: server, range: .all)
        #expect(Set(plan.deletes.map(\.uploadID)) == ["P", "V"])
    }

    // Ordering ----------------------------------------------------------------
    @Test func uploadsOrderedByCreationDateThenKey() {
        let older = res("B", date: "2024-01-01T00:00:00Z")
        let newer = res("A", date: "2024-12-01T00:00:00Z")
        let plan = SyncPlanner.plan(library: [newer, older], server: [], range: .all)
        #expect(plan.uploads.map { $0.resource.key.localIdentifier } == ["B", "A"])
    }
}
