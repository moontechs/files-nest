import Foundation

/// Reconciles the library against the server: enumerate → page → diff → upload
/// queue → delete queue → structured report. Composes the concrete ServerClient
/// and AssetUploader directly (spec §6).
public struct SyncCoordinator: Sendable {
    private let client: ServerClient
    private let library: any AssetLibrary
    private let uploader: AssetUploader
    private let state: any SyncStateStore
    private let configuredConcurrency: Int?
    private let now: @Sendable () -> Date

    private static let defaultConcurrency = 4
    /// One O(N) encode of a shrinking list per tick, not per file.
    private static let persistEvery = 500

    public init(client: ServerClient,
                library: any AssetLibrary,
                uploader: AssetUploader,
                state: any SyncStateStore,
                configuredConcurrency: Int? = nil,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.client = client
        self.library = library
        self.uploader = uploader
        self.state = state
        self.configuredConcurrency = configuredConcurrency
        self.now = now
    }

    public func sync(range: SyncRange,
                     onProgress: @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        state.saveLastSyncStarted(now())

        let libraryResources = try await library.resources(in: range)
        let serverRecords = try await pagedServerRecords()
        let plan = SyncPlanner.plan(library: libraryResources, server: serverRecords, range: range)

        let result = try await runUploads(plan.uploads, onProgress: onProgress)
        let uploaded = result.uploaded
        var failed = result.failed        // the deletes loop below still appends delete failures
        var deleted: [ResourceKey] = []

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

    /// Re-drive a saved list of resources without enumerating or diffing. Each is
    /// rebuilt as a `.create` (idempotent server-side); every file still HEADs its
    /// offset, so half-done/done/new all resolve correctly.
    public func resume(resources: [AssetResource],
                       onProgress: @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        state.saveLastSyncStarted(now())
        let uploads = resources.map { PlannedUpload(resource: $0, mode: .create) }
        let (uploaded, failed) = try await runUploads(uploads, onProgress: onProgress)
        return SyncReport(uploaded: uploaded, deleted: [], failed: failed, skipped: 0)
    }

    /// Injected value wins (tests, future Settings UI). Otherwise discover the cap
    /// from GET /config, falling back to the default when the endpoint is absent
    /// (old server) or unreachable — config never fails a sync.
    private func resolveCap() async throws -> Int {
        if let injected = configuredConcurrency { return max(1, injected) }
        let discovered: Int
        do {
            discovered = try await client.config().maxConcurrentUploads
        } catch is CancellationError {
            throw CancellationError()   // never let a cancel look like a config miss
        } catch {
            discovered = Self.defaultConcurrency
        }
        return max(1, discovered)
    }

    /// Bounded sliding-window upload of `uploads`, persisting the not-yet-uploaded
    /// resources (throttled + on cancel) so a pause/quit/launch can resume without
    /// re-scanning. Ends by writing the final remaining (empty when all succeeded).
    private func runUploads(_ uploads: [PlannedUpload],
                            onProgress: @Sendable (SyncProgress) -> Void) async throws
        -> (uploaded: [ResourceKey], failed: [FailedItem]) {
        let cap = try await resolveCap()
        // Captured up front: if the engine clears the list mid-run (sign-out, server change),
        // this run's later writes are dropped rather than resurrecting a superseded plan.
        let session = state.remainingUploadsSession()
        let uploadTotal = uploads.count

        var uploaded: [ResourceKey] = []
        var failed: [FailedItem] = []
        var iterator = uploads.makeIterator()
        // In-flight uploads in start order. The strip reports the most-recently-
        // started one that is STILL running (the last element) — never one that has
        // already completed while an older upload is still in flight.
        var inFlightItems: [(key: ResourceKey, name: String)] = []
        var sincePersist = 0

        func remaining() -> [AssetResource] {
            let done = Set(uploaded.map(\.encoded))
            return uploads.filter { !done.contains($0.resource.key.encoded) }.map(\.resource)
        }
        // `completed` is successful uploads so far (not attempts), so a live "backed
        // up" count derived from it never credits a failed item.
        func emit() {
            let current = inFlightItems.last
            onProgress(SyncProgress(completed: uploaded.count,
                                    total: uploadTotal,
                                    currentItemName: current?.name,
                                    bytesRemaining: nil,
                                    currentItemID: current?.key.localIdentifier,
                                    inFlight: inFlightItems.count))
        }

        do {
            try await withThrowingTaskGroup(of: UploadOutcome.self) { group in
                // Adds one upload task if any remain. Runs only on this coordinator
                // coroutine, so the in-flight list and `onProgress` stay single-threaded
                // even though the uploads themselves run concurrently.
                func addNext() -> Bool {
                    guard let item = iterator.next() else { return false }
                    inFlightItems.append((item.resource.key, item.resource.filename))
                    group.addTask {
                        try Task.checkCancellation()
                        do {
                            try await execute(item)
                            return .success(item.resource.key)
                        } catch is CancellationError {
                            throw CancellationError()
                        } catch ServerClientError.alreadyCompleted {
                            return .success(item.resource.key)   // already on the server → done
                        } catch {
                            return .failed(FailedItem(key: item.resource.key,
                                                      filename: item.resource.filename,
                                                      reason: String(describing: error)))
                        }
                    }
                    emit()   // newest still-in-flight item becomes the reported current
                    return true
                }

                for _ in 0..<cap { if !addNext() { break } }

                while let outcome = try await group.next() {
                    let completedKey: ResourceKey
                    switch outcome {
                    case .success(let key): uploaded.append(key); completedKey = key
                    case .failed(let item): failed.append(item); completedKey = item.key
                    }
                    if let idx = inFlightItems.firstIndex(where: { $0.key == completedKey }) {
                        inFlightItems.remove(at: idx)
                    }
                    sincePersist += 1
                    if sincePersist >= Self.persistEvery {
                        sincePersist = 0
                        state.saveRemainingUploads(remaining(), session: session)
                    }
                    if !addNext() { emit() }   // refresh completed + drained in-flight set
                }
            }
        } catch is CancellationError {
            state.saveRemainingUploads(remaining(), session: session)   // final write on pause/quit
            throw CancellationError()
        }
        state.saveRemainingUploads(remaining(), session: session)   // clean finish → remaining is the failures (empty if none)
        return (uploaded, failed)
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

/// Result of one concurrent upload task. A per-item failure is a value, not a
/// thrown error — throwing out of a task would cancel its siblings.
private enum UploadOutcome: Sendable {
    case success(ResourceKey)
    case failed(FailedItem)
}
