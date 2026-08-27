# Settings Window + Sync Destination Choice

## Overview

Move FilesNest's macOS Settings out of the menu-bar popover panel into a real,
standalone `Settings {}` window, and add a (mostly future-facing) "Sync to:
FilesNest Server | Local Folder" destination picker so a later ticket can add local
folder sync without restructuring Settings again. Replaces the current
Test-Connection-then-Save two-step with a single "Connect" action that verifies
before it ever persists, and introduces a destination-readiness gate so the app
never attempts to sync to an unconfigured destination — surfaced as a clear message
in the existing menu-bar panel.

Full design record: `docs/design/20260826-settings-window-and-destination.md`
(original proposal) plus the brainstormed refinements this plan implements
(Connect-not-Save, the readiness gate, destination-aware panel messaging).

## Context (from discovery)

- `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` — composition root; builds
  `urlStore`/`credStore`/`stateStore`, the `LiveSyncEngine` closures
  (`perform`/`resume`/`assess`), and the `MenuBarExtra` scene. This is where the new
  `Window` anchor scene, `Settings {}` scene, and `SyncDestinationStore` get wired in.
- `apple/macos/FilesNest/FilesNest/PanelView.swift` — owns `showingSettings` state,
  the in-panel slide-swap to `SettingsView`, and the hardcoded `.signedOut` subtitle
  ("Connect your own FilesNest server to begin").
- `apple/macos/FilesNest/FilesNest/SettingsView.swift` / `SettingsModel.swift` —
  today: Server URL/Username/Password form, separate Test Connection + Save & Connect
  actions, `onDone` callback for the in-panel Back button, `hasDraftEdits` /
  `isApplyingInitialValues` race guard around the async Keychain load.
- `apple/FilesNestCore/Sources/FilesNestCore/ServerURLStore.swift` — exact pattern
  the new `SyncDestinationStore` mirrors (`UserDefaults`, injectable suite, single
  key).
- `apple/FilesNestCore/Tests/FilesNestCoreTests/ShellStoresTests.swift` — existing
  home for small store round-trip tests; `ConnectionProbeTests.swift` is the existing
  `MockURLProtocol`-based pattern for probe success/failure tests.
- `apple/macos/FilesNest/FilesNestTests/FilesNestTests.swift` — the app target's own
  Xcode test target (`FilesNestTests`, Swift Testing framework, `@testable import
  FilesNest`), currently just boilerplate. `swift test` cannot see this target (it's
  not part of the `FilesNestCore` SwiftPM package); it runs only via `xcodebuild
  -project FilesNest.xcodeproj -scheme FilesNest test`. This is where `SettingsModel`
  tests belong, since `SettingsModel.swift` lives in the app target, not `FilesNestCore`.
- `apple/CLAUDE.md` — engine/store logic belongs in `FilesNestCore` (tested via
  `swift test`), not the app target; credentials only via `KeychainStore`/
  `CredentialStore`, never `UserDefaults`.

## Development Approach

- **Testing approach:** Regular (code, then tests) — two tiers, both automated:
  `FilesNestCoreTests` (`swift test`) for `FilesNestCore` types, `FilesNestTests`
  (`xcodebuild ... test`) for app-target types like `SettingsModel`. Only
  UI-runtime/window-behavior work (Dock icon timing, ⌘, reachability, window
  focus/close ordering) has no automated coverage and falls back to the documented
  manual-verification checklist.
- **Sequencing:** Core first. `SyncDestination`, `SyncDestinationStore`, and
  `isDestinationReady` land and get `swift test`-green before any window-scene or
  view work starts, so the UI tasks build on a stable, already-tested model layer
  instead of discovering data-model gaps mid-view-work.
- Complete each task fully, tests passing, before starting the next.
- Update this plan's checkboxes immediately as work completes; note scope changes
  inline with ➕/⚠️ if they come up.

## Testing Strategy

- **Unit tests (Core, `swift test`):** `FilesNestCoreTests` — round-trip/default
  behavior for the new store, all four branches of `isDestinationReady`.
- **Unit tests (app target, `xcodebuild ... test`):** `FilesNestTests` —
  `SettingsModel`'s `destination` wiring and `connect()`'s persist-on-success /
  no-persist-on-failure behavior, via the existing `MockURLProtocol` pattern already
  used by `ConnectionProbeTests`.
- **Manual verification:** documented as a checklist in Task 11 — covers only what
  neither test target can assert (Dock icon visibility/timing, ⌘, reachability,
  window focus/close ordering).

## What Goes Where

- **Implementation Steps** below: Core types, store, model/view changes, panel
  changes, icon generation — all achievable in this codebase.
- **Post-Completion**: manual QA checklist execution and the known ⌘,-reliability
  open item, which needs real hardware/interaction to resolve, not code.

## Implementation Steps

### Task 1: Add `SyncDestination` + `SyncDestinationStore`

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SyncDestination.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/ShellStoresTests.swift`

- [ ] add `public enum SyncDestination: String, Sendable, CaseIterable { case server; case localFolder }`
- [ ] add `public protocol SyncDestinationStore: Sendable { func load() -> SyncDestination; func save(_ destination: SyncDestination) }`
- [ ] add `UserDefaultsSyncDestinationStore` (key `"com.filesnest.syncDestination"`), mirroring `UserDefaultsServerURLStore`'s shape exactly — `load()` returns `.server` when the key is unset or holds an unrecognized raw value
- [ ] write test: fresh `UserDefaults` suite → `load()` returns `.server`
- [ ] write test: `save(.localFolder)` then `load()` → `.localFolder`; round-trip back to `.server`
- [ ] run `swift test` from `apple/FilesNestCore/` — must pass before Task 2

### Task 2: Add `isDestinationReady`

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncDestination.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/ShellStoresTests.swift`

- [ ] add `public func isDestinationReady(_ destination: SyncDestination, urlStore: any ServerURLStore, credStore: any CredentialStore) async -> Bool` — `.server` requires both a stored URL and stored credentials; `.localFolder` always returns `false` (no bookmark concept exists yet)
- [ ] write test: `.server` with URL + creds present → `true`
- [ ] write test: `.server` with URL missing, or creds missing, or both missing → `false` (three cases)
- [ ] write test: `.localFolder` → always `false` regardless of stored URL/creds
- [ ] run `swift test` — must pass before Task 3

### Task 3: `SettingsModel` — destination field + store wiring

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/SettingsModel.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Create: `apple/macos/FilesNest/FilesNestTests/SettingsModelTests.swift`

- [ ] add `@Published var destination: SyncDestination = .server { didSet { markDraftAsEdited() } }` (explicit `= .server` default — a non-optional enum has no implicit default, unlike `serverURL`/`username`/`password`'s `""`)
- [ ] add `destinationStore: any SyncDestinationStore` to `init`; assign `destination = destinationStore.load()` synchronously in `init` (not only in the async `load()`) so the picker never flashes the `.server` default before the persisted value arrives
- [ ] also read `destination` in `load()` under the existing `hasLoadedInitialValues`/`hasDraftEdits` guard, consistent with how `serverURL`/`username`/`password` are (re)confirmed there
- [ ] persist `destination` immediately on change (not gated behind a Connect-style action — it carries no secret/incomplete-data risk the way credentials do): call `destinationStore.save(_:)` in `destination`'s `didSet`
- [ ] construct **one** `UserDefaultsSyncDestinationStore` in `FilesNestApp.init`'s composition root, alongside `urlStore`/`credStore`/`stateStore`, and update the existing `SettingsModel(urlStore:credStore:probe:)` call site to also pass it — in this task, not later, so the app target still compiles after this task instead of breaking until Task 6. This is the single instance every later task (6, 8, 9) reuses — never construct a second one.
- [ ] write test (in new `FilesNestTests/SettingsModelTests.swift`, `@testable import FilesNest`): `SettingsModel` constructed with a fake store pre-loaded to `.localFolder` → `destination` reflects `.localFolder` immediately after `init`, before `load()` runs
- [ ] write test: setting `destination = .localFolder` calls `destinationStore.save(.localFolder)` (spy/fake store)
- [ ] run `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest test` — must pass before Task 4 (not `swift test`: `SettingsModel` lives in the app target, which `swift test` cannot see)

### Task 4: `SettingsModel` — replace `test()`/`save()` with `connect()`

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/SettingsModel.swift`
- Modify: `apple/macos/FilesNest/FilesNestTests/SettingsModelTests.swift`

- [ ] replace `isTesting` with `@Published var isConnecting`; remove standalone `test()`
- [ ] add `func connect() async` — guard `!isConnecting` at entry and return immediately if already `true` (prevents a double Connect click/return-key race from firing two concurrent probes and interleaving their persist/error-setting side effects); otherwise runs `ConnectionProbe.probe(baseURL:credentials:)` on the typed values; on `.ok`, persists via `credStore.save`/`urlStore.save` and calls `onSaved?()`; on `.unauthorized`/`.unreachable`, sets `testResult` (for the pill) and does **not** touch `credStore`/`urlStore`; if `credStore.save` itself throws after a successful probe, set `saveError` describing the keychain failure — same behavior the current `save()` already has (its doc comment notes a prior bug where a swallowed keychain error left the app stuck signed-out; don't regress that)
- [ ] remove the old standalone `save()` method (superseded by `connect()`)
- [ ] write test: `connect()` with a mocked `.ok` probe response persists URL + credentials and invokes `onSaved`
- [ ] write test: `connect()` with a mocked `.unauthorized`/`.unreachable` response does not call `credStore.save`/`urlStore.save`, and leaves previously-persisted values (if any) unchanged
- [ ] write test: `connect()` with an empty/invalid URL string sets `saveError` without probing
- [ ] write test: `connect()` with a mocked `.ok` probe response but a `credStore.save` that throws sets `saveError` (fake `CredentialStore` that throws on `save`)
- [ ] write test: calling `connect()` a second time while the first call's `Task` hasn't completed (`isConnecting == true`) is a no-op — the probe/spy records only one invocation
- [ ] run `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest test` — must pass before Task 5

### Task 5: `SettingsView` — destination picker + Connect button

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/SettingsView.swift`

- [ ] remove the "Connect your server" title and "FilesNest keeps your password…" subtitle text (no replacement copy — confirmed)
- [ ] add `Picker("Sync to", selection: $model.destination).pickerStyle(.segmented)` with tags `Text("FilesNest Server").tag(SyncDestination.server)` / `Text("Local Folder").tag(SyncDestination.localFolder)`
- [ ] switch on `model.destination`: `.server` renders the existing form (Server URL/Username/Password + connection-result pill) with a single `Button("Connect") { Task { await model.connect() } }` (`.disabled` when `isConnecting` or any required field is empty; shows `ProgressView` while `isConnecting`) replacing the old "Test Connection" + "Save & Connect" pair
- [ ] `.localFolder` renders a placeholder pane: `Label("Local folder sync is coming soon", systemImage: "externaldrive.badge.timemachine")` + one caption line, no fields
- [ ] keep "Launch at login" toggle below the destination-specific area, unchanged (live-apply, no button), applying regardless of `destination`
- [ ] remove `onDone`/the Back button entirely — the view no longer navigates "back," it's a real window
- [ ] grow `.frame(width: 320)` → `.frame(width: 360)`
- [ ] no new Core logic here — manual verification only (covered in Task 11); this task's "tests" are the Task 3/4 model tests already covering `destination`/`connect()`'s behavior

### Task 6: Settings window scene, hidden anchor, activation-policy toggle

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/SettingsPresenter.swift`
- Create: `apple/macos/FilesNest/FilesNest/SettingsAnchorView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Modify: `apple/macos/FilesNest/FilesNest/SettingsView.swift`

- [ ] add `SettingsPresenter`: `@MainActor enum SettingsPresenter { static func open(_ openSettings: OpenSettingsAction) { NSApp.setActivationPolicy(.regular); NSApp.activate(ignoringOtherApps: true); openSettings() } }`
- [ ] add `SettingsAnchorView`, an otherwise-empty `View` (`Color.clear` or similar, no visible content) — this replaces a bare `EmptyView()` specifically so it has a body capable of holding `@Environment(\.openSettings)` and a `.task`, which Task 9 needs for first-launch auto-open (a bare `EmptyView` can't host either)
- [ ] in `FilesNestApp.body`, add `Window("", id: "settings-anchor") { SettingsAnchorView() }.windowStyle(.hiddenTitleBar)` (render-tree context for `openSettings()` to resolve against, since this app is `.accessory`-policy with no other window that's guaranteed present at launch)
- [ ] add `Settings { SettingsView(model: settings) }` scene
- [ ] reuse the single `UserDefaultsSyncDestinationStore` instance Task 3 already constructed in `FilesNestApp.init` — this task does not construct another one
- [ ] add `.onDisappear { NSApp.setActivationPolicy(.accessory) }` to `SettingsView`'s root as the first attempt at restoring accessory policy on close. **This is not confirmed to be race-free** — whether `.onDisappear` reliably fires only after the window is fully gone (vs. firing early on a rapid open/close, leaving the Dock icon stuck) is unverified until manual testing in Task 11; if it proves flaky there, the fallback is an explicit `NSWindowDelegate.windowWillClose` on the resolved `NSWindow`. Do not treat this bullet as closing the race — treat it as the first thing to try.
- [ ] manual verification only for this task — Dock icon appear/disappear timing and the close-detection race are UI-runtime behaviors outside both test targets' reach; covered in Task 11's checklist

### Task 7: Route all Settings-opening call sites through `SettingsPresenter`

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/PanelView.swift`

- [ ] remove `showingSettings` `@State`, the `SettingsView` branch of the dashboard `ZStack`, and its slide transition — `PanelView` goes back to being just the dashboard (leave `showingFailed`/`FailedItemsView` untouched, it's unrelated)
- [ ] add `@Environment(\.openSettings) private var openSettings` to `PanelView`
- [ ] footer "Settings…" button calls `SettingsPresenter.open(openSettings)`
- [ ] sign-out CTA ("Set Up FilesNest") calls `SettingsPresenter.open(openSettings)`
- [ ] error-state "Settings" button calls `SettingsPresenter.open(openSettings)`
- [ ] add a `⌘,`-shortcut to the *existing* footer "Settings…" button itself (`.keyboardShortcut(",", modifiers: .command)`) rather than adding a second, visually separate "Settings…" control — the shortcut only needs to be reachable while the `MenuBarExtra` menu is open, and reusing the existing button avoids two same-labeled controls in one small panel
- [ ] manual verification only — covered in Task 11

### Task 8: Destination-aware panel messaging

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/PanelView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`

- [ ] pass the single `SyncDestinationStore` instance (Task 3) directly into `PanelView`'s `init`, the same way `thumbnails: ThumbnailLoader` is already passed in — not through `AppModel`. `PanelView`'s subtitle computed property calls `destinationStore.load()` fresh each time it's evaluated, so it can't go stale while the panel is open; no new `@Published` plumbing or reactivity design needed.
- [ ] `PanelView`'s `.signedOut` subtitle branches on `destinationStore.load()`: `.server` → "Connect your server in Settings"; `.localFolder` → "Local folder sync isn't available yet — set it up in Settings" (no new `SyncStatus` case)
- [ ] manual verification for the panel-message behavior — covered in Task 11

### Task 9: Sync-engine destination gating + first-launch auto-open

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Modify: `apple/macos/FilesNest/FilesNest/SettingsAnchorView.swift`

- [ ] replace the inline `guard url != nil, creds != nil` checks in `perform` and `resume` with `isDestinationReady(_:urlStore:credStore:)` from Task 2, still throwing `NotSignedInError` when it's `false` — behavior for `.server` is unchanged, `.localFolder` now correctly always refuses instead of silently having no concept of destinations
- [ ] in `assess`, replace its inline `guard` with the same `isDestinationReady(...)` call, but **keep its current graceful-fallback behavior** on `false` — `assess` does not throw; it must keep returning `Assessment(backedUp: 0, pending: scan.count, resourceTotal: scan.count)` exactly as it does today, so the panel still shows a coherent "everything pending" state for an unready destination instead of surfacing an error
- [ ] **first-launch auto-open is new behavior, not a replacement** — nothing in the current code opens Settings automatically today (`PanelView`'s sign-out state only shows a "Set Up FilesNest" button the user must click; verified by reading `AppModel`/`PanelView` — no auto-open exists). Implement it in `SettingsAnchorView` (Task 6), the one view guaranteed to exist at launch on an `.accessory`-policy app with no other window: add `@Environment(\.openSettings) private var openSettings` and a `.task` that runs once (guard a local `@State`/instance flag so re-entering the task, if it ever reruns, doesn't reopen the window), checks `await isDestinationReady(destinationStore.load(), urlStore: urlStore, credStore: credStore)`, and calls `SettingsPresenter.open(openSettings)` if `false`. `FilesNestApp.init`/`AppModel` cannot do this themselves — `openSettings` is only resolvable from inside a View's environment, and `AppModel`/`init` run outside any view's lifecycle.
- [ ] write test (Core): none needed beyond Task 2's `isDestinationReady` coverage — this task only wires existing tested logic into the app target
- [ ] manual verification for the auto-open and signed-out-assessment behavior — covered in Task 11, including explicitly re-confirming a signed-out/not-ready `assess()` still shows "pending" counts rather than an error state

### Task 10: Placeholder AppIcon

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/Assets.xcassets/AppIcon.appiconset/Contents.json`
- Create: PNG assets referenced by that `Contents.json` (16/32/128/256/512 @1x/2x)

- [ ] render the `arrow.triangle.2.circlepath` SF Symbol (tinted with the app's `AccentColor`) to each size the existing `Contents.json` slots declare, using `sips`/available image tooling (per Impeccable's session note: `cwebp`, `sips`, `magick`, `ffmpeg` available — `sips` is sufficient for simple PNG scaling of a single rendered symbol)
- [ ] update `Contents.json` to reference the generated filenames for each slot
- [ ] no automated test — visual asset; verified manually in Task 11 (icon renders correctly in Dock at each size, no blurriness/clipping)

### Task 11: Verify acceptance criteria

- [ ] verify every item in `docs/design/20260826-settings-window-and-destination.md` §4 "In (this ticket)" scope is implemented
- [ ] verify the explicit non-goals were **not** implemented: no `NSOpenPanel`, no security-scoped bookmark, no disk-writing engine, no new `SyncStatus` case, no changes to `KeychainStore`/`ServerClient`/`SyncCoordinator`
- [ ] run full Core test suite: `swift test` from `apple/FilesNestCore/` — must be green
- [ ] run app-target test target: `xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest test`
- [ ] work through the full manual-verification checklist below on a real build

Manual-verification checklist to run in this task:
- Connect with valid creds → pill shows Connected, values persist across relaunch.
- Connect with bad creds/unreachable URL → pill shows failure, nothing persisted, prior saved values (if any) untouched.
- Empty required field → Connect button disabled.
- Fresh install, `.server` selected, never connected → panel shows "Connect your server in Settings," Sync Now unavailable, and `assess()` still reports a coherent pending count rather than an error.
- Switch picker to `.localFolder` → panel shows the local-folder-not-ready message; switch back to `.server` with previously-connected creds → normal syncing status resumes without re-connecting.
- Footer "Settings…" (with its ⌘, shortcut while the panel menu is open), sign-out CTA, and error-state "Settings" button all open the Settings window.
- Dock icon appears while Settings is open, disappears when it closes (including a rapid open/close); window is independently closable/resizable and doesn't block the panel.
- Close the Settings window while a `connect()` probe is still in flight (e.g. against a slow/unresponsive host) — confirm the app doesn't crash or leave the Dock icon stuck, and that the in-flight probe's eventual result doesn't do anything surprising once the window is gone.
- Placeholder Dock icon renders correctly (not blurry/clipped) at each required size.

### Task 12: Update documentation

- [ ] update `apple/CLAUDE.md` if this work established a new pattern worth recording (e.g. the `SettingsPresenter` activation-policy-toggle approach, if future menu-bar-only windows will need the same trick)
- [ ] add a short "Sync destination" entry to `CONTEXT.md` defining `SyncDestination`/"destination" as domain vocabulary (a `Sync to: Server | Local Folder` choice, exactly one active at a time) — commit to this now rather than deferring it, since `apple/CLAUDE.md` requires new domain vocabulary to live there
- [ ] move `docs/design/20260826-settings-window-and-destination.md`'s status from "Proposed" to "Implemented" (or equivalent) once this plan completes
- [ ] move this plan file to `docs/plans/completed/`

## Post-Completion

**Manual verification:**
- The full checklist in Task 11 requires a real interactive macOS session (Dock
  icon visibility, ⌘, behavior, window focus/ordering) — not something `swift test`
  or `xcodebuild test` can assert.

**Known open item (carried from the design doc, not a blocker for this plan):**
- Whether a genuinely *global* ⌘, (menu closed, no FilesNest window focused) works
  given the accessory-policy + hidden-window-anchor approach is unresolved by
  design — only the locally-bound "Settings…" button shortcut (Task 7) is
  required to be reliable. If global ⌘, turns out not to work in practice, that's
  expected and not a regression to chase down as part of this plan.
