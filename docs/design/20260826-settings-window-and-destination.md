# Design: Settings as a Separate Window + Sync Destination Choice

**Date:** 2026-08-26
**Status:** Implemented
**Packages:** `apple/macos/FilesNest` (window scene, views) and `apple/FilesNestCore` (new `SyncDestination` seam)
**Supersedes:** `docs/design/20260726-menubar-shell.md` §5.1 — that spec chose in-panel,
AirBuddy-style Settings ("no separate `Settings {}` window scene") because Settings
had one job: enter server creds. That assumption no longer holds once Settings must
host a destination choice (§3 below); this doc revises the decision.

---

## 1. Problem

Settings currently lives *inside* the menu-bar panel (`PanelView` swaps the 320pt
dashboard for `SettingsView` with a slide transition). That fit a single-destination
app: one form, one Save button. It stops fitting once the app supports **more than
one place to send backups** — the panel is not where you want to sit and configure
that, and cramming a destination picker plus two full per-destination forms into a
320pt popover that closes when you click away is the wrong shape for it.

## 2. Decision: `Settings {}` scene, not an in-panel screen

Use SwiftUI's native `Settings` scene instead of the panel's `showingSettings` toggle:

```swift
// FilesNestApp.swift
var body: some Scene {
    MenuBarExtra("FilesNest", systemImage: "arrow.triangle.2.circlepath") {
        PanelView(model: model, settings: settings, thumbnails: thumbnails).task { model.begin() }
    }
    .menuBarExtraStyle(.window)

    Settings {
        SettingsView(model: settings)
    }
}
```

This is the platform-native pattern for exactly this problem (rung 4 of "does a
native feature already cover it" — yes): a `Settings` scene gives a real, resizable,
independently-closable window; correct window restoration and standard close/⌘W
behavior; for free, no custom `NSWindow` plumbing. Anything short of this (a
manually-managed `NSWindow`, an `openWindow` scene with hand-built menu wiring)
reimplements what `Settings {}` already does.

**Caveat — `.accessory` apps don't get this for free.** An `.accessory`-policy app
(no Dock icon, no traditional app menu bar) makes `openSettings()`/⌘, unreliable on
their own — well-documented (Apple Feedback FB10184971; confirmed by multiple 2025
writeups on `MenuBarExtra` + `Settings {}`). Two things compensate:
1. Toggle `NSApp.setActivationPolicy(.regular)` immediately before opening Settings
   (any entry point), and back to `.accessory` when the Settings window closes
   (via `NSWindow.willCloseNotification`). macOS only reliably
   orders/focuses windows for Dock-icon apps — this is what makes the window
   actually come forward and stay reachable, at the cost of a Dock icon flickering
   in for as long as Settings is open. Accepted: the app is already "not only a
   tray app" per this proposal's premise.
2. A **locally**-bound `⌘,` on a "Settings…" menu item inside the `MenuBarExtra`'s
   own menu content, calling `openSettings()` — the reliable path while the panel
   menu itself is open. System-wide ⌘, is not guaranteed to reach an accessory app
   and is not relied on as the only entry point.

**Panel changes:**
- Remove `showingSettings` / the slide-transition swap from `PanelView`.
- Footer "Settings…", the sign-out CTA ("Set Up FilesNest"), and the error-state
  "Settings" button all route through one helper that does the activation-policy
  toggle + `openSettings()`, instead of toggling local state.
- `SettingsView.onDone` goes away — a window closes via the standard close button/⌘W,
  it doesn't navigate "back" to something.
- First-launch auto-open (menubar-shell.md §5.3) calls the Settings window's standard
  AppKit action once from `FilesNestApp` when the destination is not ready, instead
  of creating a blank anchor window or setting `showingSettings = true`.

`SettingsModel` is unaffected — it already survives independently of the panel's view
lifecycle (`hasLoadedInitialValues` guard), and a window follows the same
create-once-per-app-launch lifecycle a `MenuBarExtra` panel does.

**Placeholder Dock icon.** Toggling to `.regular` shows *some* Dock icon while
Settings is open. The project's `AppIcon.appiconset` currently has no images in it
(only empty Xcode-declared slots) — nothing to reuse. Rather than commission real
artwork as part of a Settings-window ticket, generate a placeholder icon set from the
same `arrow.triangle.2.circlepath` SF Symbol already used for the menu-bar glyph,
tinted with the app's `AccentColor`, so the transient Dock icon and the permanent
menu-bar icon read as one identity. Explicitly a placeholder — replace with real
branding whenever that work happens, independent of this ticket.

## 3. Sync destination (future-proofing only — this doc doesn't implement it)

Two destinations are planned:
- **Server** — current behavior (`ServerClient` + TUS upload), fully implemented.
- **Local folder** — sync to a connected drive/mounted volume. **Not built. Separate
  ticket.**

Exactly one is active at a time, chosen in Settings. Model that now so Settings has
the right shape without building the folder side:

```swift
// FilesNestCore — new, small enum + store, same pattern as ServerURLStore
public enum SyncDestination: String, Sendable, CaseIterable {
    case server
    case localFolder
}

public protocol SyncDestinationStore: Sendable {
    func load() -> SyncDestination   // defaults to .server
    func save(_ destination: SyncDestination)
}
public final class UserDefaultsSyncDestinationStore: SyncDestinationStore, @unchecked Sendable {
    public init(defaults: UserDefaults)
}   // key "com.filesnest.syncDestination"
```

Not a secret (like the server URL) — `UserDefaults`, not Keychain.

**Settings window layout** — a `Picker(.segmented)` at the top choosing the
destination, with the existing server form shown when `.server` is selected. General,
destination-independent settings (Launch at login, …) sit outside the
destination-specific area entirely, so they don't get duplicated per destination and
don't change when the picker changes:

```
┌──────────────────────────────────────┐
│  Sync to: [ FilesNest Server | Local Folder ] │  ← segmented Picker
├──────────────────────────────────────┤
│  (destination-specific form)          │
│                                        │
│  FilesNest Server → today's form:     │
│    Server URL / Username / Pw         │
│    Test Connection · pill             │
│                                        │
│  Local Folder → placeholder pane:     │
│    "Local folder sync is coming       │
│     soon." (selectable now, no        │
│     folder picker/logic yet)          │
├──────────────────────────────────────┤
│  Launch at login                      │  ← General, independent of destination
├──────────────────────────────────────┤
│  (saveError, if any)      [Save & Connect] │
└──────────────────────────────────────┘
```

A segmented picker over two mutually-exclusive options is the native macOS idiom for
this (System Settings does the same for e.g. "Allow accessories to connect" sources);
it doesn't need a sidebar or `TabView` pane structure for two items. The Local Folder
segment is selectable now (not disabled) — a disabled segment reads as permanently
unavailable, where the goal is "coming soon." `SettingsModel` gains a published
`destination: SyncDestination` bound to the picker and persisted via
`SyncDestinationStore`. Local Folder has no engine yet, so selecting it makes the
current server-backed engine unavailable; the panel keeps local resources pending until
the user switches back to a ready Server destination.

**Why model this now instead of waiting for the folder-sync ticket:** so the folder
ticket adds a destination and a form, not a picker, a form, *and* a Settings
restructure. It's the one piece of this proposal worth doing ahead of need — every
other change here is purely "move existing Settings to a window."

## 4. Scope

**In (this ticket):**
- `Settings {}` scene replaces in-panel `showingSettings`; launch-time setup uses the
  standard Settings-window action without an anchor `Window`.
- A shared open-Settings helper (activation-policy toggle + `openSettings()`) used by
  `PanelView`'s footer/CTA/error buttons, the first-launch auto-open, and a locally
  `⌘,`-bound "Settings…" item inside the `MenuBarExtra` menu.
- Activation policy flips `.accessory` → `.regular` while Settings is open, back to
  `.accessory` on close.
- Placeholder `AppIcon.appiconset` generated from the existing menu-bar SF Symbol.
- `SyncDestination` enum + `SyncDestinationStore` in Core, unit-tested (round-trip,
  default-to-`.server`).
- `SettingsView` gains the segmented destination picker (labels "FilesNest Server" /
  "Local Folder"); `.localFolder` is selectable and renders a "coming soon"
  placeholder, no local-folder logic. General settings (Launch at login) move outside
  the destination-specific area.

**Out (future, separate ticket):**
- Local folder sync itself — picking a folder (`NSOpenPanel`/security-scoped
  bookmark), a `SyncEngine`/`AssetUploader` implementation that writes to disk
  instead of `ServerClient`, and whatever `SyncCoordinator` changes that implies.
  This doc only reserves the picker and the enum case so that ticket doesn't also
  have to touch Settings' shape.

## 5. Testing strategy

- **Core:** `UserDefaultsSyncDestinationStore` round-trip with an injected suite;
  defaults to `.server` when unset.
- **App target (manual verification):** panel's "Settings…"/CTA/error buttons and the
  locally-bound ⌘, (menu open) all open the window; Dock icon appears while Settings
  is open and disappears when it closes; window is independently closable/resizable
  and doesn't block the panel; first launch with no saved credentials opens it
  automatically; destination picker persists across relaunch; selecting Local Folder
  shows the "coming soon" placeholder and does not affect the active (server) sync
  engine; placeholder Dock icon renders correctly at all required sizes.

## 6. Open items

1. **Destination switch mid-flight.** If a user switches `.server` → `.localFolder`
   once folder sync exists, does the server engine pause, or do both run assessed
   independently? Deferred to the folder-sync ticket — out of scope here since
   `.localFolder` has no engine yet.
2. **System-wide ⌘, reliability.** Whether the activation-policy toggle makes a
   genuinely global ⌘, (i.e. while the menu is closed and no FilesNest window has
   focus) work, or only the locally-bound menu-item shortcut is dependable — resolve
   empirically during implementation; the design doesn't depend on the global case
   working.
