# Design: macOS Menu-Bar Shell + Settings

**Date:** 2026-07-26
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (new UI-agnostic seams + fakes) and `apple/macos/FilesNest` (SwiftUI app target — menu-bar agent, panel, settings)
**Depends on:**
- `docs/design/20260726-keychainstore.md` (`KeychainStore`, `CredentialStore`, `BasicCredentials`, merged `5b0b913`) — credential persistence.
- `docs/design/20260723-serverclient.md` (`ServerClient`, `listUploads`, merged `9eb5feb`) — reachability/auth probe.
- `docs/design/20260726-synccoordinator.md` (`SyncCoordinator`, `SyncRange`, merged `011053d`) — the engine the *next* slice will wire behind the seam defined here.
- `docs/architecture.md` — "Mac app" (menu-bar utility, credentials in Keychain).

---

## 1. Purpose

Turn the tested `FilesNestCore` package into a runnable macOS product: a menu-bar
agent whose icon opens a panel showing backup status, and a Settings window where
the user enters the server URL and Basic Auth credentials (persisted via
`KeychainStore`) and verifies them with a "Test Connection" probe.

This slice builds **the frame the always-on sync engine plugs into**. The real
continuous engine (watch Photos → debounce → incremental sync) depends on the
still-unbuilt PhotoKit `AssetLibrary` adapter, so it is deferred to the next
slice. Here we define a `SyncEngine` **seam** and ship a `StubSyncEngine` that
drives the UI through every state. The panel and settings become fully wired and
launchable now, with the engine a drop-in replacement later.

## 2. Scope

**In:**
- App becomes a menu-bar agent (accessory activation policy — no Dock icon, no
  main window). `MenuBarExtra` with `.menuBarExtraStyle(.window)` hosting the panel.
- Panel UI = approved "Variant B": status hero with a progress ring, a
  current-item strip while syncing, three stat tiles, Pause/Resume + Sync Now, and
  a Settings/Quit footer.
- Settings window: server URL, username, password → `KeychainStore`; server URL →
  a plain settings store; "Test Connection"; "Launch at login" toggle.
- UI-agnostic seams in Core (`SyncStatus`, `SyncProgress`, `SyncEngine`,
  `StubSyncEngine`, `ConnectionProbe`, `ServerURLStore`, `StaticCredentialStore`),
  all unit-tested.

**Out (future slices):**
- The PhotoKit `AssetLibrary` adapter and the real continuous `SyncEngine`
  (change observer + debounce + incremental range) — next slice.
- A browseable **Library/History list** of backed-up/pending items — its own slice;
  the panel footer will gain a "Recent activity…" entry point when built.
- Notifications, auto-update, onboarding beyond first-launch Settings.

## 3. Architecture

Two layers, matching the repo's tested-core / untestable-shell split:

- **`FilesNestCore` (pure, `swift test`-covered):** value/model types and protocol
  seams with no SwiftUI. Everything with logic lives here so it is tested headless.
- **`apple/macos/FilesNest` (SwiftUI, manual-verify):** the `MenuBarExtra` scene,
  panel and settings views, a `@MainActor` observable view-model that adapts the
  engine to the views, and the composition root. No business logic beyond binding.

```
MenuBarExtra (.window)                 [app target]
  └─ PanelView  ← observes ─ AppModel (@MainActor, @Observable)   [app target]
Settings scene (⌘,)                                                [app target]
  └─ SettingsView ← binds ─ SettingsModel (@MainActor)            [app target]
AppModel / SettingsModel depend on Core seams:                    [FilesNestCore]
  SyncEngine (StubSyncEngine now)   ServerURLStore
  ConnectionProbe                   KeychainStore / StaticCredentialStore
```

## 4. Core seams (all `Sendable`, unit-tested)

### 4.1 Status model

```swift
public struct SyncProgress: Sendable, Equatable {
    public let completed: Int          // items done this run
    public let total: Int              // items in this run
    public let currentItemName: String?
    public let bytesRemaining: Int64?
    public init(completed: Int, total: Int, currentItemName: String?, bytesRemaining: Int64?)
    /// 0.0…1.0, 0 when total == 0 (drives the ring).
    public var fraction: Double { total > 0 ? Double(completed) / Double(total) : 0 }
}

public enum SyncStatus: Sendable, Equatable {
    case signedOut                       // no credentials → "Sign in in Settings"
    case watching(lastSync: Date?)       // idle, monitoring for new items
    case syncing(SyncProgress)
    case paused(pending: Int)
    case error(message: String)          // e.g. "Can't reach server"
}
```

### 4.2 Engine seam + stub

```swift
public protocol SyncEngine: Sendable {
    /// Current status, then every subsequent change. Multiple consumers each get
    /// their own stream. First element is the current status (never awaits a change).
    func statusStream() -> AsyncStream<SyncStatus>
    func start() async          // begin watching (no-op if signed out)
    func pause() async
    func resume() async
    func syncNow() async        // manual trigger
}

/// Scripted engine for this slice: control methods mutate an in-memory status and
/// publish it to all live streams. Optionally replays a demo sequence so the panel
/// can be exercised through syncing/paused/error without a backend.
public final class StubSyncEngine: SyncEngine, @unchecked Sendable { … }
```

`StubSyncEngine` is state-machine simple: `syncNow()` → emits a short
`syncing(progress…)` sequence then `watching(lastSync:)`; `pause()` → `paused`;
`resume()` → `watching`; constructed `signedOut` when no creds. It exists so the UI
is real and demoable now, and so status→view mapping is testable.

### 4.3 Connection probe

```swift
public enum ConnectionResult: Sendable, Equatable {
    case ok                     // reachable and authenticated
    case unauthorized           // reached server, creds rejected (401)
    case unreachable(String)    // network/DNS/TLS/other, with a display message
}

public struct ConnectionProbe: Sendable {
    public init(session: URLSession = .shared)
    /// Builds a ServerClient for the given URL+creds and calls listUploads(cursor: nil).
    /// Maps success → .ok, ServerClientError.unauthorized → .unauthorized,
    /// anything else → .unreachable(message).
    public func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult
}
```

Uses the existing `listUploads` as an authenticated GET — no new server endpoint.
Tested with the existing `MockURLProtocol` (200 → `.ok`, 401 → `.unauthorized`,
transport error → `.unreachable`).

### 4.4 Server-URL persistence + ephemeral creds

Server URL is configuration, not a secret — stored like `SyncStateStore`:

```swift
public protocol ServerURLStore: Sendable {
    func load() -> URL?
    func save(_ url: URL)
}
public final class UserDefaultsServerURLStore: ServerURLStore, @unchecked Sendable {
    public init(defaults: UserDefaults)   // inject a suite in tests
}   // key "com.filesnest.serverURL"

/// A non-Keychain CredentialStore over fixed creds — used to probe unsaved form
/// values before Save, and anywhere a static credential is convenient.
public struct StaticCredentialStore: CredentialStore {
    public init(_ credentials: BasicCredentials?)
    public func basicCredentials() async throws -> BasicCredentials? { credentials }
}
```

## 5. UI (app target — manual verification)

### 5.1 Menu-bar agent

- `Info.plist` `LSUIElement = true` (or `NSApp.setActivationPolicy(.accessory)` at
  launch) — no Dock icon, background-style agent.
- Scene: `MenuBarExtra("FilesNest", systemImage: …) { PanelView() }
  .menuBarExtraStyle(.window)`, plus a `Settings { SettingsView() }` scene for ⌘,.
- Menu-bar icon reflects status (e.g. tinted/animated glyph for syncing vs error).

### 5.2 PanelView (Variant B)

Approved mockup: `docs/design/mockups/20260726-menubar-panel.html` (toolbar flips
state and light/dark). Renders from `AppModel.status`:
- **Hero ring** — `fraction`-filled while syncing (blue), full green when
  `watching`/up-to-date, amber when `paused`; glyph ✓ / ⏸ / ✕ by state.
- **Current-item strip** — visible only in `syncing`: thumbnail placeholder,
  `currentItemName`, "completed of total", per-file bar, size.
- **Three tiles** — Backed up / Pending / Failed (counts from the engine; Backed-up
  is a lifetime figure the engine reports, Pending/Failed from the current plan/run).
- **Actions** — Pause/Resume (toggles by state), Sync Now (calls `syncNow`).
- **Footer** — "Settings… ⌘," opens the Settings scene; "Quit ⌘Q".
- **signedOut** collapses the hero to a "Sign in in Settings" call-to-action;
  **error** shows the message with a Retry that calls `syncNow`.

### 5.3 SettingsView

- Fields: Server URL, Username, Password (`SecureField`), prefilled from
  `ServerURLStore` + `KeychainStore` on open.
- **Test Connection** → `ConnectionProbe.probe(baseURL:credentials:)` on the typed
  values; inline pill: green "Connected · 200 OK" / red "401 Unauthorized" / red
  "Can't reach server — <reason>".
- **Save** → validate URL, `KeychainStore.save(creds)`, `ServerURLStore.save(url)`,
  tell `AppModel` to (re)start the engine; dismiss.
- **Launch at login** → `SMAppService.mainApp` register/unregister.
- First launch (no creds) opens Settings automatically; panel shows `signedOut`.

### 5.4 AppModel / SettingsModel

`@MainActor @Observable` classes in the app target. `AppModel` subscribes to
`engine.statusStream()` in a `Task`, republishing `status` for the panel, and
forwards Pause/Resume/Sync-Now/Quit. Thin enough that logic lives in the tested
Core seams, not here.

## 6. Composition root

A single place (app `init`) builds the object graph:
`UserDefaults.standard` → `UserDefaultsServerURLStore`; `KeychainStore()`;
`ServerClient(baseURL:credentials:)` when a URL exists; **`StubSyncEngine`** for
now. The next slice swaps `StubSyncEngine` for the PhotoKit-backed engine with no
UI change.

## 7. Testing strategy

- **Core (headless `swift test`):**
  - `SyncProgress.fraction` (incl. `total == 0` → 0); `SyncStatus` equality.
  - `StubSyncEngine`: `pause`→`paused`, `resume`→`watching`, `syncNow` emits a
    `syncing` sequence ending in `watching`; a fresh `statusStream()` first yields
    the current status; signed-out construction stays `signedOut` until creds set.
  - `ConnectionProbe` via `MockURLProtocol`: 200→`.ok`, 401→`.unauthorized`,
    transport error→`.unreachable(_)`.
  - `UserDefaultsServerURLStore` round-trip with an injected suite; `nil` when unset
    and when a stored string isn't a valid URL.
  - `StaticCredentialStore` returns its value / `nil`.
- **App target (manual verification, documented steps):** icon appears with no Dock
  icon; panel opens from the menu bar; first launch opens Settings; Test Connection
  shows each pill; Save persists (relaunch keeps you signed in); Pause/Resume/Sync
  Now drive the stub through visible states; Launch-at-login toggles the login item.

## 8. Definition of done

- App launches as a menu-bar agent; `MenuBarExtra` window panel (Variant B) renders
  all five `SyncStatus` states from the stub.
- Settings persists creds to `KeychainStore` and URL to `ServerURLStore`; Test
  Connection reports ok / unauthorized / unreachable; Launch-at-login works.
- Core seams implemented and `swift test` green (§7); zero Swift 6 warnings.
- `StubSyncEngine` is the only stand-in; no PhotoKit/sync logic in this slice.
- Manual-verification checklist documented and run.

## 9. Out of scope (future slices, each its own spec)

- PhotoKit `AssetLibrary` adapter + real continuous `SyncEngine` (change observer,
  debounce, incremental range, `backendLost` recovery) — **next**.
- Library/History list view + its "Recent activity…" panel entry point.
- iOS app shell; notifications; auto-update; richer onboarding.

## 10. Open items

1. **Backed-up lifetime count source.** Whether the "Backed up" tile reads a running
   total the engine maintains or is derived from a `listUploads` count — resolved
   when the real engine lands; the stub supplies a fixed number.
2. **Menu-bar icon states.** Exact SF Symbol / tint per status — finalized during UI
   implementation against the mockup.
