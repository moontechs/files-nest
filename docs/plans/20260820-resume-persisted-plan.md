# Persisted Resume + Fast Warm-Launch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist the list of files still to upload so Resume and cold launch start backing up immediately instead of re-counting the whole library.

**Architecture:** `SyncCoordinator` (which already holds the `SyncStateStore`) persists the not-yet-uploaded `AssetResource`s (throttled + on cancel, cleared on clean finish) and gains a `resume(resources:)` entry that re-drives them as idempotent `.create` uploads. `LiveSyncEngine` reads the saved list on launch and on resume and fast-paths straight into an upload, then chains a normal `.all` reconcile.

**Tech Stack:** Swift 6 strict concurrency, swift-testing (`import Testing`), pure Foundation in `FilesNestCore`; SwiftUI app target.

**Spec:** `docs/design/20260820-resume-persisted-plan.md`

## Global Constraints

- Swift 6 language mode; `FilesNestCore` is pure Foundation (no PhotoKit/SwiftUI).
- Tests: swift-testing, `@testable import FilesNestCore`, fakes under `Tests/FilesNestCoreTests/Support/`.
- TDD mandatory: failing test first, watch it fail, implement to green, commit per task.
- Core test run: `cd apple/FilesNestCore && swift test`; single: `swift test --filter <Name>`.
- App build: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`.
- Persist throttle: **every 500 completions** (`persistEvery = 500`).
- Re-drive rebuilds each saved resource as `PlannedUpload(mode: .create)`; `ServerClientError.alreadyCompleted` counts as a **successful** upload.
- Builds on branch `apple-clients/upload-concurrency` (PR #27). Paths are relative to `apple/FilesNestCore/` unless prefixed `apple/macos/`.
- Commit messages: no `Co-Authored-By`/bot-attribution trailer.

---

### Task 1: Codable for `AssetResource` / `ResourceKey` / `ResourceKind`

**Files:**
- Modify: `Sources/FilesNestCore/ResourceKey.swift` (add `Codable` to `ResourceKind` and `ResourceKey`)
- Modify: `Sources/FilesNestCore/Models/AssetResource.swift` (add `Codable`)
- Test: `Tests/FilesNestCoreTests/AssetResourceCodableTests.swift` (create)

**Interfaces:**
- Produces: `AssetResource: Codable`, `ResourceKey: Codable`, `ResourceKind: Codable`.

- [ ] **Step 1: Write the failing test**

Create `Tests/FilesNestCoreTests/AssetResourceCodableTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Test func assetResourceRoundTripsThroughJSON() throws {
    let original = AssetResource(
        key: ResourceKey(localIdentifier: "ABC#weird", kind: .pairedVideo),
        filename: "IMG_0001.mov",
        creationDate: Date(timeIntervalSince1970: 1_700_000_000),
        bundleID: "com.example.live")
    let data = try JSONEncoder().encode(original)
    let decoded = try JSONDecoder().decode(AssetResource.self, from: data)
    #expect(decoded == original)
}

@Test func assetResourceArrayRoundTrips() throws {
    let items = (0..<3).map {
        AssetResource(key: ResourceKey(localIdentifier: "A\($0)", kind: .photo),
                      filename: "IMG\($0).jpg",
                      creationDate: Date(timeIntervalSince1970: 1_700_000_000 + Double($0)),
                      bundleID: nil)
    }
    let data = try JSONEncoder().encode(items)
    #expect(try JSONDecoder().decode([AssetResource].self, from: data) == items)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter assetResourceRoundTripsThroughJSON`
Expected: FAIL — `AssetResource` does not conform to `Encodable`/`Decodable` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `ResourceKey.swift`, change the two type declarations to add `Codable`:

```swift
public enum ResourceKind: String, Sendable, CaseIterable, Codable {
```

```swift
public struct ResourceKey: Sendable, Equatable, Codable {
```

In `AssetResource.swift`:

```swift
public struct AssetResource: Sendable, Equatable, Codable {
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter assetResource`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/ResourceKey.swift Sources/FilesNestCore/Models/AssetResource.swift Tests/FilesNestCoreTests/AssetResourceCodableTests.swift
git commit -m "feat(core): make AssetResource/ResourceKey/ResourceKind Codable"
```

---

### Task 2: `SyncStateStore` remaining-uploads API

**Files:**
- Modify: `Sources/FilesNestCore/SyncStateStore.swift` (protocol + `UserDefaultsSyncStateStore`)
- Modify: `Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift`
- Test: `Tests/FilesNestCoreTests/SyncStateStoreTests.swift` (create)

**Interfaces:**
- Consumes: `[AssetResource]` (Codable, Task 1).
- Produces: on `SyncStateStore` — `saveRemainingUploads(_:)`, `loadRemainingUploads() -> [AssetResource]` (`[]` when absent/undecodable), `clearRemainingUploads()`.

- [ ] **Step 1: Write the failing test**

Create `Tests/FilesNestCoreTests/SyncStateStoreTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Test func inMemoryRemainingUploadsRoundTrips() {
    let store = InMemorySyncStateStore()
    #expect(store.loadRemainingUploads().isEmpty)
    let items = [AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                               filename: "A.jpg", creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)]
    store.saveRemainingUploads(items)
    #expect(store.loadRemainingUploads() == items)
    store.clearRemainingUploads()
    #expect(store.loadRemainingUploads().isEmpty)
}

@Test func userDefaultsRemainingUploadsRoundTripsAndToleratesGarbage() {
    let defaults = UserDefaults(suiteName: "remaining-\(UUID().uuidString)")!
    let store = UserDefaultsSyncStateStore(defaults: defaults)
    #expect(store.loadRemainingUploads().isEmpty)
    let items = [AssetResource(key: ResourceKey(localIdentifier: "A", kind: .video),
                               filename: "A.mov", creationDate: Date(timeIntervalSince1970: 2), bundleID: "b")]
    store.saveRemainingUploads(items)
    #expect(store.loadRemainingUploads() == items)
    // Undecodable value → [] (clean fallback to a normal count).
    defaults.set(Data([0x00, 0x01]), forKey: "com.filesnest.sync.remainingUploads")
    #expect(store.loadRemainingUploads().isEmpty)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter inMemoryRemainingUploadsRoundTrips`
Expected: FAIL — `loadRemainingUploads` is not a member (compile error).

- [ ] **Step 3: Write minimal implementation**

In `SyncStateStore.swift`, add to the protocol:

```swift
    func loadRemainingUploads() -> [AssetResource]
    func saveRemainingUploads(_ resources: [AssetResource])
    func clearRemainingUploads()
```

Add to `UserDefaultsSyncStateStore` (new key + JSON):

```swift
    private let remainingKey = "com.filesnest.sync.remainingUploads"

    public func loadRemainingUploads() -> [AssetResource] {
        guard let data = defaults.data(forKey: remainingKey) else { return [] }
        return (try? JSONDecoder().decode([AssetResource].self, from: data)) ?? []
    }

    public func saveRemainingUploads(_ resources: [AssetResource]) {
        if let data = try? JSONEncoder().encode(resources) { defaults.set(data, forKey: remainingKey) }
    }

    public func clearRemainingUploads() { defaults.removeObject(forKey: remainingKey) }
```

In `Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift`, add storage + methods:

```swift
    private var _remaining: [AssetResource] = []

    func loadRemainingUploads() -> [AssetResource] { lock.lock(); defer { lock.unlock() }; return _remaining }
    func saveRemainingUploads(_ resources: [AssetResource]) { lock.lock(); defer { lock.unlock() }; _remaining = resources }
    func clearRemainingUploads() { lock.lock(); defer { lock.unlock() }; _remaining = [] }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter RemainingUploads`
Expected: PASS. Also `swift test` compiles (any other `SyncStateStore` conformer would now fail to build — there are none besides these two).

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/SyncStateStore.swift Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift Tests/FilesNestCoreTests/SyncStateStoreTests.swift
git commit -m "feat(core): SyncStateStore persists a remaining-uploads list"
```

---

### Task 3: `SyncCoordinator` persists remaining (extract `runUploads`)

**Files:**
- Modify: `Sources/FilesNestCore/SyncCoordinator.swift`
- Test: `Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`

**Interfaces:**
- Consumes: `SyncStateStore.saveRemainingUploads/loadRemainingUploads` (Task 2).
- Produces: private `runUploads(_ uploads:onProgress:)`; private `resolveCap()`. `sync` persists the not-yet-uploaded resources (throttled every 500 + on cancel) and ends by writing the final remaining (empty when all succeeded).

- [ ] **Step 1: Write the failing tests**

Add to `SyncCoordinatorTests.swift`:

```swift
    @Test func cleanSyncClearsRemaining() async throws {
        let server = FakeServer(host: "sc-remain-clear.test")
        let state = InMemorySyncStateStore()
        let report = try await makeCoordinator(server: server, library: [resource("A"), resource("B")],
                                               state: state).sync(range: .all)
        #expect(report.uploaded.count == 2)
        #expect(state.loadRemainingUploads().isEmpty)   // nothing left after a full sync
    }

    @Test func cancelledSyncPersistsNotYetUploaded() async throws {
        // Two items; A blocks until progress reports completed==1, then we cancel.
        let server = FakeServer(host: "sc-remain-cancel.test")
        let state = InMemorySyncStateStore()
        let a = resource("A", date: "2024-06-15T10:00:00Z")
        let b = resource("B", date: "2024-06-15T10:01:00Z")
        let baton = Baton()
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [a, b], error: nil),
            uploader: AssetUploader(client: client,
                                    source: OrderedDataSource(baton: baton, waitID: "A#photo",
                                                              totalBytes: 250, blobSize: 100)),
            state: state,
            configuredConcurrency: 2,
            now: { Date(timeIntervalSince1970: 1_700_000_000) })

        let task = Task { try await coord.sync(range: .all) { p in
            if p.completed == 1 { /* B done, A still blocked */ }
        } }
        // Give B time to complete and A to be in-flight, then cancel.
        try? await Task.sleep(for: .milliseconds(50))
        task.cancel()
        baton.release()                    // let A unwind
        _ = try? await task.value

        // A never successfully uploaded, so it's the persisted remaining.
        let remaining = state.loadRemainingUploads().map { $0.key.localIdentifier }
        #expect(remaining.contains("A"))
        #expect(!remaining.contains("B"))
    }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter cancelledSyncPersistsNotYetUploaded`
Expected: FAIL — `loadRemainingUploads()` stays empty (coordinator does not persist yet).

- [ ] **Step 3: Write the implementation**

In `SyncCoordinator.swift`, add the throttle constant near `defaultConcurrency`:

```swift
    private static let persistEvery = 500
```

Extract cap resolution into a helper (replace the inline `let cap …` block inside `sync` with a call). Add:

```swift
    private func resolveCap() async throws -> Int {
        if let injected = configuredConcurrency { return max(1, injected) }
        let discovered: Int
        do { discovered = try await client.config().maxConcurrentUploads }
        catch is CancellationError { throw CancellationError() }
        catch { discovered = Self.defaultConcurrency }
        return max(1, discovered)
    }
```

In `sync`, replace everything from the `let cap …` line through the end of the `withThrowingTaskGroup { … }` block (the whole PR #27 upload section) with a single call to `runUploads`. The declarations that section used (`var uploaded`, `var failed`, the `cap`/`iterator`/`inFlightItems`/`emit`/`addNext` locals) all move into `runUploads`, so delete them from `sync`. The result is:

```swift
        let result = try await runUploads(plan.uploads, onProgress: onProgress)
        let uploaded = result.uploaded
        var failed = result.failed        // the deletes loop below still appends delete failures
        var deleted: [ResourceKey] = []
```

Keep the existing `for del in plan.deletes { … }` loop and the final `return SyncReport(uploaded: uploaded, deleted: deleted, failed: failed, skipped: plan.skipped)` exactly as they are.

Add the extracted loop with persistence as a private method:

```swift
    /// Bounded sliding-window upload of `uploads`, persisting the not-yet-uploaded
    /// resources (throttled + on cancel) so a pause/quit/launch can resume without
    /// re-scanning. Ends by writing the final remaining (empty when all succeeded).
    private func runUploads(_ uploads: [PlannedUpload],
                            onProgress: @Sendable (SyncProgress) -> Void) async throws
        -> (uploaded: [ResourceKey], failed: [FailedItem]) {
        let cap = try await resolveCap()
        let uploadTotal = uploads.count

        var uploaded: [ResourceKey] = []
        var failed: [FailedItem] = []
        var iterator = uploads.makeIterator()
        var inFlightItems: [(key: ResourceKey, name: String)] = []
        var sincePersist = 0

        func remaining() -> [AssetResource] {
            let done = Set(uploaded)
            return uploads.filter { !done.contains($0.resource.key) }.map(\.resource)
        }
        func emit() {
            let current = inFlightItems.last
            onProgress(SyncProgress(completed: uploaded.count, total: uploadTotal,
                                    currentItemName: current?.name, bytesRemaining: nil,
                                    currentItemID: current?.key.localIdentifier, inFlight: inFlightItems.count))
        }

        do {
            try await withThrowingTaskGroup(of: UploadOutcome.self) { group in
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
                    emit()
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
                    if sincePersist >= Self.persistEvery { sincePersist = 0; state.saveRemainingUploads(remaining()) }
                    if !addNext() { emit() }
                }
            }
        } catch is CancellationError {
            state.saveRemainingUploads(remaining())   // final write on pause/quit
            throw CancellationError()
        }
        state.saveRemainingUploads(remaining())        // clean finish → remaining is the failures (empty if none)
        return (uploaded, failed)
    }
```

Note: `ServerClientError.alreadyCompleted` handling is new here (see Task 4 for the accompanying test); the case already exists in `ServerClientError`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter "cleanSyncClearsRemaining|cancelledSyncPersistsNotYetUploaded"`
Expected: PASS. Then `swift test --filter SyncCoordinatorTests` — all prior coordinator tests still green.

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/SyncCoordinator.swift Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "feat(core): persist remaining uploads during a sync (resume groundwork)"
```

---

### Task 4: `SyncCoordinator.resume(resources:)` + `alreadyCompleted` = success

**Files:**
- Modify: `Sources/FilesNestCore/SyncCoordinator.swift`
- Test: `Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`

**Interfaces:**
- Consumes: `runUploads` (Task 3).
- Produces: `public func resume(resources: [AssetResource], onProgress:) async throws -> SyncReport` — uploads each as `.create`, no enumeration, no deletes.

- [ ] **Step 1: Write the failing tests**

Add to `SyncCoordinatorTests.swift`:

```swift
    @Test func resumeUploadsGivenResourcesWithoutScanning() async throws {
        let server = FakeServer(host: "sc-resume.test")
        let state = InMemorySyncStateStore()
        let client = server.client()
        // A library that FAILS if enumerated — proves resume never scans.
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [], error: FakeSourceError.injected),
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 250, blobSize: 100)),
            state: state,
            configuredConcurrency: 2,
            now: { Date(timeIntervalSince1970: 1_700_000_000) })

        let report = try await coord.resume(resources: [resource("A"), resource("B")])
        #expect(Set(report.uploaded.map(\.localIdentifier)) == ["A", "B"])
        #expect(server.all().count == 2)
        #expect(report.deleted.isEmpty)
    }

    @Test func alreadyCompletedCountsAsUploaded() async throws {
        let server = FakeServer(host: "sc-already.test")
        server.seed(localIdentifier: "A#photo", status: "complete")   // already done on the server
        let state = InMemorySyncStateStore()
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: [], error: FakeSourceError.injected),
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 250, blobSize: 100)),
            state: state,
            configuredConcurrency: 1,
            now: { Date(timeIntervalSince1970: 1_700_000_000) })

        let report = try await coord.resume(resources: [resource("A")])
        #expect(report.uploaded.map(\.localIdentifier) == ["A"])   // not a failure
        #expect(report.failed.isEmpty)
    }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter resumeUploadsGivenResourcesWithoutScanning`
Expected: FAIL — `resume(resources:)` is not a member (compile error).

- [ ] **Step 3: Write minimal implementation**

In `SyncCoordinator.swift`, add after `sync`:

```swift
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
```

(The `alreadyCompleted → .success` branch was added in Task 3's `runUploads`; `alreadyCompletedCountsAsUploaded` verifies it here.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter "resumeUploadsGivenResourcesWithoutScanning|alreadyCompletedCountsAsUploaded"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/SyncCoordinator.swift Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "feat(core): SyncCoordinator.resume(resources:) re-drives a saved plan"
```

---

### Task 5: Engine `Resume` closure + launch fast-path

**Files:**
- Modify: `Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `state.loadRemainingUploads()` (Task 2); `SyncReport`.
- Produces: `LiveSyncEngine.Resume` typealias; `init(… resume: Resume? = nil …)`; on launch, a non-empty saved list drives `resume` before any count, then chains `startIdleCount(.all, autoSync: true)`.

- [ ] **Step 1: Write the failing test**

Add to `LiveSyncEngineTests.swift`:

```swift
    @Test func launchWithSavedListResumesBeforeCounting() async {
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg",
                                                  creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let order = OrderRecorder()
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in order.mark("perform"); return self.emptyReport() },
            assess: { _, _ in order.mark("assess"); return Assessment(backedUp: 1, pending: 0, resourceTotal: 1) },
            resume: { _, _ in order.mark("resume"); return SyncReport(uploaded: [ResourceKey(localIdentifier: "A", kind: .photo)], deleted: [], failed: [], skipped: 0) })
        await engine.start()
        _ = await awaitStatus(engine, isWatching)   // settle through resume → reconcile
        await engine.settle()
        // resume ran, and it ran before the reconcile's assess.
        #expect(order.marks.first == "resume")
        #expect(order.marks.contains("assess"))
    }

    @Test func launchWithEmptySavedListCountsAsBefore() async {
        let engine = LiveSyncEngine(
            credentials: creds(true), state: InMemorySyncStateStore(),
            perform: { _, _ in self.emptyReport() },
            assess: { _, _ in Assessment(backedUp: 0, pending: 0, resourceTotal: 0) },
            resume: { _, _ in Issue.record("resume must not run without a saved list"); return self.emptyReport() })
        await engine.start(); await engine.settle()
        #expect(isWatching(await awaitStatus(engine, isWatching)))
    }
```

Add this recorder at the bottom of `LiveSyncEngineTests.swift` (outside the struct):

```swift
final class OrderRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _marks: [String] = []
    var marks: [String] { lock.lock(); defer { lock.unlock() }; return _marks }
    func mark(_ s: String) { lock.lock(); _marks.append(s); lock.unlock() }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter launchWithSavedListResumesBeforeCounting`
Expected: FAIL — `resume:` is not an argument of `LiveSyncEngine.init` (compile error).

- [ ] **Step 3: Write the implementation**

In `LiveSyncEngine.swift`, add the typealias next to `Perform` (~line 30):

```swift
    public typealias Resume =
        @Sendable ([AssetResource], @Sendable (SyncProgress) -> Void) async throws -> SyncReport
```

Add stored properties (near `perform`, ~line 47) and consumer state (near `pendingLibraryChange`, ~line 65):

```swift
    private let resume: Resume?
```
```swift
    private var resumeReconcilePending = false   // a fast-path upload should chain a reconcile on finish
```

Add `resume` to `init` (after `perform`), defaulting nil, and assign it:

```swift
                perform: @escaping Perform,
                resume: Resume? = nil,
```
```swift
        self.resume = resume
```

Add the fast-path launcher and the resume-finish handler:

```swift
    /// Fast-path: upload a saved list straight away (no count), then chain a full reconcile.
    private func doResumeUpload(_ resources: [AssetResource]) {
        guard signedIn, syncChild == nil, let resume else { return }
        generation &+= 1
        assessChild?.cancel(); assessChild = nil
        let gen = generation
        lastProgress = nil
        currentSyncStartedAt = now()
        syncBaseBackedUp = currentSummary.backedUp
        resumeReconcilePending = true
        setStatus(.syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)))
        syncChild = Task { [resume, submit] in
            do {
                let report = try await resume(resources) { progress in submit(.progress(gen: gen, progress)) }
                submit(.finished(gen: gen, report))
            } catch is CancellationError {
            } catch { submit(.failed(gen: gen, message: String(describing: error))) }
        }
    }

    /// A fast-path upload finished → hand off to a full `.all` reconcile, which sets exact
    /// numbers and catches deletions / photos added while closed. (The coordinator has already
    /// cleared/updated the saved list.)
    private func finishResumeUpload(_ report: SyncReport) {
        syncChild = nil
        lastProgress = nil
        if !report.failed.isEmpty { logFailures(report.failed) }
        resumeReconcilePending = false
        startIdleCount(range: .all, autoSync: true)
    }
```

Route `.finished` through the new handler when a fast-path is in flight. Change the `.finished` case (~line 158):

```swift
        case .finished(let gen, let report):
            if gen == generation { resumeReconcilePending ? finishResumeUpload(report) : finishSync(report) }
```

In `doStart`, the non-paused `else` branch (~line 252-254) becomes:

```swift
            } else {
                let saved = state.loadRemainingUploads()
                if !saved.isEmpty, resume != nil {
                    doResumeUpload(saved)                          // fast-path: upload saved → then reconcile
                } else {
                    startIdleCount(range: .all, autoSync: true)   // launch/restart catch-up (option A)
                }
            }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter "launchWithSavedListResumesBeforeCounting|launchWithEmptySavedListCountsAsBefore"`
Expected: PASS. Then `swift test --filter LiveSyncEngineTests` — prior engine tests green (all construct the engine without `resume:`, which defaults nil → old behavior).

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/LiveSyncEngine.swift Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "feat(core): fast-path launch to a saved-list upload before reconcile"
```

---

### Task 6: Engine resume-from-pause fast-path + guard + clearing

**Files:**
- Modify: `Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `doResumeUpload` (Task 5); `state.clearRemainingUploads()` (Task 2).
- Produces: `doResume` fast-paths when a saved list exists and no change was coalesced; sign-out and reconcile clear the saved list.

- [ ] **Step 1: Write the failing tests**

Add to `LiveSyncEngineTests.swift`:

```swift
    @Test func resumeWithSavedListAndNoChangeFastPaths() async {
        let state = InMemorySyncStateStore()
        let hold = Gate()   // stall the resume upload so the `.syncing` fast-path state is observable
        let engine = LiveSyncEngine(
            credentials: creds(true), state: state,
            perform: { _, _ in self.emptyReport() },
            assess: { _, _ in Assessment(backedUp: 0, pending: 0, resourceTotal: 0) },
            resume: { _, _ in await hold.wait(); return self.emptyReport() })
        // Get to paused, then seed a saved list and resume.
        await engine.start(); await engine.settle()
        engine.pause(); await engine.settle()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg", creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        engine.resume()
        // Fast-path drives `.syncing` (the non-fast-path would go to `.watching`).
        #expect(isSyncing(await awaitStatus(engine, isSyncing)))
    }

    @Test func signOutClearsSavedList() async {
        let state = InMemorySyncStateStore()
        state.saveRemainingUploads([AssetResource(key: ResourceKey(localIdentifier: "A", kind: .photo),
                                                  filename: "A.jpg", creationDate: Date(timeIntervalSince1970: 1), bundleID: nil)])
        let store = MutableCreds(present: true)
        let engine = LiveSyncEngine(credentials: store, state: state,
                                    perform: { _, _ in self.emptyReport() })
        await engine.start(); await engine.settle()
        store.present = false
        await engine.reconcile(); await engine.settle()   // reconcile re-reads creds → signed out
        #expect(state.loadRemainingUploads().isEmpty)
    }
```

`Gate` is the existing Support primitive (`await hold.wait()`, already used by `startCountsThenAssesses`). `MutableCreds` is a small mutable `CredentialStore` fake — add it to `Tests/FilesNestCoreTests/Support/` if absent:

```swift
final class MutableCreds: CredentialStore, @unchecked Sendable {
    private let lock = NSLock(); private var _present: Bool
    init(present: Bool) { _present = present }
    var present: Bool { get { lock.lock(); defer { lock.unlock() }; return _present } set { lock.lock(); _present = newValue; lock.unlock() } }
    func basicCredentials() async throws -> BasicCredentials? { present ? .init(username: "u", password: "p") : nil }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter resumeWithSavedListAndNoChangeFastPaths`
Expected: FAIL — resume closure is never invoked (doResume still goes to `.watching`).

- [ ] **Step 3: Write the implementation**

Replace `doResume` (~line 271-281):

```swift
    private func doResume() {
        log("cmd resume (status=\(currentStatus))")
        guard case .paused = currentStatus else { return }
        let saved = state.loadRemainingUploads()
        if !saved.isEmpty, !pendingLibraryChange, resume != nil {
            assessChild?.cancel(); assessChild = nil
            doResumeUpload(saved)                         // fast-path: upload saved → then reconcile
        } else {
            generation &+= 1
            assessChild?.cancel(); assessChild = nil
            lastProgress = nil
            setStatus(.watching(lastSync: lastSync))
            drainPendingChangeIfAny()                     // honor a change coalesced while paused
        }
    }
```

In `resetToSignedOut` (~line 198) add, next to the other resets:

```swift
        state.clearRemainingUploads()               // a saved list must not survive sign-out
        resumeReconcilePending = false
```

In `doReconcile` (~line 214), after `incrementalAnchor = nil`, add:

```swift
        state.clearRemainingUploads()               // config/server change → re-ground from scratch
        resumeReconcilePending = false
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter "resumeWithSavedListAndNoChangeFastPaths|signOutClearsSavedList"`
Expected: PASS. Then `swift test` — full Core suite green; hammer 3×: `for i in 1 2 3; do swift test || break; done`.

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/LiveSyncEngine.swift Tests/FilesNestCoreTests/LiveSyncEngineTests.swift Tests/FilesNestCoreTests/Support/
git commit -m "feat(core): resume-from-pause fast-path (guarded) + clear saved list on sign-out/reconcile"
```

---

### Task 7: App wiring + manual verification checklist

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Create: `docs/plans/20260820-resume-persisted-plan-verification.md`

**Interfaces:**
- Consumes: `LiveSyncEngine.init(… resume:)` (Task 5); `SyncCoordinator.resume(resources:)` (Task 4).

No unit test — app-target wiring is manual-verify.

- [ ] **Step 1: Wire the resume closure**

In `FilesNestApp.swift`, add a `resume:` argument to the `LiveSyncEngine(...)` construction, mirroring the existing `perform:` closure but calling `resume`:

```swift
            resume: { resources, onProgress in
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    throw NotSignedInError()
                }
                let client   = ServerClient(baseURL: url, credentials: credStore)
                let uploader = AssetUploader(client: client, source: PhotosAssetDataSource())
                let coordinator = SyncCoordinator(client: client,
                                                  library: library,
                                                  uploader: uploader,
                                                  state: stateStore)
                return try await coordinator.resume(resources: resources, onProgress: onProgress)
            },
```

- [ ] **Step 2: Build the app target**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
Expected: BUILD SUCCEEDED.

- [ ] **Step 3: Write the manual checklist**

Create `docs/plans/20260820-resume-persisted-plan-verification.md`:

```markdown
# Manual verification — persisted resume + fast launch

Prereqs: a server with real credentials and a library with a real backlog
(enough that a full count is visibly slow).

- [ ] Start a sync, let some files upload, then Pause. Quit the app.
- [ ] Relaunch: it goes straight to "Backing up" (no "Counting… 0 of N"),
      uploads the remaining files, then briefly reconciles.
- [ ] Pause mid-sync, then Resume: it continues backing up immediately — no
      "Counting 0 of N".
- [ ] Add a few photos while paused, then Resume: the coalesced change path
      still reconciles (safe), and the new photos get uploaded.
- [ ] Delete a photo that was pending, then resume/relaunch: it does not crash;
      the deleted item drops out after the reconcile (may flash once in Failed).
- [ ] Sign out and back in: no stale "Backing up" from a previous account.
```

- [ ] **Step 4: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/FilesNestApp.swift docs/plans/20260820-resume-persisted-plan-verification.md
git commit -m "feat(app): wire resume closure for persisted fast resume/launch"
```

---

## Post-Implementation

- [ ] Full Core suite 3× for flakiness: `cd apple/FilesNestCore && for i in 1 2 3; do swift test || break; done`.
- [ ] Codex review (`codex:rescue`) before finishing/merging (standing workflow rule).
- [ ] Push and open a PR titled `Apple clients: Persisted resume + fast launch (#NN)` — base it on `apple-clients/upload-concurrency` if #27 has not merged, else `main`.
- [ ] Walk the manual verification checklist against a real server.
```
