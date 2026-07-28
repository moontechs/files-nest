# PhotoKit AssetLibrary + Real Sync Now — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make "Sync Now" run a real one-shot reconcile of the Mac's Photos library against the server, with a live progress ring.

**Architecture:** Two Core additions (a backward-compatible progress hook on `SyncCoordinator`; a `LiveSyncEngine` that choreographs `SyncStatus` around an injected `perform` closure) plus the PhotoKit `PhotosAssetLibrary` enumeration adapter and composition-root rewire in the macOS app. The engine stays PhotoKit-free and headless-testable; the app composes the real `SyncCoordinator` into `perform`.

**Tech Stack:** Swift 6 (language mode), swift-testing (`import Testing`), Foundation, PhotoKit (`Photos`, app target only), SwiftUI (`MenuBarExtra`).

**Design doc:** `docs/design/20260726-photos-library-real-syncnow.md`

## Global Constraints

- Swift 6 language mode; **zero** concurrency warnings. `NSLock` must never be held across an `await`.
- `FilesNestCore` is pure Foundation/Security — **no PhotoKit, no SwiftUI** in Core. PhotoKit lives only in the app target (`apple/macos/FilesNest/FilesNest`).
- All Core work is reachable by `swift test`. PhotoKit residue is manual-verify.
- Every test is **failure-injected and watched to fail first** before writing the implementation (spec §7 discipline).
- App target uses **file-system-synchronized groups**: drop `.swift` files into `apple/macos/FilesNest/FilesNest/` — no `.pbxproj` hand-edits.
- Core build/test dir: `apple/FilesNestCore`. Run tests with `swift test` from there.
- macOS app build: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build` (run from `apple/macos/FilesNest`).
- Branch: `apple/photos-library-real-syncnow` (already created off `main`). PR title: `Apple clients: PhotoKit AssetLibrary + real Sync Now (#7)`.
- Commit style: `feat:` / `test:` / `docs:` prefixes (matches history).

---

### Task 1: `SyncCoordinator` progress hook

Add an additive, backward-compatible `onProgress` callback to `SyncCoordinator.sync` that fires once before each planned upload. Default no-op keeps every existing caller and test unchanged.

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift` (the `sync(range:)` method + the upload loop)
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift` (new extension + `ProgressBox` helper)

**Interfaces:**
- Consumes: existing `SyncCoordinator.init(client:library:uploader:state:now:)`, `SyncPlan.uploads` (ordered `creationDate` asc then key), `SyncProgress(completed:total:currentItemName:bytesRemaining:)`, `AssetResource.filename`. Test harness: `FakeServer`, `makeCoordinator(server:library:state:now:)`, `date(_:)`.
- Produces: `SyncCoordinator.sync(range:onProgress:) async throws -> SyncReport` where `onProgress: @Sendable (SyncProgress) -> Void = { _ in }`. Before upload item `i` of `N = plan.uploads.count`, calls `onProgress(SyncProgress(completed: i, total: N, currentItemName: resource.filename, bytesRemaining: nil))`. Fires zero times when `N == 0`.

- [ ] **Step 1: Write the failing tests + helper**

Append to `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift` (after the last extension):

```swift
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter SyncCoordinatorTests/progress 2>&1 | tail -20`
Expected: **compile failure** — `sync(range:onProgress:)` does not exist (extra argument `onProgress`). This confirms the tests exercise the new API.

- [ ] **Step 3: Add the `onProgress` parameter and emit per upload**

In `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift`, change the `sync` signature and the upload loop. The method currently starts:

```swift
    public func sync(range: SyncRange) async throws -> SyncReport {
        state.saveLastSyncStarted(now())
```

Replace the signature line with:

```swift
    public func sync(range: SyncRange,
                     onProgress: @Sendable (SyncProgress) -> Void = { _ in }) async throws -> SyncReport {
        state.saveLastSyncStarted(now())
```

Then replace the upload loop. The current loop is:

```swift
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
```

with:

```swift
        let uploadTotal = plan.uploads.count
        for (index, item) in plan.uploads.enumerated() {
            try Task.checkCancellation()
            onProgress(SyncProgress(completed: index,
                                    total: uploadTotal,
                                    currentItemName: item.resource.filename,
                                    bytesRemaining: nil))
            do {
                try await execute(item)
                uploaded.append(item.resource.key)
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                failed.append(FailedItem(key: item.resource.key, reason: String(describing: error)))
            }
        }
```

- [ ] **Step 4: Run the full coordinator suite to verify pass + no regression**

Run: `cd apple/FilesNestCore && swift test --filter SyncCoordinatorTests 2>&1 | tail -20`
Expected: **all pass** — the two new progress tests plus every pre-existing `SyncCoordinatorTests` (the default `onProgress` argument means the untouched tests still compile and pass).

- [ ] **Step 5: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift
git commit -m "feat: onProgress hook on SyncCoordinator.sync"
```

---

### Task 2: `LiveSyncEngine`

The real `SyncEngine` for one-shot Sync Now. Pure `SyncStatus` choreography around an injected `perform` closure — no PhotoKit, no change to `SyncStatus`. Reuses `StubSyncEngine`'s `NSLock` + continuations streaming pattern.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`

**Interfaces:**
- Consumes: `SyncEngine` protocol (`statusStream/start/pause/resume/syncNow`), `SyncStatus`, `SyncProgress`, `SyncReport`, `FailedItem`, `CredentialStore.basicCredentials()`, `SyncStateStore.loadLastSyncStarted()`, `SyncRange`. Test doubles: `StaticCredentialStore`, `InMemorySyncStateStore`.
- Produces: `LiveSyncEngine(credentials:state:perform:now:)` where `perform: @escaping @Sendable (SyncRange, @Sendable (SyncProgress) -> Void) async throws -> SyncReport`. `syncNow()` runs `perform(.all, …)`; success → `.watching(lastSync:)` (even with `report.failed` non-empty); throw → `.error`; `CancellationError` → `.watching`. No-op when signed out, paused, or already syncing.

- [ ] **Step 1: Write the failing tests**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct LiveSyncEngineTests {

    // Reads the current status from a fresh stream (current-status-first).
    func firstStatus(_ engine: any SyncEngine) async -> SyncStatus {
        var it = engine.statusStream().makeAsyncIterator()
        return await it.next()!
    }

    func creds(_ present: Bool) -> StaticCredentialStore {
        StaticCredentialStore(present ? .init(username: "u", password: "p") : nil)
    }

    func emptyReport() -> SyncReport {
        SyncReport(uploaded: [], deleted: [], failed: [], skipped: 0)
    }

    @Test func startWithoutCredentialsIsSignedOut() async {
        let engine = LiveSyncEngine(credentials: creds(false), state: InMemorySyncStateStore(),
                                    perform: { _, _ in self.emptyReport() })
        await engine.start()
        #expect(await firstStatus(engine) == .signedOut)
    }

    @Test func startWithCredentialsIsWatchingWithStoredLastSync() async {
        let state = InMemorySyncStateStore()
        let d = Date(timeIntervalSince1970: 1_700_000_000)
        state.saveLastSyncStarted(d)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, _ in self.emptyReport() })
        await engine.start()
        #expect(await firstStatus(engine) == .watching(lastSync: d))
    }

    @Test func syncNowIgnoredWhenSignedOut() async {
        let calls = Counter()
        let engine = LiveSyncEngine(credentials: creds(false), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await calls.inc(); return self.emptyReport() })
        await engine.start()
        await engine.syncNow()
        #expect(await firstStatus(engine) == .signedOut)
        #expect(await calls.value == 0)
    }

    @Test func syncNowEmitsSyncingProgressThenWatching() async {
        let state = InMemorySyncStateStore()
        let started = Date(timeIntervalSince1970: 1_700_000_500)
        let p0 = SyncProgress(completed: 0, total: 2, currentItemName: "A.jpg", bytesRemaining: nil)
        let p1 = SyncProgress(completed: 1, total: 2, currentItemName: "B.jpg", bytesRemaining: nil)
        let engine = LiveSyncEngine(credentials: creds(true), state: state,
                                    perform: { _, onProgress in
            state.saveLastSyncStarted(started)   // simulate the coordinator stamping start
            onProgress(p0); onProgress(p1)
            return self.emptyReport()
        })
        await engine.start()                      // → .watching(nil)

        // Subscribe BEFORE syncNow; the build closure runs synchronously and buffers
        // the current status (.watching(nil)) first. Default buffering is unbounded,
        // so no yields are dropped before we drain them.
        var it = engine.statusStream().makeAsyncIterator()
        await engine.syncNow()

        var got: [SyncStatus] = []
        for _ in 0..<5 { got.append(await it.next()!) }
        #expect(got == [
            .watching(lastSync: nil),
            .syncing(SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)),
            .syncing(p0),
            .syncing(p1),
            .watching(lastSync: started),
        ])
    }

    @Test func partialFailuresStillEndWatching() async {
        let key = ResourceKey(localIdentifier: "X", kind: .photo)
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            SyncReport(uploaded: [], deleted: [], failed: [FailedItem(key: key, reason: "boom")], skipped: 0)
        })
        await engine.start()
        await engine.syncNow()
        guard case .watching = await firstStatus(engine) else { Issue.record("expected .watching"); return }
    }

    @Test func wholeSyncThrowSetsError() async {
        struct Boom: Error {}
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in throw Boom() })
        await engine.start()
        await engine.syncNow()
        guard case .error = await firstStatus(engine) else { Issue.record("expected .error"); return }
    }

    @Test func reentrantSyncNowIsIgnoredWhileSyncing() async {
        let calls = Counter()
        let inside = Gate()      // opened once perform is running
        let release = Gate()     // test opens it to let perform finish
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in
            await calls.inc()
            await inside.open()
            await release.wait()
            return self.emptyReport()
        })
        await engine.start()

        let first = Task { await engine.syncNow() }
        await inside.wait()          // first sync is now inside perform (status .syncing)
        await engine.syncNow()       // second call must be ignored (guard)
        #expect(await calls.value == 1)

        await release.open()
        await first.value
        #expect(await calls.value == 1)
    }

    @Test func pauseBlocksSyncNowResumeReturnsToWatching() async {
        let calls = Counter()
        let engine = LiveSyncEngine(credentials: creds(true), state: InMemorySyncStateStore(),
                                    perform: { _, _ in await calls.inc(); return self.emptyReport() })
        await engine.start()
        await engine.pause()
        guard case .paused = await firstStatus(engine) else { Issue.record("expected .paused"); return }
        await engine.syncNow()
        #expect(await calls.value == 0)
        await engine.resume()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }
}

/// Counts `perform` invocations across concurrency.
actor Counter {
    private(set) var value = 0
    func inc() { value += 1 }
}

/// One-shot async gate: `wait()` suspends until `open()` (idempotent).
actor Gate {
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private var opened = false
    func wait() async {
        if opened { return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func open() {
        opened = true
        for w in waiters { w.resume() }
        waiters.removeAll()
    }
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter LiveSyncEngineTests 2>&1 | tail -20`
Expected: **compile failure** — `cannot find 'LiveSyncEngine' in scope`.

- [ ] **Step 3: Implement `LiveSyncEngine`**

Create `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift`:

```swift
import Foundation

/// A real `SyncEngine` that runs a single `SyncCoordinator` pass on `syncNow()`
/// and publishes `SyncStatus`. Continuous watching (a `PHPhotoLibraryChangeObserver`
/// + scheduler) is a later slice — hence "Live", not "Continuous": this drives the
/// one-shot Sync Now against the live library + server.
///
/// PhotoKit-free by construction: the app's composition root builds the real
/// `SyncCoordinator` (+ PhotoKit adapters) into the injected `perform` closure,
/// keeping this unit headless-testable with a fake `perform`.
public final class LiveSyncEngine: SyncEngine, @unchecked Sendable {
    public typealias Perform =
        @Sendable (SyncRange, @Sendable (SyncProgress) -> Void) async throws -> SyncReport

    private let credentials: any CredentialStore
    private let state: any SyncStateStore
    private let perform: Perform
    private let now: @Sendable () -> Date

    private let lock = NSLock()
    private var status: SyncStatus = .signedOut
    private var isSyncing = false
    private var continuations: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]

    public init(credentials: any CredentialStore,
                state: any SyncStateStore,
                perform: @escaping Perform,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.state = state
        self.perform = perform
        self.now = now
    }

    public func statusStream() -> AsyncStream<SyncStatus> {
        AsyncStream { continuation in
            let id = UUID()
            lock.lock()
            continuation.yield(status)          // current status first
            continuations[id] = continuation
            lock.unlock()
            continuation.onTermination = { [weak self] _ in
                guard let self else { return }
                self.lock.lock(); self.continuations[id] = nil; self.lock.unlock()
            }
        }
    }

    private func set(_ newStatus: SyncStatus) {
        lock.lock()
        status = newStatus
        let conts = Array(continuations.values)
        lock.unlock()
        for c in conts { c.yield(newStatus) }
    }

    private var isSignedOut: Bool {
        lock.lock(); defer { lock.unlock() }
        if case .signedOut = status { return true }
        return false
    }

    private var isPaused: Bool {
        lock.lock(); defer { lock.unlock() }
        if case .paused = status { return true }
        return false
    }

    /// Atomically claim the single sync slot. Returns false if one is already running.
    private func beginSyncing() -> Bool {
        lock.lock(); defer { lock.unlock() }
        if isSyncing { return false }
        isSyncing = true
        return true
    }

    private func endSyncing() { lock.lock(); isSyncing = false; lock.unlock() }

    /// Last sync = the coordinator-persisted start time (single source of truth).
    private var lastSync: Date? { state.loadLastSyncStarted() }

    public func start() async {
        let creds = try? await credentials.basicCredentials()
        set(creds == nil ? .signedOut : .watching(lastSync: lastSync))
    }

    public func pause() async {
        guard !isSignedOut else { return }
        set(.paused(pending: 0))
    }

    public func resume() async {
        guard !isSignedOut else { return }
        set(.watching(lastSync: lastSync))
    }

    public func syncNow() async {
        guard !isSignedOut, !isPaused else { return }
        guard beginSyncing() else { return }        // re-entrancy guard
        defer { endSyncing() }

        set(.syncing(SyncProgress(completed: 0, total: 0,
                                  currentItemName: nil, bytesRemaining: nil)))
        do {
            let report = try await perform(.all) { [weak self] progress in
                self?.set(.syncing(progress))
            }
            if !report.failed.isEmpty { logFailures(report.failed) }
            set(.watching(lastSync: lastSync))
        } catch is CancellationError {
            set(.watching(lastSync: lastSync))       // cancellation is not an error
        } catch {
            set(.error(message: String(describing: error)))
        }
    }

    /// Per-item failures don't fail the whole sync (skip-and-continue). Surface them
    /// to the log; a later slice renders `SyncReport.failed` in the panel.
    private func logFailures(_ failed: [FailedItem]) {
        for item in failed {
            print("FilesNest sync: failed \(item.key.encoded): \(item.reason)")
        }
    }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter LiveSyncEngineTests 2>&1 | tail -20`
Expected: **all pass**.

- [ ] **Step 5: Run the whole Core suite (no regressions, no warnings)**

Run: `cd apple/FilesNestCore && swift test 2>&1 | tail -25`
Expected: full suite green, **zero** Swift 6 warnings.

- [ ] **Step 6: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/LiveSyncEngineTests.swift
git commit -m "feat: LiveSyncEngine (one-shot real Sync Now)"
```

---

### Task 3: `PhotosAssetLibrary` adapter + app wiring + manual verification

Add the PhotoKit enumeration adapter, rewire the composition root from `StubSyncEngine` to `LiveSyncEngine`, and document the manual verification checklist (this is the untestable PhotoKit residue).

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` (composition root)
- Create: `docs/plans/20260726-photos-library-real-syncnow-verification.md`

**Interfaces:**
- Consumes: `AssetLibrary`, `AssetResource`, `ResourceKey`, `ResourceKind`, `SyncRange`, `SyncCoordinator.sync(range:onProgress:)`, `LiveSyncEngine`, `ServerClient`, `AssetUploader`, `PhotosAssetDataSource`, `UserDefaultsServerURLStore.load()`, `KeychainStore` (a `CredentialStore`), `UserDefaultsSyncStateStore(defaults:)`, `AppModel(engine:)`, `SettingsModel(urlStore:credStore:probe:)`, `ConnectionProbe`.
- Produces: `PhotosAssetLibrary()` conforming to `AssetLibrary`; a `LiveSyncEngine` wired into `AppModel`; `NotSignedInError`.

- [ ] **Step 1: Create `PhotosAssetLibrary`**

Create `apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift`:

```swift
import Foundation
import Photos
import FilesNestCore

/// PhotoKit enumeration adapter — the `AssetLibrary` counterpart to
/// `PhotosAssetDataSource`. Enumerates `PHAsset`s in `range` and maps each
/// addressable `PHAssetResource` to an `AssetResource` keyed by `ResourceKey`.
/// A Live Photo yields two entries (`#photo` + `#pairedVideo`) sharing `bundleID`,
/// exactly what `SyncPlanner` expects (design §3.3, §5.4).
nonisolated struct PhotosAssetLibrary: AssetLibrary {

    enum LibraryError: Error, Equatable {
        case authorizationDenied(PHAuthorizationStatus)
    }

    func resources(in range: SyncRange) async throws -> [AssetResource] {
        try await ensureAuthorized()

        let options = PHFetchOptions()
        options.sortDescriptors = [NSSortDescriptor(key: "creationDate", ascending: true)]
        if case .dates(let r) = range {
            options.predicate = NSPredicate(format: "creationDate >= %@ AND creationDate <= %@",
                                            r.lowerBound as NSDate, r.upperBound as NSDate)
        }

        let assets = PHAsset.fetchAssets(with: options)
        var out: [AssetResource] = []
        assets.enumerateObjects { asset, _, _ in
            let isLive = asset.mediaSubtypes.contains(.photoLive)
            let created = asset.creationDate ?? .distantPast   // non-optional key field (design §3.4)
            for resource in PHAssetResource.assetResources(for: asset) {
                guard let kind = Self.mapType(resource.type) else { continue }   // skip unaddressed types
                out.append(AssetResource(
                    key: ResourceKey(localIdentifier: asset.localIdentifier, kind: kind),
                    filename: resource.originalFilename,
                    creationDate: created,
                    bundleID: isLive ? asset.localIdentifier : nil))
            }
        }
        return out
    }

    private func ensureAuthorized() async throws {
        let current = PHPhotoLibrary.authorizationStatus(for: .readWrite)
        let status = current == .notDetermined
            ? await PHPhotoLibrary.requestAuthorization(for: .readWrite)
            : current
        guard status == .authorized || status == .limited else {
            throw LibraryError.authorizationDenied(status)
        }
    }

    /// Reverse of `PhotosAssetDataSource.mapKind`. Returns nil for resource types
    /// this client does not address (adjustment data, full-size video, etc.); those
    /// are skipped, not errored — they are not uploadable resources.
    private static func mapType(_ type: PHAssetResourceType) -> ResourceKind? {
        switch type {
        case .photo:          return .photo
        case .video:          return .video
        case .audio:          return .audio
        case .pairedVideo:    return .pairedVideo
        case .fullSizePhoto:  return .fullSizePhoto
        case .alternatePhoto: return .alternatePhoto
        default:              return nil
        }
    }
}
```

- [ ] **Step 2: Rewire the composition root to `LiveSyncEngine`**

In `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`, replace the entire `init()` body with:

```swift
    init() {
        let defaults   = UserDefaults.standard
        let urlStore   = UserDefaultsServerURLStore(defaults: defaults)
        let credStore  = KeychainStore()
        let stateStore = UserDefaultsSyncStateStore(defaults: defaults)

        let engine = LiveSyncEngine(
            credentials: credStore,
            state: stateStore,
            perform: { range, onProgress in
                // Read URL + creds at sync time so a Settings change takes effect.
                guard let url = urlStore.load(),
                      (try await credStore.basicCredentials()) != nil else {
                    throw NotSignedInError()
                }
                let client   = ServerClient(baseURL: url, credentials: credStore)
                let uploader = AssetUploader(client: client, source: PhotosAssetDataSource())
                let coordinator = SyncCoordinator(client: client,
                                                  library: PhotosAssetLibrary(),
                                                  uploader: uploader,
                                                  state: stateStore)
                return try await coordinator.sync(range: range, onProgress: onProgress)
            })

        let appModel = AppModel(engine: engine)
        let settingsModel = SettingsModel(urlStore: urlStore,
                                          credStore: KeychainStore(),
                                          probe: ConnectionProbe())
        settingsModel.onSaved = { appModel.restart() }
        _model = StateObject(wrappedValue: appModel)
        _settings = StateObject(wrappedValue: settingsModel)
    }
```

Then add this type at the end of `FilesNestApp.swift` (after the `AppDelegate` class):

```swift
/// Thrown by the sync `perform` closure when no server URL or credentials are set.
struct NotSignedInError: Error {}
```

- [ ] **Step 3: Build the macOS app**

Run: `cd apple/macos/FilesNest && xcodebuild -project FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build 2>&1 | tail -25`
Expected: **BUILD SUCCEEDED**, no warnings from the new files. (`PhotosAssetLibrary.swift` is auto-included via the file-system-synchronized group.)

- [ ] **Step 4: Write the manual verification checklist**

Create `docs/plans/20260726-photos-library-real-syncnow-verification.md`:

```markdown
# Manual verification: PhotoKit AssetLibrary + real Sync Now

The PhotoKit enumeration adapter (`PhotosAssetLibrary`) is untestable in `swift test`
(it needs the real photo library + TCC). Verify by hand against a live FilesNest server.

## Setup
- [ ] Build & run the app (`xcodebuild … build`, then launch the product, or Run in Xcode).
- [ ] Confirm it launches as a background agent (menu-bar icon, no Dock icon).
- [ ] In Settings, enter a valid server URL + Basic Auth creds; Test Connection → ok; Save.

## Authorization
- [ ] First Sync Now triggers the macOS Photos permission prompt
      (copy: "FilesNest reads your photo and video originals to back them up to your server.").
- [ ] Grant access.

## Real sync
- [ ] Sync Now uploads real originals: the ring advances, and the current-item strip shows
      real filenames (e.g. `IMG_xxxx.HEIC`).
- [ ] On completion the panel returns to "watching" with a "last synced" timestamp.
- [ ] Server side: uploaded files appear under the user's `year/month/day` tree.
- [ ] A **Live Photo** produces two server records sharing a `bundleID` (the `#photo`
      and `#pairedVideo` resources).
- [ ] Running Sync Now again with no new photos completes quickly with no new uploads
      (already-in-sync → skipped).

## Failure paths
- [ ] Deny Photos access (reset via `tccutil reset Photos <bundle-id>` or System Settings),
      Sync Now → panel shows an error state (not a crash).
- [ ] Clear the server URL/creds → Sync Now → panel shows signed-out / error (not a crash).

## Notes
- `bytesRemaining` is intentionally `nil` (PhotoKit doesn't expose resource size publicly),
  so the ring shows count progress only — no byte countdown.
- If enumeration is blocked by the app sandbox rather than TCC, add the Photos entitlement
  to the target; the usage string alone is already present.
```

- [ ] **Step 5: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/PhotosAssetLibrary.swift \
        apple/macos/FilesNest/FilesNest/FilesNestApp.swift \
        docs/plans/20260726-photos-library-real-syncnow-verification.md
git commit -m "feat: PhotoKit PhotosAssetLibrary + wire real Sync Now into the app"
```

- [ ] **Step 6: Perform the manual verification**

Work through `docs/plans/20260726-photos-library-real-syncnow-verification.md` against a live server and check off each item. Record the outcome in the PR description.

---

## Self-Review

**Spec coverage:**
- §2 `PhotosAssetLibrary` (auth, `.all`/`.dates` fetch, reverse kind-map, Live-Photo `bundleID`, `creationDate` fallback) → Task 3 Step 1 + §3.1–3.4.
- §3 progress hook on `SyncCoordinator` → Task 1.
- §4/§5 `LiveSyncEngine` (streaming, start/pause/resume, syncNow, re-entrancy, partial-failure → watching, error mapping, `lastSync` semantics) → Task 2.
- §6 app wiring (read URL+creds at sync time, `NotSignedInError`, `UserDefaultsSyncStateStore`) → Task 3 Step 2.
- §7 testing (engine fake-`perform` tests; coordinator progress tests; manual PhotoKit checklist) → Tasks 1–3.
- §8 error handling table → Task 2 syncNow (`.error` on throw, `.watching` on partial failure) + Task 3 auth throw.

**Placeholder scan:** No TBD/TODO; every code step shows complete code; commands include expected output.

**Type consistency:** `sync(range:onProgress:)` signature, `SyncProgress(completed:total:currentItemName:bytesRemaining:)`, `SyncReport(uploaded:deleted:failed:skipped:)`, `FailedItem(key:reason:)`, `LiveSyncEngine(credentials:state:perform:now:)`, `SyncCoordinator(client:library:uploader:state:)`, `AssetUploader(client:source:)`, `ServerClient(baseURL:credentials:)`, `UserDefaultsServerURLStore.load()`, `SettingsModel(urlStore:credStore:probe:)` all verified against the existing sources.
