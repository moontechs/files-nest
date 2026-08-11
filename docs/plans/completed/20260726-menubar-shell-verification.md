# Menu-Bar Shell — Manual Verification Checklist

Run these after any change to the macOS shell (`apple/macos/FilesNest`). The Core
seams are covered by `swift test`; this covers the SwiftUI/AppKit surface that
can't be tested headless. Build first:

```bash
xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest \
  -destination 'platform=macOS' -configuration Debug CODE_SIGNING_ALLOWED=NO build
```

Then run the app (from Xcode, or the built `.app`).

## Menu-bar agent
- [x] A menu-bar icon appears on launch.
- [x] **No Dock icon** and no window opens automatically (accessory activation policy).
- [x] Clicking the icon opens the panel (window-style, anchored under the icon).
- [x] "Quit" terminates the app.

## Panel states (Variant B)
With **no credentials saved** (fresh install / after logout):
- [x] Panel shows the **signed-out** hero: "Sign in in Settings"; Pause and Sync Now are disabled.

With **credentials saved** (see Settings below):
- [x] **Watching**: green ✓ ring full, "Up to date".
- [x] **Sync Now**: ring animates blue by progress; the current-item strip shows a thumbnail, filename, "Uploading · N of 12", and a per-file bar; returns to green "Up to date" when done.
- [x] **Pause**: amber ⏸, "Paused", "N items waiting"; button reads "Resume".
- [x] **Resume**: returns to green watching.
- [x] Layout matches `docs/design/mockups/20260726-menubar-panel.html` in both light and dark.

## Settings (panel footer "Settings…" — shown inside the panel)
- [x] "Settings…" swaps the panel to the settings view; "Back" returns to the dashboard.
- [x] Fields prefill from saved server URL + Keychain credentials on open.
- [x] **Test Connection** against a reachable, correctly-authed server → green "Connected".
- [x] Test Connection with wrong credentials → red "401 Unauthorized".
- [x] Test Connection against an unreachable host → red with the failure reason.
- [x] **Save** persists: panel leaves signed-out and shows "Up to date"; password is stored in Keychain (not UserDefaults).
- [x] **Relaunch** the app → still signed in; Settings still prefilled.
- [x] **Launch at login** toggle adds/removes the login item (System Settings › General › Login Items).

## Notes
- The sync behaviour is `StubSyncEngine` (no real photo backup this slice) — Sync Now
  runs a scripted 12-item sequence. The PhotoKit-backed engine lands next slice and
  replaces the stub with no UI change.
