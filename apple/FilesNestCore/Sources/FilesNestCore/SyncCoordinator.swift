import Foundation

/// Reconciles the library against the server: enumerate → page → diff → upload
/// queue → delete queue → structured report. Composes the concrete ServerClient
/// and AssetUploader directly (spec §6).
public struct SyncCoordinator: Sendable {
    private let client: ServerClient
    private let library: any AssetLibrary
    private let uploader: AssetUploader
    private let state: any SyncStateStore
    private let now: @Sendable () -> Date

    public init(client: ServerClient,
                library: any AssetLibrary,
                uploader: AssetUploader,
                state: any SyncStateStore,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.client = client
        self.library = library
        self.uploader = uploader
        self.state = state
        self.now = now
    }

    public func sync(range: SyncRange,
                     onProgress: @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        state.saveLastSyncStarted(now())

        let libraryResources = try await library.resources(in: range)
        let serverRecords = try await pagedServerRecords()
        let plan = SyncPlanner.plan(library: libraryResources, server: serverRecords, range: range)

        var uploaded: [ResourceKey] = []
        var deleted: [ResourceKey] = []
        var failed: [FailedItem] = []

        let uploadTotal = plan.uploads.count
        for item in plan.uploads {
            try Task.checkCancellation()
            // `completed` is successful uploads so far (not attempts), so a live "backed up"
            // count derived from it never credits a failed item.
            onProgress(SyncProgress(completed: uploaded.count,
                                    total: uploadTotal,
                                    currentItemName: item.resource.filename,
                                    bytesRemaining: nil))
            do {
                try await execute(item)
                uploaded.append(item.resource.key)
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                failed.append(FailedItem(key: item.resource.key,
                                         filename: item.resource.filename,
                                         reason: String(describing: error)))
            }
        }

        for del in plan.deletes {
            try Task.checkCancellation()
            do {
                try await client.deleteUpload(id: del.uploadID)
                deleted.append(del.key)
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                failed.append(FailedItem(key: del.key,
                                         filename: del.key.encoded,
                                         reason: String(describing: error),
                                         kind: .delete))
            }
        }

        return SyncReport(uploaded: uploaded, deleted: deleted, failed: failed, skipped: plan.skipped)
    }

    private func pagedServerRecords() async throws -> [UploadRecord] {
        var records: [UploadRecord] = []
        var cursor: String? = nil
        repeat {
            try Task.checkCancellation()
            let page = try await client.listUploads(cursor: cursor)
            records.append(contentsOf: page.items)
            cursor = page.nextCursor
        } while cursor != nil
        return records
    }

    private func execute(_ item: PlannedUpload) async throws {
        let assetKey = item.resource.key.encoded
        switch item.mode {
        case .create:
            let record = try await create(item.resource)
            try await uploadWithRecovery(assetKey: assetKey, uploadID: record.id, resource: item.resource)
        case .resume(let uploadID):
            try await uploadWithRecovery(assetKey: assetKey, uploadID: uploadID, resource: item.resource)
        case .recover:
            try await recover(assetKey: assetKey, resource: item.resource)
        }
    }

    /// Upload, recovering ONCE from a mid-flight backend_lost (spec §6 step 5).
    private func uploadWithRecovery(assetKey: String, uploadID: String,
                                    resource: AssetResource) async throws {
        do {
            try await uploader.upload(assetID: assetKey, uploadID: uploadID)
        } catch ServerClientError.backendLost {
            try await recover(assetKey: assetKey, resource: resource)
        }
    }

    /// Re-register via POST and upload from 0. The server resets a `backend_lost`
    /// record back to `uploading` with a fresh backend (`ReRegister`,
    /// `handlers.go:258`), so NO `deleteUpload` is needed. Deleting first would be
    /// redundant (the lost backend is already gone) and would leave a `deleted`
    /// tombstone if recovery were interrupted — which the planner skips, stranding
    /// a still-present asset. A mid-recovery failure instead leaves a resumable
    /// `uploading` record. No further recovery on a second backend_lost.
    private func recover(assetKey: String, resource: AssetResource) async throws {
        let record = try await create(resource)
        try await uploader.upload(assetID: assetKey, uploadID: record.id)
    }

    private func create(_ resource: AssetResource) async throws -> UploadRecord {
        try await client.createUpload(CreateUploadRequest(
            localIdentifier: resource.key.encoded,
            filename: resource.filename,
            creationDate: resource.creationDate,
            bundleID: resource.bundleID))
    }
}
