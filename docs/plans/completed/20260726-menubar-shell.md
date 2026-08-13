# macOS Menu-Bar Shell + Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `FilesNestCore` into a runnable macOS menu-bar app: a status panel + a Settings window (credentials → `KeychainStore`, Test Connection), driven by a stubbed `SyncEngine` seam that the PhotoKit-backed engine will replace next slice.

**Architecture:** UI-agnostic logic and seams live in `FilesNestCore` (pure, `swift test`-covered): `SyncStatus`/`SyncProgress`, `SyncEngine` + `StubSyncEngine`, `ConnectionProbe`, `ServerURLStore`, `StaticCredentialStore`. The SwiftUI shell in `apple/macos/FilesNest` (`MenuBarExtra(.window)` panel, Settings, thin `@MainActor` models, composition root) is compile-verified with `xcodebuild` and manually verified.

**Tech Stack:** Swift 6, Foundation + Security (Core), SwiftUI + ServiceManagement (app), swift-testing, SwiftPM, Xcode 26 (synchronized groups — drop-in source files).

## Global Constraints

- Swift 6 language mode, complete concurrency checking, **zero warnings**.
- Core stays pure Foundation/Security — **no SwiftUI in `FilesNestCore`**. All Core types `Sendable`.
- Core tests: swift-testing (`import Testing`, `@Test`, `#expect`), `@testable import FilesNestCore`, fakes in `Tests/FilesNestCoreTests/Support/`. Reuse existing `MockURLProtocol` (`setHandler(forHost:)`, `makeSession()`, `respond(status:headers:body:for:)`).
- App-target source files go in `apple/macos/FilesNest/FilesNest/` (synchronized group — no `.pbxproj` edits). App is a menu-bar agent via `NSApp.setActivationPolicy(.accessory)`.
- Core verify: `cd apple/FilesNestCore && swift test`. App verify: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO build`.
- `SyncStatus` cases: `.signedOut`, `.watching(lastSync: Date?)`, `.syncing(SyncProgress)`, `.paused(pending: Int)`, `.error(message: String)`.
- Reference: `docs/design/20260726-menubar-shell.md`; panel mockup `docs/design/mockups/20260726-menubar-panel.html`.

---

### Task 1: Status model (`SyncProgress`, `SyncStatus`)

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncStatus.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncStatusTests.swift`

**Interfaces:**
- Produces: `SyncProgress(completed:total:currentItemName:bytesRemaining:)` with `var fraction: Double`; `enum SyncStatus` (cases above). Both `Sendable, Equatable`.

- [x] **Step 1: Write the failing test**

Create `SyncStatusTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct SyncStatusTests {
    @Test func fractionIsCompletedOverTotal() {
        let p = SyncProgress(completed: 3, total: 12, currentItemName: "IMG.HEIC", bytesRemaining: 100)
        #expect(p.fraction == 0.25)
    }

    @Test func fractionIsZeroWhenTotalZero() {
        let p = SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)
        #expect(p.fraction == 0)
    }

    @Test func statusEquates() {
        #expect(SyncStatus.watching(lastSync: nil) == .watching(lastSync: nil))
        #expect(SyncStatus.paused(pending: 2) != .paused(pending: 3))
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter SyncStatusTests`
Expected: FAIL — `SyncProgress` / `SyncStatus` undefined.

- [x] **Step 3: Write minimal implementation**

Create `SyncStatus.swift`:

```swift
import Foundation

public struct SyncProgress: Sendable, Equatable {
    public let completed: Int
    public let total: Int
    public let currentItemName: String?
    public let bytesRemaining: Int64?

    public init(completed: Int, total: Int, currentItemName: String?, bytesRemaining: Int64?) {
        self.completed = completed
        self.total = total
        self.currentItemName = currentItemName
        self.bytesRemaining = bytesRemaining
    }

    /// 0.0…1.0; 0 when `total == 0`. Drives the panel's progress ring.
    public var fraction: Double { total > 0 ? Double(completed) / Double(total) : 0 }
}

public enum SyncStatus: Sendable, Equatable {
    case signedOut                    // no credentials → "Sign in in Settings"
    case watching(lastSync: Date?)    // idle, monitoring for new items
    case syncing(SyncProgress)
    case paused(pending: Int)
    case error(message: String)
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter SyncStatusTests`
Expected: PASS (3 tests), no warnings.

- [x] **Step 5: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncStatus.swift apple/FilesNestCore/Tests/FilesNestCoreTests/SyncStatusTests.swift
git commit -m "feat: SyncStatus + SyncProgress model for shell"
```

---

### Task 2: Settings stores (`ServerURLStore`, `StaticCredentialStore`)

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/ServerURLStore.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/StaticCredentialStore.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/ShellStoresTests.swift`

**Interfaces:**
- Consumes: `CredentialStore`, `BasicCredentials` (existing).
- Produces: `protocol ServerURLStore: Sendable { func load() -> URL?; func save(_ url: URL) }`; `final class UserDefaultsServerURLStore(defaults:)`; `struct StaticCredentialStore(_ credentials: BasicCredentials?)`.

- [x] **Step 1: Write the failing test**

Create `ShellStoresTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct ShellStoresTests {
    @Test func serverURLRoundTrips() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        let store = UserDefaultsServerURLStore(defaults: suite)
        #expect(store.load() == nil)
        store.save(URL(string: "https://nest.home.example")!)
        #expect(store.load() == URL(string: "https://nest.home.example")!)
    }

    @Test func serverURLNilWhenStoredValueInvalid() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        suite.set("", forKey: "com.filesnest.serverURL")
        let store = UserDefaultsServerURLStore(defaults: suite)
        #expect(store.load() == nil)
    }

    @Test func staticCredentialStoreReturnsValueThenNil() async throws {
        let creds = BasicCredentials(username: "u", password: "p")
        #expect(try await StaticCredentialStore(creds).basicCredentials() == creds)
        #expect(try await StaticCredentialStore(nil).basicCredentials() == nil)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter ShellStoresTests`
Expected: FAIL — types undefined.

- [x] **Step 3: Write minimal implementation**

Create `ServerURLStore.swift`:

```swift
import Foundation

public protocol ServerURLStore: Sendable {
    func load() -> URL?
    func save(_ url: URL)
}

/// Server URL is configuration, not a secret — stored in UserDefaults (inject a
/// suite in tests). An empty or malformed stored string loads as `nil`.
public final class UserDefaultsServerURLStore: ServerURLStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let key = "com.filesnest.serverURL"

    public init(defaults: UserDefaults) { self.defaults = defaults }

    public func load() -> URL? {
        guard let s = defaults.string(forKey: key), !s.isEmpty else { return nil }
        return URL(string: s)
    }

    public func save(_ url: URL) { defaults.set(url.absoluteString, forKey: key) }
}
```

Create `StaticCredentialStore.swift`:

```swift
import Foundation

/// A non-Keychain `CredentialStore` over fixed credentials — used to probe unsaved
/// Settings values before Save, and to inject creds into a `ServerClient`.
public struct StaticCredentialStore: CredentialStore {
    private let credentials: BasicCredentials?
    public init(_ credentials: BasicCredentials?) { self.credentials = credentials }
    public func basicCredentials() async throws -> BasicCredentials? { credentials }
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter ShellStoresTests`
Expected: PASS (3 tests), no warnings.

- [x] **Step 5: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/ServerURLStore.swift apple/FilesNestCore/Sources/FilesNestCore/StaticCredentialStore.swift apple/FilesNestCore/Tests/FilesNestCoreTests/ShellStoresTests.swift
git commit -m "feat: ServerURLStore + StaticCredentialStore"
```

---

### Task 3: Connection probe (`ConnectionProbe`, `ConnectionResult`)

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/ConnectionProbe.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/ConnectionProbeTests.swift`

**Interfaces:**
- Consumes: `ServerClient`, `ServerClientError.unauthorized`, `StaticCredentialStore`, `BasicCredentials`.
- Produces: `enum ConnectionResult: Sendable, Equatable { case ok; case unauthorized; case unreachable(String) }`; `struct ConnectionProbe(session:)` with `func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult`.

- [x] **Step 1: Write the failing test**

Create `ConnectionProbeTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite(.serialized)
struct ConnectionProbeTests {
    let host = "probe.test"
    var baseURL: URL { URL(string: "https://\(host)")! }
    let creds = BasicCredentials(username: "u", password: "p")

    @Test func reachableAndAuthedIsOk() async {
        MockURLProtocol.setHandler(forHost: host) { req in
            MockURLProtocol.respond(status: 200,
                body: #"{"items":[],"next_cursor":""}"#.data(using: .utf8)!, for: req.url!)
        }
        defer { MockURLProtocol.removeHandler(forHost: host) }
        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        #expect(await probe.probe(baseURL: baseURL, credentials: creds) == .ok)
    }

    @Test func rejectedCredsIsUnauthorized() async {
        MockURLProtocol.setHandler(forHost: host) { req in
            MockURLProtocol.respond(status: 401, body: Data(), for: req.url!)
        }
        defer { MockURLProtocol.removeHandler(forHost: host) }
        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        #expect(await probe.probe(baseURL: baseURL, credentials: creds) == .unauthorized)
    }

    @Test func transportFailureIsUnreachable() async {
        MockURLProtocol.setHandler(forHost: host) { _ in throw URLError(.cannotConnectToHost) }
        defer { MockURLProtocol.removeHandler(forHost: host) }
        let probe = ConnectionProbe(session: MockURLProtocol.makeSession())
        if case .unreachable = await probe.probe(baseURL: baseURL, credentials: creds) {} else {
            Issue.record("expected .unreachable")
        }
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter ConnectionProbeTests`
Expected: FAIL — `ConnectionProbe` / `ConnectionResult` undefined.

- [x] **Step 3: Write minimal implementation**

Create `ConnectionProbe.swift`:

```swift
import Foundation

public enum ConnectionResult: Sendable, Equatable {
    case ok                    // reachable and authenticated
    case unauthorized          // reached server, credentials rejected (401)
    case unreachable(String)   // network/DNS/TLS/other, with a display message
}

/// Verifies a server URL + credentials by issuing an authenticated `GET /uploads`
/// (`listUploads`) — no dedicated health endpoint needed.
public struct ConnectionProbe: Sendable {
    private let session: URLSession
    public init(session: URLSession = .shared) { self.session = session }

    public func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult {
        let client = ServerClient(baseURL: baseURL,
                                  credentials: StaticCredentialStore(credentials),
                                  session: session)
        do {
            _ = try await client.listUploads(cursor: nil)
            return .ok
        } catch ServerClientError.unauthorized {
            return .unauthorized
        } catch let error as URLError {
            return .unreachable(error.localizedDescription)
        } catch {
            return .unreachable(String(describing: error))
        }
    }
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter ConnectionProbeTests`
Expected: PASS (3 tests), no warnings.

- [x] **Step 5: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/ConnectionProbe.swift apple/FilesNestCore/Tests/FilesNestCoreTests/ConnectionProbeTests.swift
git commit -m "feat: ConnectionProbe for Test Connection"
```

---

### Task 4: Sync engine seam (`SyncEngine`, `StubSyncEngine`)

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift`

**Interfaces:**
- Consumes: `SyncStatus`, `SyncProgress`, `CredentialStore`, `StaticCredentialStore`, `BasicCredentials`.
- Produces:
  - `protocol SyncEngine: Sendable { func statusStream() -> AsyncStream<SyncStatus>; func start() async; func pause() async; func resume() async; func syncNow() async }`.
  - `final class StubSyncEngine(credentials:autoComplete:now:)` conforming to it. `start()` reconciles signed-in/out from `credentials`; `pause`→`.paused`; `resume`→`.watching`; `syncNow`→`.syncing(...)` (auto-advancing to `.watching` when `autoComplete`). No-ops while `.signedOut`.

- [x] **Step 1: Write the failing test**

Create `StubSyncEngineTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct StubSyncEngineTests {
    func firstStatus(_ engine: any SyncEngine) async -> SyncStatus {
        var it = engine.statusStream().makeAsyncIterator()
        return await it.next()!
    }

    @Test func startWithoutCredentialsIsSignedOut() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(nil))
        await engine.start()
        #expect(await firstStatus(engine) == .signedOut)
    }

    @Test func startWithCredentialsIsWatching() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.start()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }

    @Test func pauseThenResume() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")))
        await engine.start()
        await engine.pause()
        if case .paused = await firstStatus(engine) {} else { Issue.record("expected .paused") }
        await engine.resume()
        #expect(await firstStatus(engine) == .watching(lastSync: nil))
    }

    @Test func syncNowEntersSyncingThenWatching() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(.init(username: "u", password: "p")),
                                    autoComplete: false)
        await engine.start()
        await engine.syncNow()
        if case .syncing = await firstStatus(engine) {} else { Issue.record("expected .syncing") }
    }

    @Test func syncNowIgnoredWhenSignedOut() async {
        let engine = StubSyncEngine(credentials: StaticCredentialStore(nil))
        await engine.start()
        await engine.syncNow()
        #expect(await firstStatus(engine) == .signedOut)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter StubSyncEngineTests`
Expected: FAIL — `SyncEngine` / `StubSyncEngine` undefined.

- [x] **Step 3: Write minimal implementation**

Create `SyncEngine.swift`:

```swift
import Foundation

/// The seam the panel observes and the sync engine implements. This slice ships
/// `StubSyncEngine`; the next slice replaces it with the PhotoKit-backed engine.
public protocol SyncEngine: Sendable {
    /// The current status followed by every change. Each call returns an
    /// independent stream whose first element is the current status.
    func statusStream() -> AsyncStream<SyncStatus>
    func start() async     // reconcile signed-in/out from credentials, begin watching
    func pause() async
    func resume() async
    func syncNow() async   // manual trigger
}
```

Create `StubSyncEngine.swift`:

```swift
import Foundation

/// In-memory stand-in that drives the UI through every `SyncStatus` without a
/// backend. `start()` reads `credentials` to decide signed-in vs signed-out.
public final class StubSyncEngine: SyncEngine, @unchecked Sendable {
    private let credentials: any CredentialStore
    private let autoComplete: Bool
    private let now: @Sendable () -> Date

    private let lock = NSLock()
    private var status: SyncStatus = .signedOut
    private var continuations: [UUID: AsyncStream<SyncStatus>.Continuation] = [:]

    public init(credentials: any CredentialStore = StaticCredentialStore(nil),
                autoComplete: Bool = true,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.credentials = credentials
        self.autoComplete = autoComplete
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
                self?.lock.lock(); self?.continuations[id] = nil; self?.lock.unlock()
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

    public func start() async {
        let creds = try? await credentials.basicCredentials()
        set(creds == nil ? .signedOut : .watching(lastSync: nil))
    }

    public func pause() async {
        guard !isSignedOut else { return }
        set(.paused(pending: 3))
    }

    public func resume() async {
        guard !isSignedOut else { return }
        set(.watching(lastSync: now()))
    }

    public func syncNow() async {
        guard !isSignedOut else { return }
        let total = 12
        set(.syncing(SyncProgress(completed: 0, total: total,
                                  currentItemName: "IMG_2043.HEIC", bytesRemaining: 210_000_000)))
        guard autoComplete else { return }
        for i in 1...total {
            try? await Task.sleep(nanoseconds: 250_000_000)
            set(.syncing(SyncProgress(completed: i, total: total,
                                      currentItemName: "IMG_\(2043 + i).HEIC",
                                      bytesRemaining: Int64((total - i)) * 17_000_000)))
        }
        set(.watching(lastSync: now()))
    }
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter StubSyncEngineTests`
Expected: PASS (5 tests), no warnings.

- [x] **Step 5: Run the full Core suite**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS (all suites); confirm zero warnings with `swift build --build-tests 2>&1 | grep -i warning` (no output).

- [x] **Step 6: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/SyncEngine.swift apple/FilesNestCore/Sources/FilesNestCore/StubSyncEngine.swift apple/FilesNestCore/Tests/FilesNestCoreTests/StubSyncEngineTests.swift
git commit -m "feat: SyncEngine seam + StubSyncEngine"
```

---

### Task 5: Menu-bar agent + composition root + AppModel

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` (replace `WindowGroup`)
- Create: `apple/macos/FilesNest/FilesNest/AppModel.swift`

**Interfaces:**
- Consumes: `StubSyncEngine`, `KeychainStore`, `UserDefaultsServerURLStore`, `ConnectionProbe`, `SyncStatus`.
- Produces: `@MainActor final class AppModel: ObservableObject` with `@Published private(set) var status: SyncStatus`, `func begin()`, `pause()`, `resume()`, `syncNow()`, and a `restart()` used after Settings save. `PanelView`/`SettingsView` are added in Tasks 6–7; this task uses a temporary inline placeholder body so the app compiles.

- [x] **Step 1: Write AppModel**

Create `AppModel.swift`:

```swift
import SwiftUI
import FilesNestCore

@MainActor
final class AppModel: ObservableObject {
    @Published private(set) var status: SyncStatus = .signedOut
    let engine: any SyncEngine
    private var streamTask: Task<Void, Never>?

    init(engine: any SyncEngine) { self.engine = engine }

    /// Subscribe to the engine and start it. Idempotent.
    func begin() {
        guard streamTask == nil else { return }
        streamTask = Task { [engine] in
            for await s in engine.statusStream() { self.status = s }
        }
        Task { await engine.start() }
    }

    /// Re-reconcile after credentials change (Settings save).
    func restart() { Task { await engine.start() } }

    func pause()   { Task { await engine.pause() } }
    func resume()  { Task { await engine.resume() } }
    func syncNow() { Task { await engine.syncNow() } }
}
```

- [x] **Step 2: Rewrite the app entry point**

Replace the entire contents of `FilesNestApp.swift`:

```swift
import SwiftUI
import FilesNestCore

@main
struct FilesNestApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model: AppModel

    init() {
        // Composition root: build the object graph once.
        let engine = StubSyncEngine(credentials: KeychainStore())
        _model = StateObject(wrappedValue: AppModel(engine: engine))
    }

    var body: some Scene {
        MenuBarExtra("FilesNest", systemImage: "arrow.triangle.2.circlepath") {
            // Placeholder until Task 6 adds PanelView.
            VStack(alignment: .leading, spacing: 6) {
                Text("FilesNest").font(.headline)
                Text(String(describing: model.status)).font(.caption).foregroundStyle(.secondary)
                Button("Quit") { NSApp.terminate(nil) }
            }
            .padding(12)
            .frame(width: 320)
            .task { model.begin() }
        }
        .menuBarExtraStyle(.window)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Menu-bar agent: no Dock icon, no main window.
        NSApp.setActivationPolicy(.accessory)
    }
}
```

- [x] **Step 3: Compile the app target**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO build 2>&1 | tail -5`
Expected: `** BUILD SUCCEEDED **`.

- [x] **Step 4: Manual verification**

Launch the built app (or run from Xcode). Verify:
- A menu-bar icon appears; **no Dock icon** and no window on launch.
- Clicking the icon opens a small panel showing "FilesNest" and a status line (`signedOut`, since no creds yet).
- Quit works.

- [x] **Step 5: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/FilesNestApp.swift apple/macos/FilesNest/FilesNest/AppModel.swift
git commit -m "feat: menu-bar agent scaffold + AppModel + composition root"
```

---

### Task 6: PanelView (Variant B)

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/PanelView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` (swap placeholder for `PanelView`)

**Interfaces:**
- Consumes: `AppModel`, `SyncStatus`, `SyncProgress`. Uses SwiftUI `@EnvironmentObject`/parameter injection of `AppModel`. Opens Settings via `SettingsLink` (added when the Settings scene lands in Task 7 — until then the footer button is present but the Settings scene is wired in Task 7).

- [x] **Step 1: Write PanelView**

Create `PanelView.swift` (renders Variant B from `docs/design/mockups/20260726-menubar-panel.html`):

```swift
import SwiftUI
import FilesNestCore

struct PanelView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(spacing: 0) {
            hero
            if case let .syncing(p) = model.status { currentItem(p) }
            tiles
            actions
            Divider()
            footer
        }
        .frame(width: 320)
    }

    // MARK: hero ring + status text
    private var hero: some View {
        VStack(spacing: 8) {
            ZStack {
                Circle().stroke(.quaternary, lineWidth: 6).frame(width: 74, height: 74)
                Circle().trim(from: 0, to: ringFraction)
                    .stroke(ringColor, style: .init(lineWidth: 6, lineCap: .round))
                    .rotationEffect(.degrees(-90)).frame(width: 74, height: 74)
                Text(glyph).font(.system(size: 24))
            }
            Text(title).font(.headline)
            Text(subtitle).font(.caption).foregroundStyle(.secondary)
        }
        .padding(.top, 18).padding(.bottom, 10).frame(maxWidth: .infinity)
    }

    private func currentItem(_ p: SyncProgress) -> some View {
        HStack(spacing: 10) {
            RoundedRectangle(cornerRadius: 6)
                .fill(LinearGradient(colors: [.blue.opacity(0.5), .purple.opacity(0.5)],
                                     startPoint: .topLeading, endPoint: .bottomTrailing))
                .frame(width: 34, height: 34)
            VStack(alignment: .leading, spacing: 2) {
                Text(p.currentItemName ?? "…").font(.caption).bold().lineLimit(1)
                Text("Uploading · \(p.completed) of \(p.total)")
                    .font(.caption2).foregroundStyle(.secondary)
                ProgressView(value: p.fraction).controlSize(.mini)
            }
        }
        .padding(8).background(.quaternary, in: RoundedRectangle(cornerRadius: 10))
        .padding(.horizontal, 12).padding(.bottom, 8)
    }

    private var tiles: some View {
        HStack(spacing: 8) {
            tile("1,240", "Backed up", .primary)
            tile("\(pending)", "Pending", pending > 0 ? .orange : .primary)
            tile("0", "Failed", .primary)
        }.padding(.horizontal, 12).padding(.bottom, 8)
    }

    private func tile(_ v: String, _ k: String, _ color: Color) -> some View {
        VStack(spacing: 1) {
            Text(v).font(.title3).bold().foregroundStyle(color)
            Text(k).font(.caption2).foregroundStyle(.secondary)
        }.frame(maxWidth: .infinity).padding(.vertical, 9)
        .background(.quaternary, in: RoundedRectangle(cornerRadius: 9))
    }

    private var actions: some View {
        HStack(spacing: 8) {
            Button(isPaused ? "Resume" : "Pause") { isPaused ? model.resume() : model.pause() }
                .disabled(isSignedOut)
            Button("Sync Now") { model.syncNow() }.buttonStyle(.borderedProminent)
                .disabled(isSignedOut)
        }.padding(.horizontal, 12).padding(.bottom, 4)
    }

    private var footer: some View {
        HStack {
            SettingsLink { Text("Settings…") }
            Spacer()
            Button("Quit") { NSApp.terminate(nil) }
        }
        .buttonStyle(.link).font(.caption)
        .padding(.horizontal, 12).padding(.vertical, 9).background(.quaternary)
    }

    // MARK: derived
    private var isPaused: Bool { if case .paused = model.status { return true }; return false }
    private var isSignedOut: Bool { if case .signedOut = model.status { return true }; return false }
    private var pending: Int { if case let .paused(p) = model.status { return p }; return 3 }

    private var ringFraction: CGFloat {
        switch model.status {
        case .syncing(let p): return CGFloat(p.fraction)
        case .watching: return 1
        default: return 0
        }
    }
    private var ringColor: Color {
        switch model.status {
        case .syncing: return .blue
        case .paused: return .orange
        case .error: return .red
        default: return .green
        }
    }
    private var glyph: String {
        switch model.status {
        case .syncing: return ""
        case .paused: return "⏸"
        case .error: return "✕"
        case .signedOut: return "→"
        case .watching: return "✓"
        }
    }
    private var title: String {
        switch model.status {
        case .signedOut: return "Sign in in Settings"
        case .watching: return "Up to date"
        case .syncing: return "Syncing…"
        case .paused: return "Paused"
        case .error: return "Can't reach server"
        }
    }
    private var subtitle: String {
        switch model.status {
        case .signedOut: return "Add your server and credentials"
        case .watching(let last): return last.map { "Last sync \($0.formatted(.relative(presentation: .named)))" } ?? "Watching for new items"
        case .syncing(let p): return "\(p.completed) of \(p.total)"
        case .paused(let n): return "\(n) items waiting"
        case .error(let m): return m
        }
    }
}
```

- [x] **Step 2: Swap the placeholder for PanelView**

In `FilesNestApp.swift`, replace the placeholder `VStack { … }` inside `MenuBarExtra` with:

```swift
            PanelView(model: model).task { model.begin() }
```

- [x] **Step 3: Compile the app target**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO build 2>&1 | tail -5`
Expected: `** BUILD SUCCEEDED **`.

- [x] **Step 4: Manual verification**

Launch. Because there are no credentials yet, the panel shows the **signed-out** hero ("Sign in in Settings", Pause/Sync disabled). To see other states before Settings exists, temporarily construct the engine with creds (`StubSyncEngine(credentials: StaticCredentialStore(.init(username:"u",password:"p")))`) and confirm: green ✓ up-to-date; **Sync Now** animates the ring + shows the current-item strip; **Pause** → amber ⏸; **Resume** → green. Revert the temporary change. Compare layout against the mockup.

- [x] **Step 5: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/PanelView.swift apple/macos/FilesNest/FilesNest/FilesNestApp.swift
git commit -m "feat: PanelView (Variant B) bound to AppModel"
```

---

### Task 7: SettingsView + SettingsModel + Settings scene

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/SettingsModel.swift`
- Create: `apple/macos/FilesNest/FilesNest/SettingsView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` (add `Settings` scene + inject `SettingsModel`; call `model.restart()` on save)

**Interfaces:**
- Consumes: `UserDefaultsServerURLStore`, `KeychainStore`, `ConnectionProbe`, `ConnectionResult`, `BasicCredentials`, `AppModel`.
- Produces: `@MainActor final class SettingsModel: ObservableObject` (`serverURL`, `username`, `password`, `testResult`, `isTesting`, `load()`, `test()`, `save()`, `onSaved`); `SettingsView`.

- [x] **Step 1: Write SettingsModel**

Create `SettingsModel.swift`:

```swift
import SwiftUI
import FilesNestCore

@MainActor
final class SettingsModel: ObservableObject {
    @Published var serverURL = ""
    @Published var username = ""
    @Published var password = ""
    @Published var testResult: ConnectionResult?
    @Published var isTesting = false

    private let urlStore: any ServerURLStore
    private let credStore: KeychainStore
    private let probe: ConnectionProbe
    var onSaved: (() -> Void)?

    init(urlStore: any ServerURLStore, credStore: KeychainStore, probe: ConnectionProbe) {
        self.urlStore = urlStore
        self.credStore = credStore
        self.probe = probe
    }

    var hasCredentials: Bool { !username.isEmpty && !password.isEmpty }

    func load() async {
        if let u = urlStore.load() { serverURL = u.absoluteString }
        if let c = try? await credStore.basicCredentials() {
            username = c.username; password = c.password
        }
    }

    func test() async {
        guard let url = URL(string: serverURL), !serverURL.isEmpty else {
            testResult = .unreachable("Invalid URL"); return
        }
        isTesting = true; defer { isTesting = false }
        testResult = await probe.probe(baseURL: url,
                                       credentials: .init(username: username, password: password))
    }

    func save() {
        guard let url = URL(string: serverURL), !serverURL.isEmpty else { return }
        try? credStore.save(.init(username: username, password: password))
        urlStore.save(url)
        onSaved?()
    }
}
```

- [x] **Step 2: Write SettingsView**

Create `SettingsView.swift`:

```swift
import SwiftUI
import FilesNestCore
import ServiceManagement

struct SettingsView: View {
    @ObservedObject var model: SettingsModel
    @Environment(\.dismiss) private var dismiss
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("FilesNest Settings").font(.title3).bold()

            Form {
                TextField("Server URL", text: $model.serverURL)
                    .textContentType(.URL).autocorrectionDisabled()
                TextField("Username", text: $model.username).autocorrectionDisabled()
                SecureField("Password", text: $model.password)
            }

            HStack(spacing: 10) {
                Button("Test Connection") { Task { await model.test() } }
                    .disabled(model.isTesting || !model.hasCredentials)
                if model.isTesting { ProgressView().controlSize(.small) }
                testPill
            }

            Toggle("Launch at login", isOn: $launchAtLogin)
                .onChange(of: launchAtLogin) { _, on in
                    try? on ? SMAppService.mainApp.register() : SMAppService.mainApp.unregister()
                }

            Divider()
            HStack {
                Spacer()
                Button("Save") { model.save(); dismiss() }
                    .buttonStyle(.borderedProminent).disabled(!model.hasCredentials)
            }
        }
        .padding(20).frame(width: 360)
        .task { await model.load() }
    }

    @ViewBuilder private var testPill: some View {
        switch model.testResult {
        case .ok: Label("Connected", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
        case .unauthorized: Label("401 Unauthorized", systemImage: "xmark.circle.fill").foregroundStyle(.red)
        case .unreachable(let m): Label(m, systemImage: "xmark.circle.fill").foregroundStyle(.red).lineLimit(1)
        case nil: EmptyView()
        }
    }
}
```

- [x] **Step 3: Wire the Settings scene**

In `FilesNestApp.swift`: build a `SettingsModel` in `init()` alongside `AppModel`, set its `onSaved` to call `model.restart()`, hold it in a `@StateObject`, and add a `Settings` scene. Updated `FilesNestApp.swift`:

```swift
import SwiftUI
import FilesNestCore

@main
struct FilesNestApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model: AppModel
    @StateObject private var settings: SettingsModel

    init() {
        let defaults = UserDefaults.standard
        let engine = StubSyncEngine(credentials: KeychainStore())
        let appModel = AppModel(engine: engine)
        let settingsModel = SettingsModel(urlStore: UserDefaultsServerURLStore(defaults: defaults),
                                          credStore: KeychainStore(),
                                          probe: ConnectionProbe())
        settingsModel.onSaved = { appModel.restart() }
        _model = StateObject(wrappedValue: appModel)
        _settings = StateObject(wrappedValue: settingsModel)
    }

    var body: some Scene {
        MenuBarExtra("FilesNest", systemImage: "arrow.triangle.2.circlepath") {
            PanelView(model: model).task { model.begin() }
        }
        .menuBarExtraStyle(.window)

        Settings { SettingsView(model: settings) }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
    }
}
```

- [x] **Step 4: Compile the app target**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO build 2>&1 | tail -5`
Expected: `** BUILD SUCCEEDED **`.

- [x] **Step 5: Manual verification**

Launch. Open Settings from the panel footer (or ⌘,):
- Enter server URL + username + password. **Test Connection** shows green "Connected" against a reachable+authed server, red "401 Unauthorized" against bad creds, red reason against an unreachable host.
- **Save** → panel leaves signed-out and shows "Up to date" (engine `restart()` picked up the creds via Keychain). Relaunch the app → still signed in (creds persisted, prefilled on reopen).
- Toggle **Launch at login** → confirm the login item appears/disappears (System Settings › General › Login Items).

- [x] **Step 6: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/SettingsModel.swift apple/macos/FilesNest/FilesNest/SettingsView.swift apple/macos/FilesNest/FilesNest/FilesNestApp.swift
git commit -m "feat: SettingsView + SettingsModel + Settings scene"
```

---

### Task 8: Final verification + manual checklist

**Files:**
- Create: `docs/plans/20260726-menubar-shell-verification.md`

- [x] **Step 1: Full Core suite + zero-warning check**

Run: `cd apple/FilesNestCore && swift test 2>&1 | grep -E "Test run with"`
Then: `swift build --build-tests 2>&1 | grep -i warning` (expect no output).
Expected: all tests pass; no warnings.

- [x] **Step 2: App build**

Run: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO build 2>&1 | tail -3`
Expected: `** BUILD SUCCEEDED **`.

- [x] **Step 3: Write the manual-verification checklist**

Create `docs/plans/20260726-menubar-shell-verification.md` capturing the manual steps from Tasks 5–7 as a runnable checklist (menu-bar agent / no Dock icon; panel states; Settings Test Connection outcomes; Save persistence across relaunch; Launch-at-login), for use whenever the shell changes.

- [x] **Step 4: Commit**

```bash
git add docs/plans/20260726-menubar-shell-verification.md
git commit -m "docs: menu-bar shell manual-verification checklist"
```

---

## Definition of Done

- App launches as a menu-bar agent (no Dock icon); `MenuBarExtra(.window)` panel (Variant B) renders all five `SyncStatus` states from `StubSyncEngine`.
- Settings persists credentials to `KeychainStore` and the URL to `ServerURLStore`; Test Connection reports ok / unauthorized / unreachable; Launch-at-login works; signed-in state survives relaunch.
- Core seams (`SyncStatus`, `SyncEngine`/`StubSyncEngine`, `ConnectionProbe`, `ServerURLStore`, `StaticCredentialStore`) implemented; `swift test` green; zero Swift 6 warnings.
- App target compiles via `xcodebuild`; manual-verification checklist documented and run.
- No PhotoKit / real sync logic — `StubSyncEngine` is the only stand-in.
- Open PR titled `Apple clients: MenuBar shell + Settings (#6)`.
