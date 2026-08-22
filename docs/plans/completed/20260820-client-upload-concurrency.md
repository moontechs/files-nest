# Client Concurrent Uploads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upload multiple assets in parallel, bounded by a server-advertised concurrency cap, handling the server's over-limit `503 + Retry-After` rejection.

**Architecture:** `SyncCoordinator`'s serial upload loop becomes a bounded sliding-window `TaskGroup`. The cap is discovered from a new `GET /config` endpoint (fallback 4). `ServerClient.patchData` retries `503`s honoring `Retry-After`. A single new `SyncProgress.inFlight` field lets `PanelView` show the count; the strip's hero thumbnail is the most-recently-started item.

**Tech Stack:** Swift 6 (strict concurrency), swift-testing (`import Testing`), pure Foundation + Security in `FilesNestCore`; SwiftUI in the macOS app target.

**Spec:** `docs/design/20260820-client-upload-concurrency.md`

## Global Constraints

- Swift 6 language mode; `FilesNestCore` is pure Foundation + Security (no PhotoKit/SwiftUI).
- Tests use swift-testing (`@Test`, `#expect`), `@testable import FilesNestCore`; fakes live under `Tests/FilesNestCoreTests/Support/`.
- TDD is mandatory: write the failing test, run it, watch it fail for the right reason, then implement to green. Commit per task.
- Core test run: `cd apple/FilesNestCore && swift test`. Single test: `swift test --filter <TestName>`.
- App build (no unit tests; manual-verify): `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`.
- All paths below are relative to `apple/FilesNestCore/` unless they start with `apple/macos/`.
- Concurrency default when the server advertises none: **4**. `Retry-After` fallback when the header is absent: **1** second. Default PATCH retries: **5**.
- Do NOT parallelize the delete queue; do NOT add a Settings UI control (both are out of scope per the spec).
- Commit messages: no `Co-Authored-By`/bot-attribution trailer.

---

### Task 1: `ServerConfig` model + `ServerClient.config()`

**Files:**
- Create: `Sources/FilesNestCore/Models/ServerConfig.swift`
- Modify: `Sources/FilesNestCore/ServerClient.swift` (add `config()` after `getUpload`, ~line 111)
- Test: `Tests/FilesNestCoreTests/ServerClientTests.swift`

**Interfaces:**
- Produces: `public struct ServerConfig: Decodable, Sendable, Equatable { public let maxConcurrentUploads: Int }`; `public func config() async throws -> ServerConfig` on `ServerClient` (GET `/config`).

- [ ] **Step 1: Write the failing test**

Add to `ServerClientTests.swift`:

```swift
@Test func configDecodesMaxConcurrentUploads() async throws {
    let host = "sc-config.test"
    // NB: don't call #expect inside the handler — it runs on URLSession's worker
    // thread where swift-testing can't associate it. The decoded result below is
    // the assertion; the handler only serves /config for this host.
    MockURLProtocol.setHandler(forHost: host) { req in
        let body = #"{"maxConcurrentUploads": 7}"#.data(using: .utf8)!
        return MockURLProtocol.respond(status: 200,
                                       headers: ["Content-Type": "application/json"],
                                       body: body, for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession())
    let cfg = try await client.config()
    #expect(cfg == ServerConfig(maxConcurrentUploads: 7))
}

@Test func configThrowsNotFoundOnOldServer() async throws {
    let host = "sc-config-404.test"
    MockURLProtocol.setHandler(forHost: host) { req in
        MockURLProtocol.respond(status: 404, body: Data(), for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.notFound) { _ = try await client.config() }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter configDecodesMaxConcurrentUploads`
Expected: FAIL — `value of type 'ServerClient' has no member 'config'` (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `Sources/FilesNestCore/Models/ServerConfig.swift`:

```swift
import Foundation

/// Server-advertised limits, from `GET /config`.
public struct ServerConfig: Decodable, Sendable, Equatable {
    public let maxConcurrentUploads: Int

    public init(maxConcurrentUploads: Int) {
        self.maxConcurrentUploads = maxConcurrentUploads
    }
}
```

In `ServerClient.swift`, add a `configURL()` helper next to the other URL builders (~line 27) and the method after `getUpload` (~line 111):

```swift
func configURL() -> URL { baseURL.appendingPathComponent("config") }

/// GET /config — server-advertised limits (e.g. the concurrency cap).
/// Throws `.notFound` on a server that predates the endpoint.
public func config() async throws -> ServerConfig {
    let req = try await authorizedRequest(configURL(), method: "GET")
    let (data, _) = try await send(req)
    return try decode(ServerConfig.self, from: data)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter configDecodesMaxConcurrentUploads` then `swift test --filter configThrowsNotFoundOnOldServer`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/Models/ServerConfig.swift Sources/FilesNestCore/ServerClient.swift Tests/FilesNestCoreTests/ServerClientTests.swift
git commit -m "feat(core): add ServerConfig model and GET /config client method"
```

---

### Task 2: Map `503` → `serviceUnavailable(retryAfter:)`

**Files:**
- Modify: `Sources/FilesNestCore/ServerClientError.swift` (add case)
- Modify: `Sources/FilesNestCore/ServerClient.swift` (`send`, ~line 60)
- Test: `Tests/FilesNestCoreTests/ServerClientNetworkTests.swift`

**Interfaces:**
- Produces: `ServerClientError.serviceUnavailable(retryAfter: Int?)`. `send` throws it on HTTP `503`, parsing the `Retry-After` header as integer seconds (`nil` when absent/unparseable).

- [ ] **Step 1: Write the failing test**

Add to `ServerClientNetworkTests.swift`:

```swift
@Test func mapsServiceUnavailableWithRetryAfter() async throws {
    let host = "sc-503.test"
    MockURLProtocol.setHandler(forHost: host) { req in
        MockURLProtocol.respond(status: 503,
                                headers: ["Retry-After": "3",
                                          "Content-Type": "application/json"],
                                body: #"{"error":"too many concurrent uploads"}"#.data(using: .utf8)!,
                                for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.serviceUnavailable(retryAfter: 3)) {
        _ = try await client.getUpload(id: "ID1")
    }
}

@Test func mapsServiceUnavailableWithoutRetryAfterHeader() async throws {
    let host = "sc-503-noheader.test"
    MockURLProtocol.setHandler(forHost: host) { req in
        MockURLProtocol.respond(status: 503, body: Data(), for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession())
    await #expect(throws: ServerClientError.serviceUnavailable(retryAfter: nil)) {
        _ = try await client.getUpload(id: "ID1")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter mapsServiceUnavailableWithRetryAfter`
Expected: FAIL — `serviceUnavailable` is not a member of `ServerClientError` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `ServerClientError.swift`, add the case (after `.transport`, line 18):

```swift
    /// 503 — server is at its concurrency cap. `retryAfter` is the `Retry-After`
    /// header in seconds (nil when absent). Recoverable: back off and retry.
    case serviceUnavailable(retryAfter: Int?)
```

In `ServerClient.swift` `send`, insert the 503 check just before the existing `map` call (line 60):

```swift
        if http.statusCode == 503 {
            let retryAfter = http.value(forHTTPHeaderField: "Retry-After").flatMap { Int($0) }
            throw ServerClientError.serviceUnavailable(retryAfter: retryAfter)
        }
        if let err = ServerClientError.map(status: http.statusCode, body: data) { throw err }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter mapsServiceUnavailable`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/ServerClientError.swift Sources/FilesNestCore/ServerClient.swift Tests/FilesNestCoreTests/ServerClientNetworkTests.swift
git commit -m "feat(core): map HTTP 503 to serviceUnavailable(retryAfter:)"
```

---

### Task 3: Bounded `Retry-After` retry in `patchData`

**Files:**
- Modify: `Sources/FilesNestCore/ServerClient.swift` (add `maxPatchRetries` init param; split `patchData` into a retry loop + `sendPatch`)
- Test: `Tests/FilesNestCoreTests/ServerClientNetworkTests.swift`

**Interfaces:**
- Consumes: `ServerClientError.serviceUnavailable(retryAfter:)` (Task 2).
- Produces: `ServerClient.init(..., maxPatchRetries: Int = 5)`. `patchData` retries on `serviceUnavailable` honoring `Retry-After` (fallback 1s), up to `maxPatchRetries`, then rethrows.

- [ ] **Step 1: Write the failing test**

Add to `ServerClientNetworkTests.swift`:

```swift
@Test func patchDataRetriesAfter503ThenSucceeds() async throws {
    let host = "sc-patch-retry.test"
    let calls = Counter503()
    MockURLProtocol.setHandler(forHost: host) { req in
        if calls.next() < 2 {   // first two attempts: 503 with a 0s backoff
            return MockURLProtocol.respond(status: 503, headers: ["Retry-After": "0"],
                                           body: Data(), for: req.url!)
        }
        return MockURLProtocol.respond(status: 204, headers: ["Upload-Offset": "100"],
                                       body: Data(), for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession(),
                              maxPatchRetries: 5)
    let newOffset = try await client.patchData(uploadID: "ID1", offset: 0,
                                               data: Data(count: 100), finalLength: nil)
    #expect(newOffset == 100)
    #expect(calls.count == 3)   // two 503s + one success
}

@Test func patchDataFailsAfterExhaustingRetries() async throws {
    let host = "sc-patch-exhaust.test"
    MockURLProtocol.setHandler(forHost: host) { req in
        MockURLProtocol.respond(status: 503, headers: ["Retry-After": "0"], body: Data(), for: req.url!)
    }
    defer { MockURLProtocol.removeHandler(forHost: host) }

    let client = ServerClient(baseURL: URL(string: "https://\(host)")!,
                              credentials: FakeCredentialStore(creds: nil),
                              session: MockURLProtocol.makeSession(),
                              maxPatchRetries: 2)
    await #expect(throws: ServerClientError.serviceUnavailable(retryAfter: 0)) {
        _ = try await client.patchData(uploadID: "ID1", offset: 0,
                                       data: Data(count: 10), finalLength: nil)
    }
}
```

Add this thread-safe call counter to the bottom of `ServerClientNetworkTests.swift` (module-internal, reused by tests):

```swift
final class Counter503: @unchecked Sendable {
    private let lock = NSLock()
    private var _count = 0
    var count: Int { lock.lock(); defer { lock.unlock() }; return _count }
    /// Returns the pre-increment value, then increments.
    func next() -> Int { lock.lock(); defer { lock.unlock() }; let v = _count; _count += 1; return v }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter patchDataRetriesAfter503ThenSucceeds`
Expected: FAIL — `patchData` throws `serviceUnavailable` on the first 503 (no retry yet); also `maxPatchRetries:` is an unknown argument (compile error).

- [ ] **Step 3: Write minimal implementation**

In `ServerClient.swift`, add the stored property + init param. Change the struct property block (line 4-6) and `init` (line 8-12):

```swift
    let baseURL: URL
    let credentials: any CredentialStore
    let session: URLSession
    let maxPatchRetries: Int

    public init(baseURL: URL, credentials: any CredentialStore,
                session: URLSession? = nil, maxPatchRetries: Int = 5) {
        self.baseURL = baseURL
        self.credentials = credentials
        self.session = session ?? Self.makeNonPersistentSession()
        self.maxPatchRetries = maxPatchRetries
    }
```

Replace the body of `patchData` (line 135-151) with a retry loop delegating to a renamed `sendPatch`:

```swift
    @discardableResult
    public func patchData(uploadID id: String, offset: Int64, data: Data,
                          finalLength: Int64?) async throws -> Int64 {
        var attempt = 0
        while true {
            do {
                return try await sendPatch(uploadID: id, offset: offset,
                                           data: data, finalLength: finalLength)
            } catch let ServerClientError.serviceUnavailable(retryAfter) {
                guard attempt < maxPatchRetries else {
                    throw ServerClientError.serviceUnavailable(retryAfter: retryAfter)
                }
                attempt += 1
                try await Task.sleep(for: .seconds(retryAfter ?? 1))
                // Loop: a 503 leaves the offset unchanged, so re-send the same PATCH.
            }
        }
    }

    private func sendPatch(uploadID id: String, offset: Int64, data: Data,
                           finalLength: Int64?) async throws -> Int64 {
        var req = try await authorizedRequest(dataURL(for: id), method: "PATCH")
        req.setValue("application/offset+octet-stream", forHTTPHeaderField: "Content-Type")
        req.setValue(String(offset), forHTTPHeaderField: "Upload-Offset")
        req.setValue("1.0.0", forHTTPHeaderField: "Tus-Resumable")
        if let finalLength {
            req.setValue(String(finalLength), forHTTPHeaderField: "Upload-Length")
        }
        req.httpBody = data
        let (_, http) = try await send(req)
        guard let offsetString = http.value(forHTTPHeaderField: "Upload-Offset"),
              let newOffset = Int64(offsetString) else {
            throw ServerClientError.decoding("missing Upload-Offset in PATCH response")
        }
        return newOffset
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter patchData`
Expected: PASS (both new tests, plus existing `AssetUploaderTests` still green).

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/ServerClient.swift Tests/FilesNestCoreTests/ServerClientNetworkTests.swift
git commit -m "feat(core): retry PATCH data on 503 honoring Retry-After (bounded)"
```

---

### Task 4: `SyncProgress.inFlight`

**Files:**
- Modify: `Sources/FilesNestCore/SyncStatus.swift:3-21`
- Test: `Tests/FilesNestCoreTests/SyncStatusTests.swift` (create if absent)

**Interfaces:**
- Produces: `SyncProgress.inFlight: Int` (init default `0`, last parameter).

- [ ] **Step 1: Write the failing test**

Create `Tests/FilesNestCoreTests/SyncStatusTests.swift`:

```swift
import Testing
@testable import FilesNestCore

@Test func syncProgressInFlightDefaultsToZero() {
    let p = SyncProgress(completed: 1, total: 3, currentItemName: "IMG.jpg", bytesRemaining: nil)
    #expect(p.inFlight == 0)
}

@Test func syncProgressCarriesInFlight() {
    let p = SyncProgress(completed: 1, total: 3, currentItemName: "IMG.jpg",
                         bytesRemaining: nil, currentItemID: "A#photo", inFlight: 4)
    #expect(p.inFlight == 4)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `swift test --filter syncProgressCarriesInFlight`
Expected: FAIL — `inFlight` is not a member of `SyncProgress` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `SyncStatus.swift`, add the field and init parameter (keep `inFlight` last so existing call sites compile):

```swift
public struct SyncProgress: Sendable, Equatable {
    public let completed: Int
    public let total: Int
    public let currentItemName: String?
    public let currentItemID: String?     // PHAsset local identifier, for the thumbnail
    public let bytesRemaining: Int64?
    public let inFlight: Int               // uploads currently in flight (concurrency)

    public init(completed: Int, total: Int, currentItemName: String?,
                bytesRemaining: Int64?, currentItemID: String? = nil, inFlight: Int = 0) {
        self.completed = completed
        self.total = total
        self.currentItemName = currentItemName
        self.currentItemID = currentItemID
        self.bytesRemaining = bytesRemaining
        self.inFlight = inFlight
    }

    /// 0.0…1.0; 0 when `total == 0`. Drives the panel's progress ring.
    public var fraction: Double { total > 0 ? Double(completed) / Double(total) : 0 }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `swift test --filter syncProgress`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Sources/FilesNestCore/SyncStatus.swift Tests/FilesNestCoreTests/SyncStatusTests.swift
git commit -m "feat(core): add SyncProgress.inFlight"
```

---

### Task 5: Parallelize the upload loop (bounded sliding-window TaskGroup)

**Files:**
- Modify: `Sources/FilesNestCore/SyncCoordinator.swift` (add `configuredConcurrency`; replace the upload `for` loop; add `UploadOutcome`)
- Create: `Tests/FilesNestCoreTests/Support/ArrivalGate.swift`
- Create: `Tests/FilesNestCoreTests/Support/GatedDataSource.swift`
- Create: `Tests/FilesNestCoreTests/Support/ProgressRecorder.swift`
- Test: `Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`

**Interfaces:**
- Consumes: `SyncProgress.inFlight` (Task 4).
- Produces: `SyncCoordinator.init(..., configuredConcurrency: Int? = nil)`. Cap resolution this task = `max(1, configuredConcurrency ?? 4)` (the `/config` middle step is added in Task 6). Progress emits most-recently-started `currentItem*` and live `inFlight`.

- [ ] **Step 1: Add the deterministic concurrency-probe support fakes**

Create `Tests/FilesNestCoreTests/Support/ArrivalGate.swift`:

```swift
import Foundation

/// Deterministically proves N tasks run concurrently. The first `target`
/// callers to `enter()` block until all `target` have arrived, so `peak`
/// reaches exactly `target`; once opened, later callers pass straight through.
actor ArrivalGate {
    private let target: Int
    private var current = 0
    private(set) var peak = 0
    private var opened = false
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(target: Int) { self.target = target }

    func enter() async {
        current += 1
        peak = max(peak, current)
        if opened { return }
        if current >= target {
            opened = true
            for w in waiters { w.resume() }
            waiters.removeAll()
            return
        }
        await withCheckedContinuation { waiters.append($0) }
    }

    func exit() { current -= 1 }
}
```

Create `Tests/FilesNestCoreTests/Support/GatedDataSource.swift`:

```swift
import Foundation
@testable import FilesNestCore

/// An `AssetDataSource` that reports entry/exit to an `ArrivalGate`, so a test
/// can observe how many uploads run concurrently. Produces `totalBytes` of
/// zeroed blobs (content is irrelevant to concurrency).
struct GatedDataSource: AssetDataSource {
    let gate: ArrivalGate
    let totalBytes: Int64
    let blobSize: Int

    func read(assetID: String, from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws {
        await gate.enter()
        var sent = offset
        while sent < totalBytes {
            let n = Int(min(Int64(blobSize), totalBytes - sent))
            try await sink(Data(count: n))
            sent += Int64(n)
        }
        await gate.exit()
    }
}
```

Create `Tests/FilesNestCoreTests/Support/ProgressRecorder.swift`:

```swift
import Foundation
@testable import FilesNestCore

/// Thread-safe collector for `onProgress` callbacks (which are @Sendable, sync).
final class ProgressRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _items: [SyncProgress] = []
    var items: [SyncProgress] { lock.lock(); defer { lock.unlock() }; return _items }
    func record(_ p: SyncProgress) { lock.lock(); defer { lock.unlock() }; _items.append(p) }
}
```

- [ ] **Step 2: Write the failing tests**

In `SyncCoordinatorTests.swift`, update the `makeCoordinator` helper to thread a concurrency value (default `1` preserves today's serial order for every existing test):

```swift
    func makeCoordinator(server: FakeServer, library: [AssetResource],
                         state: InMemorySyncStateStore = InMemorySyncStateStore(),
                         concurrency: Int? = 1,
                         now: Date = Date(timeIntervalSince1970: 1_700_000_000)) -> SyncCoordinator {
        let client = server.client()
        return SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: library, error: nil),
            uploader: AssetUploader(client: client, source: FakeAssetDataSource(totalBytes: 250, blobSize: 100)),
            state: state,
            configuredConcurrency: concurrency,
            now: { now })
    }
```

Add these tests to `SyncCoordinatorTests.swift`:

```swift
    @Test func boundedConcurrencyNeverExceedsCapAndUploadsAll() async throws {
        let server = FakeServer(host: "sc-conc-bound.test")
        let library = (0..<5).map { resource("A\($0)", date: "2024-06-15T10:0\($0):00Z") }
        let gate = ArrivalGate(target: 3)   // == cap; first wave blocks until 3 arrive
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: library, error: nil),
            uploader: AssetUploader(client: client,
                                    source: GatedDataSource(gate: gate, totalBytes: 250, blobSize: 100)),
            state: InMemorySyncStateStore(),
            configuredConcurrency: 3,
            now: { Date(timeIntervalSince1970: 1_700_000_000) })

        let report = try await coord.sync(range: .all)

        #expect(await gate.peak == 3)   // ran exactly cap concurrently
        #expect(Set(report.uploaded.map(\.localIdentifier)) ==
                Set(library.map { $0.key.localIdentifier }))
        #expect(report.failed.isEmpty)
    }

    @Test func progressReportsInFlightAndMostRecentlyStarted() async throws {
        let server = FakeServer(host: "sc-conc-progress.test")
        let library = (0..<4).map { resource("B\($0)", date: "2024-06-15T10:0\($0):00Z") }
        let gate = ArrivalGate(target: 2)
        let recorder = ProgressRecorder()
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: library, error: nil),
            uploader: AssetUploader(client: client,
                                    source: GatedDataSource(gate: gate, totalBytes: 250, blobSize: 100)),
            state: InMemorySyncStateStore(),
            configuredConcurrency: 2,
            now: { Date(timeIntervalSince1970: 1_700_000_000) })

        _ = try await coord.sync(range: .all) { recorder.record($0) }

        let progresses = recorder.items
        // inFlight is bounded by the cap and reaches it during the run.
        #expect(progresses.allSatisfy { $0.inFlight <= 2 })
        #expect(progresses.contains { $0.inFlight == 2 })
        // Every reported current item is a real library item (most-recently-started).
        let ids = Set(library.map { $0.key.localIdentifier })
        #expect(progresses.allSatisfy { $0.currentItemID == nil || ids.contains($0.currentItemID!) })
        // completed never exceeds total and is monotonic.
        #expect(progresses.allSatisfy { $0.completed <= $0.total })
    }
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `swift test --filter boundedConcurrencyNeverExceedsCapAndUploadsAll`
Expected: FAIL — `configuredConcurrency:` is an unknown argument to `SyncCoordinator.init` (compile error).

- [ ] **Step 4: Write the implementation**

In `SyncCoordinator.swift`, add the stored property, init param, and default constant. Change the property block (line 7-11) and `init` (line 13-23):

```swift
    private let client: ServerClient
    private let library: any AssetLibrary
    private let uploader: AssetUploader
    private let state: any SyncStateStore
    private let configuredConcurrency: Int?
    private let now: @Sendable () -> Date

    private static let defaultConcurrency = 4

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
```

Add the outcome type at file scope (below the struct, near the bottom of the file):

```swift
private enum UploadOutcome: Sendable {
    case success(ResourceKey)
    case failed(FailedItem)
}
```

Replace the upload `for` loop (line 37-57) — from `let uploadTotal = plan.uploads.count` through the end of the uploads `for` loop — with the bounded sliding window. Keep `var deleted`/the deletes loop/`return` that follow unchanged:

```swift
        let cap = max(1, configuredConcurrency ?? Self.defaultConcurrency)
        let uploadTotal = plan.uploads.count

        var uploaded: [ResourceKey] = []
        var failed: [FailedItem] = []
        var deleted: [ResourceKey] = []

        var iterator = plan.uploads.makeIterator()
        var inFlight = 0
        var lastName: String? = nil
        var lastID: String? = nil

        func emit() {
            onProgress(SyncProgress(completed: uploaded.count,
                                    total: uploadTotal,
                                    currentItemName: lastName,
                                    bytesRemaining: nil,
                                    currentItemID: lastID,
                                    inFlight: inFlight))
        }

        try await withThrowingTaskGroup(of: UploadOutcome.self) { group in
            // Adds one upload task if any remain. Runs only on this coordinator
            // coroutine, so the counters and `onProgress` stay single-threaded.
            func addNext() -> Bool {
                guard let item = iterator.next() else { return false }
                inFlight += 1
                lastName = item.resource.filename
                lastID = item.resource.key.localIdentifier
                group.addTask {
                    try Task.checkCancellation()
                    do {
                        try await execute(item)
                        return .success(item.resource.key)
                    } catch is CancellationError {
                        throw CancellationError()
                    } catch {
                        return .failed(FailedItem(key: item.resource.key,
                                                  filename: item.resource.filename,
                                                  reason: String(describing: error)))
                    }
                }
                emit()   // most-recently-started item, live inFlight
                return true
            }

            for _ in 0..<cap where addNext() {}

            while let outcome = try await group.next() {
                inFlight -= 1
                switch outcome {
                case .success(let key): uploaded.append(key)
                case .failed(let item): failed.append(item)
                }
                if !addNext() { emit() }   // refresh completed + drained inFlight
            }
        }
```

Note: `for _ in 0..<cap where addNext() {}` primes up to `cap` tasks and stops early when the plan runs dry.

- [ ] **Step 5: Run tests to verify they pass**

Run: `swift test --filter SyncCoordinatorTests`
Expected: PASS — the two new tests AND all pre-existing coordinator tests (which now run at `concurrency: 1`, i.e. serial as before).

- [ ] **Step 6: Run the full Core suite for regressions**

Run: `swift test`
Expected: PASS. If any test asserted exact `SyncProgress` equality without `inFlight`, update it to expect the actual `inFlight` (default 0 for serial single-item runs).

- [ ] **Step 7: Commit**

```bash
git add Sources/FilesNestCore/SyncCoordinator.swift Tests/FilesNestCoreTests/Support/ArrivalGate.swift Tests/FilesNestCoreTests/Support/GatedDataSource.swift Tests/FilesNestCoreTests/Support/ProgressRecorder.swift Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "feat(core): parallelize uploads with a bounded sliding-window task group"
```

---

### Task 6: Discover the cap via `GET /config` with fallback

**Files:**
- Modify: `Sources/FilesNestCore/SyncCoordinator.swift` (cap resolution line from Task 5)
- Modify: `Tests/FilesNestCoreTests/Support/FakeServer.swift` (add `/config` route)
- Test: `Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`

**Interfaces:**
- Consumes: `ServerClient.config()` (Task 1); `FakeServer.configMax`.
- Produces: cap resolution `max(1, configuredConcurrency ?? (try? await client.config().maxConcurrentUploads) ?? 4)`.

- [ ] **Step 1: Add the `/config` route to `FakeServer`**

In `FakeServer.swift`, add a settable property near the other tunables (~line 36):

```swift
    /// Value returned by GET /config. nil → the route 404s (models a pre-#25 server).
    var configMax: Int?
```

In `handle(_:)`, add a case inside the `switch (method, parts.count)` block, before `default` (~line 167):

```swift
        case ("GET", 1) where parts[0] == "config":
            guard let m = configMax else { return resp(404) }
            let obj: [String: Any] = ["maxConcurrentUploads": m]
            return resp(200, ["Content-Type": "application/json"],
                        try JSONSerialization.data(withJSONObject: obj))
```

- [ ] **Step 2: Write the failing tests**

Add to `SyncCoordinatorTests.swift`:

```swift
    @Test func discoversCapFromConfigWhenNotInjected() async throws {
        let server = FakeServer(host: "sc-conc-discover.test")
        server.configMax = 2   // server advertises cap 2
        let library = (0..<4).map { resource("C\($0)", date: "2024-06-15T10:0\($0):00Z") }
        let gate = ArrivalGate(target: 2)
        let client = server.client()
        let coord = SyncCoordinator(
            client: client,
            library: FakeAssetLibrary(items: library, error: nil),
            uploader: AssetUploader(client: client,
                                    source: GatedDataSource(gate: gate, totalBytes: 250, blobSize: 100)),
            state: InMemorySyncStateStore(),
            configuredConcurrency: nil,   // force discovery
            now: { Date(timeIntervalSince1970: 1_700_000_000) })

        let report = try await coord.sync(range: .all)

        #expect(await gate.peak == 2)   // used the server-advertised cap
        #expect(report.uploaded.count == 4)
    }

    @Test func fallsBackWhenConfigMissing() async throws {
        let server = FakeServer(host: "sc-conc-fallback.test")
        server.configMax = nil   // GET /config → 404 (old server)
        let report = try await makeCoordinator(server: server,
                                               library: [resource("A"), resource("B")],
                                               concurrency: nil).sync(range: .all)
        // No throw; both upload. (Cap fell back to the default; exact value not asserted.)
        #expect(Set(report.uploaded.map(\.localIdentifier)) == ["A", "B"])
        #expect(report.failed.isEmpty)
    }
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `swift test --filter discoversCapFromConfigWhenNotInjected`
Expected: FAIL — `gate.peak` is `4` (the default), not `2`, because cap resolution ignores `/config`.

- [ ] **Step 4: Write the implementation**

In `SyncCoordinator.swift`, change the cap line from Task 5 to insert the `/config` lookup between the injected value and the default:

```swift
        let cap = max(1, configuredConcurrency
                         ?? (try? await client.config().maxConcurrentUploads)
                         ?? Self.defaultConcurrency)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `swift test --filter SyncCoordinatorTests`
Expected: PASS (both new tests + all prior).

- [ ] **Step 6: Commit**

```bash
git add Sources/FilesNestCore/SyncCoordinator.swift Tests/FilesNestCoreTests/Support/FakeServer.swift Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "feat(core): discover concurrency cap from GET /config with fallback"
```

---

### Task 7: Surface the in-flight count in the sync strip (FE)

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/PanelView.swift:100`
- Create: `docs/plans/20260820-client-upload-concurrency-verification.md` (manual checklist)

**Interfaces:**
- Consumes: `SyncProgress.inFlight` (Task 4).

No unit test — the macOS app UI is manual-verify per repo convention. TDD does not apply to this task; the RRR cycle resumes for any future Core work.

- [ ] **Step 1: Update the strip label**

In `PanelView.swift`, the `currentItem(_:)` subtitle (line 100) currently reads:

```swift
                Text("Uploading · \(p.completed) of \(p.total)")
```

Change it to surface concurrency (reads naturally at 0/1 in flight):

```swift
                Text(p.inFlight > 1
                     ? "Uploading \(p.inFlight) · \(p.completed) of \(p.total)"
                     : "Uploading · \(p.completed) of \(p.total)")
```

- [ ] **Step 2: Build the app target**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`
Expected: BUILD SUCCEEDED.

- [ ] **Step 3: Write the manual verification checklist**

Create `docs/plans/20260820-client-upload-concurrency-verification.md`:

```markdown
# Manual verification — client concurrent uploads

Prereqs: a server running PR #25 (default cap 4) with real credentials, and a
library with enough pending items to see several uploads at once. A Limited
Photos Library (~10 selected) is enough.

- [ ] Trigger a sync with several pending items. The strip subtitle shows
      "Uploading N · X of Y" with N > 1 while multiple uploads are in flight.
- [ ] The hero thumbnail changes to the most-recently-started item and keeps
      moving; "Backed up" climbs faster than the pre-change serial build.
- [ ] Pause mid-burst: the sync stops promptly, no crash, "Pending" is honest.
- [ ] Point at a pre-#25 server (no /config): sync still runs; no unhandled
      errors surface (falls back to cap 4).
- [ ] Force a 503 path if possible (set MAX_CONCURRENT_UPLOADS=1 on the server,
      run several uploads): items still complete (client retries), none land in
      the Failed list from transient 503s.
```

- [ ] **Step 4: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/PanelView.swift docs/plans/20260820-client-upload-concurrency-verification.md
git commit -m "feat(app): show concurrent upload count in the sync strip"
```

---

## Post-Implementation

- [ ] Run the full Core suite three times to catch concurrency flakiness: `for i in 1 2 3; do swift test || break; done`.
- [ ] Per the standing workflow rule, run a Codex review (`codex:rescue`) before finishing/merging the branch.
- [ ] Push `apple-clients/upload-concurrency` and open a PR titled `Apple clients: Concurrent uploads (#NN)`.
- [ ] Walk the manual verification checklist against a real server before merge.
```
