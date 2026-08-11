# SyncCoordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `SyncCoordinator` in `apple/FilesNestCore` — reconcile the Photos library against the server (diff → upload queue → delete queue → structured report), keyed by `ResourceKey.encoded`, closing the latent Live Photo `SafeID` collision.

**Architecture:** A pure `SyncPlanner` computes a `SyncPlan` from `(library resources, server records, range)`; `SyncCoordinator` executes the plan against the existing concrete `ServerClient` + `AssetUploader`. A new `AssetLibrary` seam supplies library resources (faked headlessly). Persistence is a `SyncStateStore` (only `lastSyncStarted`). Coordinator tests exercise the real `ServerClient`/`AssetUploader` through a stateful in-memory `FakeServer` driven by `MockURLProtocol`.

**Tech Stack:** Swift 6, `swift-testing` (`import Testing`), Foundation only (no Photos), SwiftPM (`apple/FilesNestCore`).

## Global Constraints

- **Package:** all Sources under `apple/FilesNestCore/Sources/FilesNestCore/`; all tests under `apple/FilesNestCore/Tests/FilesNestCoreTests/`. SwiftPM globs both — **no `Package.swift` edits**.
- **Core is pure Foundation.** Never `import Photos`; never name a PhotoKit type. macOS 13 floor.
- **Swift 6 strict concurrency:** `NSLock` must not be held across `await` (keep critical sections in sync helpers); no `DispatchSemaphore.wait()` in async contexts.
- **Diff key identity:** the server round-trips whatever `local_identifier` string the client sends and hashes it into the `SafeID`. The client sends `ResourceKey.encoded`. Therefore `UploadRecord.localIdentifier == AssetResource.key.encoded` is the diff key. The bare `PHAsset` localIdentifier never appears as a key.
- **`AssetUploader.upload` already PATCHes all blobs AND calls `markComplete`.** The coordinator must **not** call `markComplete` separately.
- **Testing discipline (spec §7):** every test is failure-injected and **watched to fail first** before the implementation step. A green test nobody watched fail proves nothing.
- **macOS has no `timeout`.** For any run that could hang (cancellation tests), wrap: `perl -e 'alarm 120; exec @ARGV' swift test --filter <name>`.
- **Build gate:** `swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build` must be clean.
- **Serena for all edits** on existing files; new files may be created with Write.
- **Commits:** land on the `synccoordinator` branch (already created off `main`). Do **not** push or merge without an explicit ask from the repo owner. Choosing to execute this plan authorizes the local per-task commit cadence below.
- All working paths below are relative to `files-nest/` (the repo root, `~/Projects/filesnest/files-nest`).

---

### Task 1: Enumeration + range value types

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/Models/AssetResource.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/AssetLibrary.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncValueTypeTests.swift`

**Interfaces:**
- Consumes: `ResourceKey` (existing, `Sendable & Equatable`).
- Produces: `AssetResource(key:filename:creationDate:bundleID:)`, `enum SyncRange { case all; case dates(ClosedRange<Date>) }`, `protocol AssetLibrary { func resources(in: SyncRange) async throws -> [AssetResource] }`.

- [x] **Step 1: Write the failing test**

Create `SyncValueTypeTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct SyncValueTypeTests {
    @Test func assetResourceEquatableAndFieldsRoundTrip() {
        let k = ResourceKey(localIdentifier: "UUID/L0/001", kind: .photo)
        let d = Date(timeIntervalSince1970: 1_700_000_000)
        let a = AssetResource(key: k, filename: "IMG_1.jpg", creationDate: d, bundleID: "UUID/L0/001")
        let b = AssetResource(key: k, filename: "IMG_1.jpg", creationDate: d, bundleID: "UUID/L0/001")
        #expect(a == b)
        #expect(a.key.kind == .photo)
        #expect(a.bundleID == "UUID/L0/001")
    }

    @Test func syncRangeEquatable() {
        let r = Date(timeIntervalSince1970: 0)...Date(timeIntervalSince1970: 100)
        #expect(SyncRange.dates(r) == SyncRange.dates(r))
        #expect(SyncRange.all != SyncRange.dates(r))
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter SyncValueTypeTests`
Expected: FAIL — `cannot find 'AssetResource' in scope` / `cannot find type 'SyncRange'`.

- [x] **Step 3: Write minimal implementation**

`Models/AssetResource.swift`:

```swift
import Foundation

/// One uploadable resource of a Photos asset. A Live Photo yields two of these
/// (`#photo` and `#pairedVideo`) sharing `bundleID`. `key.encoded` is the diff key.
public struct AssetResource: Sendable, Equatable {
    public let key: ResourceKey
    public let filename: String
    public let creationDate: Date
    public let bundleID: String?

    public init(key: ResourceKey, filename: String, creationDate: Date, bundleID: String?) {
        self.key = key
        self.filename = filename
        self.creationDate = creationDate
        self.bundleID = bundleID
    }
}
```

`SyncRange.swift`:

```swift
import Foundation

public enum SyncRange: Sendable, Equatable {
    case all
    case dates(ClosedRange<Date>)
}
```

`AssetLibrary.swift`:

```swift
import Foundation

/// The enumeration seam. The app conforms this to PhotoKit (later slice); tests
/// use `FakeAssetLibrary`. A Live Photo MUST yield both resource keys.
public protocol AssetLibrary: Sendable {
    func resources(in range: SyncRange) async throws -> [AssetResource]
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter SyncValueTypeTests`
Expected: PASS (2 tests).

- [x] **Step 5: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Sources/FilesNestCore/Models/AssetResource.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncRange.swift \
        apple/FilesNestCore/Sources/FilesNestCore/AssetLibrary.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncValueTypeTests.swift
git commit -m "Apple clients: SyncCoordinator value types (AssetResource, SyncRange, AssetLibrary)"
```

---

### Task 2: SyncPlan types + SyncPlanner (pure diff)

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncPlan.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncPlanner.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncPlannerTests.swift`

**Interfaces:**
- Consumes: `AssetResource`, `SyncRange` (Task 1); `UploadRecord`, `UploadStatus`, `ResourceKey` (existing).
- Produces:
  - `SyncPlan(uploads:[PlannedUpload], deletes:[PlannedDelete], skipped:Int)`
  - `PlannedUpload(resource:AssetResource, mode:Mode)` with `enum Mode { case create; case resume(uploadID:String); case recover(uploadID:String) }`
  - `PlannedDelete(uploadID:String, key:ResourceKey)`
  - `SyncPlanner.plan(library:[AssetResource], server:[UploadRecord], range:SyncRange) -> SyncPlan`

- [x] **Step 1: Write the failing tests**

Create `SyncPlannerTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct SyncPlannerTests {
    // Helpers -----------------------------------------------------------------
    func date(_ s: String) -> Date { ISO8601DateFormatter().date(from: s)! }

    func res(_ id: String, kind: ResourceKind = .photo, date: String = "2024-06-15T10:00:00Z",
             name: String = "IMG.jpg") -> AssetResource {
        AssetResource(key: ResourceKey(localIdentifier: id, kind: kind),
                      filename: name, creationDate: date(date), bundleID: nil)
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
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter SyncPlannerTests`
Expected: FAIL — `cannot find 'SyncPlanner' in scope` / `cannot find type 'SyncPlan'`.

- [x] **Step 3: Write minimal implementation**

`SyncPlan.swift`:

```swift
import Foundation

public struct SyncPlan: Sendable, Equatable {
    public let uploads: [PlannedUpload]
    public let deletes: [PlannedDelete]
    public let skipped: Int

    public init(uploads: [PlannedUpload], deletes: [PlannedDelete], skipped: Int) {
        self.uploads = uploads
        self.deletes = deletes
        self.skipped = skipped
    }
}

public struct PlannedUpload: Sendable, Equatable {
    public enum Mode: Sendable, Equatable {
        case create                     // no server record for this key
        case resume(uploadID: String)   // server status=uploading → resume from HEAD offset
        case recover(uploadID: String)  // server status=backend_lost → delete→create→upload from 0
    }
    public let resource: AssetResource
    public let mode: Mode

    public init(resource: AssetResource, mode: Mode) {
        self.resource = resource
        self.mode = mode
    }
}

public struct PlannedDelete: Sendable, Equatable {
    public let uploadID: String
    public let key: ResourceKey

    public init(uploadID: String, key: ResourceKey) {
        self.uploadID = uploadID
        self.key = key
    }
}
```

`SyncPlanner.swift`:

```swift
import Foundation

/// Pure diff: (library resources, server records, range) -> plan. No I/O, no fakes.
public enum SyncPlanner {
    public static func plan(library: [AssetResource],
                            server: [UploadRecord],
                            range: SyncRange) -> SyncPlan {
        let serverByKey = Dictionary(server.map { ($0.localIdentifier, $0) },
                                     uniquingKeysWith: { first, _ in first })
        let libraryKeys = Set(library.map { $0.key.encoded })

        // Upload side — iterate library resources (ordered oldest-first).
        var uploads: [PlannedUpload] = []
        var skipped = 0
        for res in library.sorted(by: order) {
            guard let rec = serverByKey[res.key.encoded] else {
                uploads.append(PlannedUpload(resource: res, mode: .create)); continue
            }
            switch rec.status {
            case .uploading:  uploads.append(PlannedUpload(resource: res, mode: .resume(uploadID: rec.id)))
            case .backendLost: uploads.append(PlannedUpload(resource: res, mode: .recover(uploadID: rec.id)))
            case .complete, .completing, .deleted: skipped += 1
            }
        }

        // Delete side — server records absent from the library.
        var deletes: [PlannedDelete] = []
        for rec in server where !libraryKeys.contains(rec.localIdentifier) {
            switch rec.status {
            case .deleted, .completing:
                continue // already gone / mid-move — leave alone
            case .uploading, .complete, .backendLost:
                if case .dates(let window) = range {
                    guard let d = parseDate(rec.creationDate), window.contains(d) else { continue }
                }
                if let key = try? ResourceKey(parsing: rec.localIdentifier) {
                    deletes.append(PlannedDelete(uploadID: rec.id, key: key))
                }
            }
        }

        return SyncPlan(uploads: uploads, deletes: deletes, skipped: skipped)
    }

    static func order(_ a: AssetResource, _ b: AssetResource) -> Bool {
        if a.creationDate != b.creationDate { return a.creationDate < b.creationDate }
        return a.key.encoded < b.key.encoded
    }

    static func parseDate(_ s: String?) -> Date? {
        guard let s else { return nil }
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = withFraction.date(from: s) { return d }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: s)
    }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter SyncPlannerTests`
Expected: PASS (all cases, including the parameterized ones).

- [x] **Step 5: Verify a test can fail (spec §7 discipline)**

Temporarily change `.deleted, .completing:` `continue` in `SyncPlanner` to only `.deleted:` and rerun `datesRangeDoesNotDeleteRecordsOutsideWindow` + `deletedOrCompletingAbsentRecordsAreLeftAlone`.
Expected: `deletedOrCompletingAbsentRecordsAreLeftAlone` FAILS for `.completing`. Revert the change; rerun; PASS.

- [x] **Step 6: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Sources/FilesNestCore/SyncPlan.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncPlanner.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncPlannerTests.swift
git commit -m "Apple clients: SyncPlanner pure diff with range-scoped deletes"
```

---

### Task 3: SyncStateStore + UserDefaults / in-memory impls

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncStateStore.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncStateStoreTests.swift`

**Interfaces:**
- Produces: `protocol SyncStateStore { func loadLastSyncStarted() -> Date?; func saveLastSyncStarted(_:Date) }`, `UserDefaultsSyncStateStore(defaults:UserDefaults)`, and test support `InMemorySyncStateStore()`.

- [x] **Step 1: Write the failing test**

Create `SyncStateStoreTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct SyncStateStoreTests {
    @Test func userDefaultsRoundTripsDate() {
        let suite = UserDefaults(suiteName: "scc.state.\(UUID().uuidString)")!
        let store = UserDefaultsSyncStateStore(defaults: suite)
        #expect(store.loadLastSyncStarted() == nil)

        let d = Date(timeIntervalSince1970: 1_700_000_000) // whole second — ISO8601 has no sub-second here
        store.saveLastSyncStarted(d)
        #expect(store.loadLastSyncStarted() == d)
    }

    @Test func inMemoryRoundTrips() {
        let store = InMemorySyncStateStore()
        #expect(store.loadLastSyncStarted() == nil)
        let d = Date(timeIntervalSince1970: 42)
        store.saveLastSyncStarted(d)
        #expect(store.loadLastSyncStarted() == d)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter SyncStateStoreTests`
Expected: FAIL — `cannot find 'UserDefaultsSyncStateStore'` / `cannot find 'InMemorySyncStateStore'`.

- [x] **Step 3: Write minimal implementation**

`Sources/FilesNestCore/SyncStateStore.swift`:

```swift
import Foundation

/// The only durable client state. `lastSyncStarted` supports incremental-range
/// selection and a "last synced" UI label; crash-resume is emergent from
/// re-diffing (spec §3, decision 3), so no queue position is stored.
public protocol SyncStateStore: Sendable {
    func loadLastSyncStarted() -> Date?
    func saveLastSyncStarted(_ date: Date)
}

/// App-side implementation. Inject a dedicated `UserDefaults(suiteName:)` in
/// tests so it never touches `.standard`. Stored as an ISO-8601 string.
public final class UserDefaultsSyncStateStore: SyncStateStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.sync.lastSyncStarted"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func loadLastSyncStarted() -> Date? {
        guard let s = defaults.string(forKey: key) else { return nil }
        return ISO8601DateFormatter().date(from: s)
    }

    public func saveLastSyncStarted(_ date: Date) {
        defaults.set(ISO8601DateFormatter().string(from: date), forKey: key)
    }
}
```

`Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift`:

```swift
import Foundation
@testable import FilesNestCore

/// Deterministic test double. `final class` behind a lock (Sendable; mutated
/// from the coordinator). Exact `Date` is preserved, unlike the ISO-8601 store.
final class InMemorySyncStateStore: SyncStateStore, @unchecked Sendable {
    private let lock = NSLock()
    private var value: Date?

    func loadLastSyncStarted() -> Date? { lock.lock(); defer { lock.unlock() }; return value }
    func saveLastSyncStarted(_ date: Date) { lock.lock(); defer { lock.unlock() }; value = date }
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter SyncStateStoreTests`
Expected: PASS (2 tests).

- [x] **Step 5: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Sources/FilesNestCore/SyncStateStore.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/InMemorySyncStateStore.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncStateStoreTests.swift
git commit -m "Apple clients: SyncStateStore (protocol + UserDefaults + in-memory)"
```

---

### Task 4: FakeServer test harness + FakeAssetLibrary

This task builds the stateful in-memory server that later coordinator tests drive through the **real** `ServerClient`/`AssetUploader`, plus a trivial `FakeAssetLibrary`. Its deliverable is a self-test proving the harness speaks the wire protocol correctly.

**Files:**
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeServer.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeAssetLibrary.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/FakeServerTests.swift`

**Interfaces:**
- Consumes: `MockURLProtocol`, `FakeCredentialStore`, `URLRequest.httpBodyByteCount()` (all existing in the test target); `ServerClient`, `UploadRecord`, `AssetResource`, `AssetLibrary`.
- Produces:
  - `FakeServer(host:String)` with: `seed(localIdentifier:status:offset:creationDate:filename:bundleID:) -> String` (returns id), `client() -> ServerClient`, `record(id:) -> Record?`, `all() -> [Record]`, `events: [String]`, `var pageSize: Int`, `var backendLostIDs: Set<String>`, `var markLostOnFirstDataOp: Bool`, `var failDeleteIDs: Set<String>`. (Behaviour is stored config — no subclasses.)
  - `FakeServer.Record` with `id, localIdentifier, status, offset` (at least).
  - `FakeAssetLibrary(items:[AssetResource], error:(any Error)?)`.
  - `URLRequest.httpBodyData()` helper.

- [x] **Step 1: Write the failing self-test**

Create `FakeServerTests.swift`:

```swift
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
```

- [x] **Step 2: Run the self-test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter FakeServerTests`
Expected: FAIL — `cannot find 'FakeServer'` / `cannot find 'FakeAssetLibrary'`.

- [x] **Step 3: Write the harness**

`Tests/FilesNestCoreTests/Support/FakeAssetLibrary.swift`:

```swift
import Foundation
@testable import FilesNestCore

struct FakeAssetLibrary: AssetLibrary {
    var items: [AssetResource] = []
    var error: (any Error)? = nil

    func resources(in range: SyncRange) async throws -> [AssetResource] {
        if let error { throw error }
        return items
    }
}
```

`Tests/FilesNestCoreTests/Support/FakeServer.swift`:

```swift
import Foundation
@testable import FilesNestCore

/// Stateful in-memory stand-in for the Go server, driven through MockURLProtocol
/// so tests exercise the REAL ServerClient + AssetUploader end to end. Records are
/// keyed by an opaque `id-N` (the client treats ids as opaque routing tokens).
final class FakeServer: @unchecked Sendable {
    struct Record {
        var id: String
        var localIdentifier: String   // == ResourceKey.encoded
        var status: String            // uploading | completing | complete | deleted | backend_lost
        var backendID: String
        var filename: String?
        var bundleID: String?
        var creationDate: String?     // ISO-8601
        var offset: Int64
        var length: Int64?
    }

    let host: String
    private let lock = NSLock()
    private var records: [String: Record] = [:]
    private var nextID = 0
    private var _events: [String] = []

    /// Page size for GET /uploads. Default: everything in one page.
    var pageSize = Int.max
    /// Ids whose data ops (HEAD/PATCH/status) return 409 backend_lost. A DELETE
    /// clears the flag, so a re-created record (fresh id) uploads cleanly.
    var backendLostIDs: Set<String> = []
    /// If true, the first data op on any not-yet-flagged id flags it lost first —
    /// models a backend that lost the file the instant its record was created, so
    /// even a recovery upload (fresh id) fails.
    var markLostOnFirstDataOp = false
    /// Ids whose DELETE returns 500, to exercise "record the failure, keep deleting".
    var failDeleteIDs: Set<String> = []

    init(host: String) { self.host = host }

    /// Ordered log of "METHOD path" for ordering assertions (e.g. deletes after uploads).
    var events: [String] { lock.lock(); defer { lock.unlock() }; return _events }
    func record(id: String) -> Record? { lock.lock(); defer { lock.unlock() }; return records[id] }
    func all() -> [Record] { lock.lock(); defer { lock.unlock() }; return Array(records.values) }

    @discardableResult
    func seed(localIdentifier: String, status: String, offset: Int64 = 0,
              creationDate: String? = "2024-06-15T10:00:00Z", filename: String? = "IMG.jpg",
              bundleID: String? = nil) -> String {
        lock.lock(); defer { lock.unlock() }
        let id = "id-\(nextID)"; nextID += 1
        records[id] = Record(id: id, localIdentifier: localIdentifier, status: status,
                             backendID: "b-\(id)", filename: filename, bundleID: bundleID,
                             creationDate: creationDate, offset: offset, length: nil)
        return id
    }

    func client() -> ServerClient {
        MockURLProtocol.setHandler(forHost: host) { [weak self] req in
            try self!.handle(req)
        }
        return ServerClient(baseURL: URL(string: "https://\(host)")!,
                            credentials: FakeCredentialStore(creds: nil),
                            session: MockURLProtocol.makeSession())
    }

    // MARK: routing (runs on URLSession's worker thread; lock guards state)
    private func handle(_ req: URLRequest) throws -> (HTTPURLResponse, Data) {
        let url = req.url!
        let method = req.httpMethod ?? "GET"
        let parts = url.path.split(separator: "/").map(String.init) // uploads / <id> / data|status
        lock.lock(); defer { lock.unlock() }
        _events.append("\(method) \(url.path)")

        func resp(_ status: Int, _ headers: [String: String] = [:], _ body: Data = Data())
            -> (HTTPURLResponse, Data) {
            (HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                             headerFields: headers)!, body)
        }
        func lost() -> (HTTPURLResponse, Data) {
            resp(409, [:], #"{"error":"backend_lost"}"#.data(using: .utf8)!)
        }
        func json(_ r: Record) -> [String: Any] {
            var o: [String: Any] = ["id": r.id, "local_identifier": r.localIdentifier,
                                    "status": r.status, "backend_id": r.backendID]
            if let f = r.filename { o["filename"] = f }
            if let b = r.bundleID { o["bundle_id"] = b }
            if let c = r.creationDate { o["creation_date"] = c }
            return o
        }

        switch (method, parts.count) {
        case ("POST", 1) where parts[0] == "uploads":
            let body = (try? JSONSerialization.jsonObject(with: req.httpBodyData())) as? [String: Any] ?? [:]
            let loc = body["local_identifier"] as? String ?? ""
            let id = "id-\(nextID)"; nextID += 1
            let rec = Record(id: id, localIdentifier: loc, status: "uploading", backendID: "b-\(id)",
                             filename: body["filename"] as? String, bundleID: body["bundle_id"] as? String,
                             creationDate: body["creation_date"] as? String, offset: 0, length: nil)
            records[id] = rec
            return resp(200, [:], try JSONSerialization.data(withJSONObject: json(rec)))

        case ("GET", 1) where parts[0] == "uploads":
            let sorted = records.values.sorted { ($0.creationDate ?? "", $0.id) < ($1.creationDate ?? "", $1.id) }
            let cursorValue = URLComponents(url: url, resolvingAgainstBaseURL: false)?
                .queryItems?.first { $0.name == "cursor" }?.value
            let start = decodeCursor(cursorValue)
            let end = min(start + pageSize, sorted.count)
            let clampedStart = min(start, sorted.count)
            let slice = sorted[clampedStart..<end]
            let next = end < sorted.count ? encodeCursor(end) : ""
            let obj: [String: Any] = ["items": slice.map(json), "next_cursor": next]
            return resp(200, [:], try JSONSerialization.data(withJSONObject: obj))

        case ("HEAD", 3) where parts[0] == "uploads" && parts[2] == "data":
            let id = parts[1]
            if markLostOnFirstDataOp && !backendLostIDs.contains(id) { backendLostIDs.insert(id) }
            if backendLostIDs.contains(id) { return lost() }
            guard let r = records[id] else { return resp(404) }
            return resp(200, ["Upload-Offset": String(r.offset)])

        case ("PATCH", 3) where parts[0] == "uploads" && parts[2] == "data":
            let id = parts[1]
            if markLostOnFirstDataOp && !backendLostIDs.contains(id) { backendLostIDs.insert(id) }
            if backendLostIDs.contains(id) { return lost() }
            guard var r = records[id] else { return resp(404) }
            let off = Int64(req.value(forHTTPHeaderField: "Upload-Offset") ?? "0") ?? 0
            r.offset = off + req.httpBodyByteCount()
            if let fl = req.value(forHTTPHeaderField: "Upload-Length").flatMap(Int64.init) { r.length = fl }
            records[id] = r
            return resp(204, ["Upload-Offset": String(r.offset)])

        case ("PATCH", 3) where parts[0] == "uploads" && parts[2] == "status":
            let id = parts[1]
            if markLostOnFirstDataOp && !backendLostIDs.contains(id) { backendLostIDs.insert(id) }
            if backendLostIDs.contains(id) { return lost() }
            guard var r = records[id] else { return resp(404) }
            r.status = "complete"; records[id] = r
            return resp(200)

        case ("DELETE", 2) where parts[0] == "uploads":
            let id = parts[1]
            if failDeleteIDs.contains(id) { return resp(500) }
            records[id]?.status = "deleted"
            backendLostIDs.remove(id)
            return resp(204)

        default:
            return resp(500)
        }
    }

    private func encodeCursor(_ index: Int) -> String {
        Data(String(index).utf8).base64EncodedString()
    }
    private func decodeCursor(_ value: String?) -> Int {
        guard let value, let data = Data(base64Encoded: value),
              let s = String(data: data, encoding: .utf8), let i = Int(s) else { return 0 }
        return i
    }
}

extension URLRequest {
    /// Reads the full body (whether set as `httpBody` or streamed by URLSession).
    /// Distinct from `httpBodyByteCount()` (AssetUploaderTests.swift), which only counts.
    func httpBodyData() -> Data {
        if let body = httpBody { return body }
        guard let stream = httpBodyStream else { return Data() }
        stream.open(); defer { stream.close() }
        var data = Data()
        let size = 64 * 1024
        let buf = UnsafeMutablePointer<UInt8>.allocate(capacity: size)
        defer { buf.deallocate() }
        while stream.hasBytesAvailable {
            let n = stream.read(buf, maxLength: size)
            if n <= 0 { break }
            data.append(buf, count: n)
        }
        return data
    }
}
```

- [x] **Step 4: Run the self-test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter FakeServerTests`
Expected: PASS (5 tests).

- [x] **Step 5: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeServer.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeAssetLibrary.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/FakeServerTests.swift
git commit -m "Apple clients: FakeServer test harness + FakeAssetLibrary"
```

---

### Task 5: SyncReport + SyncCoordinator happy paths

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`

**Interfaces:**
- Consumes: `ServerClient`, `AssetUploader`, `AssetLibrary`, `SyncStateStore`, `SyncPlanner`, `CreateUploadRequest`, `UploadRecord`; test support `FakeServer`, `FakeAssetLibrary`, `InMemorySyncStateStore`, `FakeAssetDataSource`.
- Produces:
  - `SyncReport(uploaded:[ResourceKey], deleted:[ResourceKey], failed:[FailedItem], skipped:Int)`, `FailedItem(key:ResourceKey, reason:String)`.
  - `SyncCoordinator(client:library:uploader:state:now:)` with `func sync(range:SyncRange) async throws -> SyncReport`.

- [x] **Step 1: Write the failing tests**

Create `SyncCoordinatorTests.swift`:

```swift
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
        // Exactly one record, now complete, fully uploaded.
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
        // Resumed on the SAME record (no new create): still one record, now complete.
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
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter SyncCoordinatorTests`
Expected: FAIL — `cannot find 'SyncCoordinator'` / `cannot find 'SyncReport'`.

- [x] **Step 3: Write minimal implementation**

`Sources/FilesNestCore/SyncReport.swift`:

```swift
import Foundation

public struct SyncReport: Sendable, Equatable {
    public let uploaded: [ResourceKey]
    public let deleted: [ResourceKey]
    public let failed: [FailedItem]
    public let skipped: Int

    public init(uploaded: [ResourceKey], deleted: [ResourceKey], failed: [FailedItem], skipped: Int) {
        self.uploaded = uploaded
        self.deleted = deleted
        self.failed = failed
        self.skipped = skipped
    }
}

public struct FailedItem: Sendable, Equatable {
    public let key: ResourceKey
    public let reason: String   // human-readable; the UI slice renders these as a list

    public init(key: ResourceKey, reason: String) {
        self.key = key
        self.reason = reason
    }
}
```

`Sources/FilesNestCore/SyncCoordinator.swift`:

```swift
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

    public func sync(range: SyncRange) async throws -> SyncReport {
        state.saveLastSyncStarted(now())

        let libraryResources = try await library.resources(in: range)
        let serverRecords = try await pagedServerRecords()
        let plan = SyncPlanner.plan(library: libraryResources, server: serverRecords, range: range)

        var uploaded: [ResourceKey] = []
        var deleted: [ResourceKey] = []
        var failed: [FailedItem] = []

        for item in plan.uploads {
            try Task.checkCancellation()
            do {
                try await execute(item)
                uploaded.append(item.resource.key)
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                failed.append(FailedItem(key: item.resource.key, reason: String(describing: error)))
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
                failed.append(FailedItem(key: del.key, reason: String(describing: error)))
            }
        }

        return SyncReport(uploaded: uploaded, deleted: deleted, failed: failed, skipped: plan.skipped)
    }

    private func pagedServerRecords() async throws -> [UploadRecord] {
        var records: [UploadRecord] = []
        var cursor: String? = nil
        repeat {
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
        case .recover(let uploadID):
            try await recover(assetKey: assetKey, uploadID: uploadID, resource: item.resource)
        }
    }

    /// Upload, recovering ONCE from a mid-flight backend_lost (spec §6 step 5).
    private func uploadWithRecovery(assetKey: String, uploadID: String,
                                    resource: AssetResource) async throws {
        do {
            try await uploader.upload(assetID: assetKey, uploadID: uploadID)
        } catch ServerClientError.backendLost {
            try await recover(assetKey: assetKey, uploadID: uploadID, resource: resource)
        }
    }

    /// delete lost record → re-register → upload from 0. No further recovery.
    private func recover(assetKey: String, uploadID: String, resource: AssetResource) async throws {
        try await client.deleteUpload(id: uploadID)
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
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter SyncCoordinatorTests`
Expected: PASS (6 tests).

- [x] **Step 5: Verify the ordering test can fail**

Temporarily move the delete loop **above** the upload loop in `sync(range:)` and rerun `absentServerRecordIsDeletedAfterUploads`.
Expected: FAIL on the `deleteIdx > lastUploadIdx` assertion. Revert; rerun; PASS.

- [x] **Step 6: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift \
        apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "Apple clients: SyncCoordinator core (create/resume/skip/delete + report + state)"
```

---

### Task 6: backend_lost recovery (proactive + reactive)

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift` (add tests)
- (No source change expected — the recovery logic shipped in Task 5. These tests prove it. If a test legitimately fails, fix `SyncCoordinator` minimally.)

**Interfaces:**
- Consumes: everything from Task 5 plus `FakeServer.backendLostIDs`, `FakeServer.seed`.

- [x] **Step 1: Write the failing tests**

Append to `SyncCoordinatorTests.swift`:

```swift
extension SyncCoordinatorTests {
    // Proactive: planner sees a backend_lost record → recover before uploading.
    @Test func backendLostRecordIsRecoveredProactively() async throws {
        let server = FakeServer(host: "sc-recover-proactive.test")
        let lostID = server.seed(localIdentifier: "A#photo", status: "backend_lost")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(report.failed.isEmpty)
        // Original lost record deleted; a fresh record carries the completed upload.
        #expect(server.record(id: lostID)?.status == "deleted")
        let completed = server.all().filter { $0.status == "complete" }
        #expect(completed.count == 1)
        #expect(completed[0].id != lostID)
        #expect(completed[0].offset == 250)
    }

    // Reactive: planner sees `uploading`, but the server has since lost the backend
    // (HEAD returns 409). Coordinator recovers mid-flight.
    @Test func backendLostDuringResumeIsRecoveredReactively() async throws {
        let server = FakeServer(host: "sc-recover-reactive.test")
        let staleID = server.seed(localIdentifier: "A#photo", status: "uploading", offset: 100)
        server.backendLostIDs = [staleID]   // HEAD/PATCH on this id → 409 backend_lost
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded == [ResourceKey(localIdentifier: "A", kind: .photo)])
        #expect(report.failed.isEmpty)
        #expect(server.record(id: staleID)?.status == "deleted")
        #expect(server.all().contains { $0.status == "complete" && $0.id != staleID })
    }

    // Recovery that fails again → the item lands in `failed`, sync continues.
    @Test func recoveryThatFailsAgainRecordsFailure() async throws {
        // markLostOnFirstDataOp: even the fresh record created during recovery is
        // lost on its first data op, so the single recovery attempt fails too.
        let server = FakeServer(host: "sc-recover-fail.test")
        server.markLostOnFirstDataOp = true
        server.seed(localIdentifier: "A#photo", status: "backend_lost")
        let report = try await makeCoordinator(server: server, library: [resource("A")]).sync(range: .all)

        #expect(report.uploaded.isEmpty)
        #expect(report.failed.map(\.key) == [ResourceKey(localIdentifier: "A", kind: .photo)])
    }
}
```

- [x] **Step 2: Run tests to verify they fail (then pass)**

Run: `cd apple/FilesNestCore && swift test --filter SyncCoordinatorTests`
Expected: first FAIL — the three new tests do not yet exist as symbols? No: they compile (all machinery shipped in Tasks 4–5). Expected PASS for all three. If `backendLostRecordIsRecoveredProactively` or `backendLostDuringResumeIsRecoveredReactively` fails, the recovery bug is in `SyncCoordinator.uploadWithRecovery`/`recover` — fix minimally. `recoveryThatFailsAgainRecordsFailure` proves the "recover once, then give up" boundary.

- [x] **Step 3: Verify a recovery test can fail**

Temporarily change `uploadWithRecovery`'s `catch ServerClientError.backendLost` to rethrow (`catch ServerClientError.backendLost { throw ServerClientError.backendLost }`) and rerun `backendLostDuringResumeIsRecoveredReactively`.
Expected: FAIL — item goes to `failed`, `uploaded` empty. Revert; rerun; PASS.

- [x] **Step 4: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "Apple clients: SyncCoordinator backend_lost recovery (proactive + reactive) tests"
```

---

### Task 7: Failure policy — skip-and-continue + cancellation

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift` (add tests)
- (No source change expected — skip-and-continue and cancellation shipped in Task 5. Prove them; fix minimally only if a test legitimately fails.)

**Interfaces:**
- Consumes: Task 5/6 machinery; `FakeAssetDataSource(totalBytes:blobSize:failAfterBlobs:)` (existing — see AssetUploaderTests `propagatesSourceErrors`).

- [x] **Step 1: Write the failing tests**

Append to `SyncCoordinatorTests.swift`:

```swift
extension SyncCoordinatorTests {
    // One item's source read fails; later items still upload; report is accurate.
    @Test func failedItemIsRecordedAndSyncContinues() async throws {
        let server = FakeServer(host: "sc-continue.test")
        let client = server.client()
        // A source that throws after 1 blob → every upload fails the same way.
        // To make ONLY the first item fail, use a source good enough for the rest
        // by giving a large blob budget but scripting failure per-call is not
        // possible with the shared source; instead assert BOTH items fail here and
        // that the sync still processed both (no early abort) and produced a report.
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
```

- [x] **Step 2: Run tests to verify they pass**

Run: `perl -e 'alarm 120; exec @ARGV' swift test --filter SyncCoordinatorTests` (run from `apple/FilesNestCore`; the `perl` guard covers the cancellation test).
Expected: all PASS (the machinery — `failDeleteIDs`, `FakeAssetDataSource(failAfterBlobs:)`, cancellation handling — shipped in Tasks 4–5). If `cancellationStopsAndThrows` fails or hangs, the cancellation catch is wrong — fix `SyncCoordinator` minimally.

- [x] **Step 3: Verify the cancellation test can fail**

Temporarily replace the sync loop's `catch is CancellationError { throw CancellationError() }` (upload loop) with the general `catch` (i.e. delete the `CancellationError` catch) and rerun `cancellationStopsAndThrows`.
Expected: the test FAILS or hangs (cancellation gets recorded as a failed item instead of thrown) — the `perl` alarm bounds any hang. Revert; rerun; PASS.

- [x] **Step 4: Commit**

```bash
cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
git add apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "Apple clients: SyncCoordinator failure policy (skip-and-continue + cancellation) tests"
```

---

### Task 8: Range-scoped delete integration + full-suite verification + docs

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift` (one integration test)
- Modify: `docs/architecture.md` (correct the "position" wording per spec §3/§5)

**Interfaces:**
- Consumes: all prior tasks.

- [x] **Step 1: Write the failing integration test**

Append to `SyncCoordinatorTests.swift`:

```swift
extension SyncCoordinatorTests {
    // End-to-end proof of spec §5.3 through the coordinator: a January sync must
    // NOT delete a February backup even though listUploads returns everything.
    @Test func januarySyncDoesNotDeleteFebruaryBackup() async throws {
        let server = FakeServer(host: "sc-range.test")
        let febID = server.seed(localIdentifier: "FEB#photo", status: "complete",
                                creationDate: "2024-02-10T12:00:00Z")
        let jan = date("2024-01-01T00:00:00Z")...date("2024-01-31T23:59:59Z")
        // Library (January) contains one new asset.
        let report = try await makeCoordinator(
            server: server,
            library: [resource("JAN", date: "2024-01-15T09:00:00Z")]).sync(range: .dates(jan))

        #expect(report.uploaded == [ResourceKey(localIdentifier: "JAN", kind: .photo)])
        #expect(report.deleted.isEmpty)
        #expect(server.record(id: febID)?.status == "complete") // untouched
    }
}
```

- [x] **Step 2: Run to verify it passes (planner logic already implements it)**

Run: `cd apple/FilesNestCore && swift test --filter januarySyncDoesNotDeleteFebruaryBackup`
Expected: PASS. (If it fails, the bug is in `SyncPlanner` range scoping — fix there.)

- [x] **Step 3: Verify it can fail**

Temporarily change the coordinator call to pass `range: .all` instead of `.dates(jan)` and rerun.
Expected: FAIL — February record is now deleted (`report.deleted` non-empty). Revert; rerun; PASS.

- [x] **Step 4: Correct `architecture.md`**

In `docs/architecture.md`, "Mac app: sync logic" step 8, replace the sentence:

> 8. Sync state (`lastSyncStarted`, current position) persisted to `UserDefaults` so a crash-restart resumes from the first incomplete item, not from scratch.

with:

> 8. Only `lastSyncStarted` is persisted to `UserDefaults` (via `SyncStateStore`). No queue position is stored: because the server is the single source of truth, a crash-restart re-runs the diff — completed items already read `complete` on the server and are skipped, and `uploading` items resume from the HEAD offset — so resume is emergent, not stored. See `docs/design/20260726-synccoordinator.md` §3 (decision 3).

Use Serena `replace_content` for this edit (it is a docs file, but stay consistent with tooling).

- [x] **Step 5: Full suite + build gate**

Run:
```bash
cd apple/FilesNestCore
perl -e 'alarm 300; exec @ARGV' swift test
swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/scc-build
```
Expected: ALL tests pass (the 93 pre-existing + the new SyncCoordinator/Planner/State/FakeServer/value-type tests); build clean with warnings-as-errors.

- [x] **Step 6: Server unaffected (sanity)**

Run:
```bash
cd server && go test ./... && go vet ./...
```
Expected: PASS — this slice touches no Go code; this only confirms the working tree is clean.

- [x] **Step 7: Commit**

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift docs/architecture.md
git commit -m "Apple clients: SyncCoordinator range-scoping integration test + architecture.md correction"
```

---

## Post-plan (gated on the repo owner)

Per the working conventions, after all tasks pass: **Codex review at slice completion** (not per task), then open the PR. Do not push or open the PR without an explicit ask. The design doc (`docs/design/20260726-synccoordinator.md`) is still uncommitted — commit it alongside the first task or when the owner asks.

## Self-Review (completed by the plan author)

**Spec coverage:** §2 types → Tasks 1–5; §5 planner incl. range scoping & nil-date & Live Photo & ordering → Task 2 (+ Task 8 integration); §6 executor incl. create/resume/recover, markComplete-owned-by-uploader, backendLost recovery, skip-and-continue, cancellation, delete-after-upload, state persistence → Tasks 5–7; §7.1 client-faking via MockURLProtocol → Task 4 (`FakeServer`), resolving the open risk toward the primary option; §7.2/§7.3/§7.4 test matrix → Tasks 2/5/6/7/3; §3 decision 3 doc correction → Task 8. All covered.

**Placeholder scan:** no TBD/TODO; every code and test step shows complete code; every run step gives an exact command + expected result.

**Type consistency:** `SyncPlanner.plan(library:server:range:)`, `PlannedUpload.Mode.{create,resume(uploadID:),recover(uploadID:)}`, `PlannedDelete(uploadID:key:)`, `SyncReport(uploaded:deleted:failed:skipped:)`, `FailedItem(key:reason:)`, `SyncCoordinator(client:library:uploader:state:now:)`, `FakeServer.seed(...)->String` / `.client()` / `.record(id:)` / `.events` / `.backendLostIDs` / `.pageSize`, `FakeAssetLibrary(items:error:)`, `InMemorySyncStateStore()`, `UserDefaultsSyncStateStore(defaults:)` — used consistently across tasks. `AssetUploader.upload(assetID:uploadID:)` and `ServerClient` method names match the verified existing sources.

**Open risk carried into execution:** the `FakeServer` harness (Task 4) is the load-bearing test seam resolving spec §7.1. Its fault-injection is stored config (`markLostOnFirstDataOp`, `failDeleteIDs`, `backendLostIDs`) — no subclasses, no `Package.swift` change. If driving the ordered recovery sequence through `MockURLProtocol` proves illegible in practice, the spec's documented fallback (a minimal internal client protocol) applies — but the harness is designed to avoid needing it.
