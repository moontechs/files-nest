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
- [ ] A menu-bar icon appears on launch.
- [ ] **No Dock icon** and no window opens automatically (accessory activation policy).
- [ ] Clicking the icon opens the panel (window-style, anchored under the icon).
- [ ] "Quit" terminates the app.

## Panel states (Variant B)
With **no credentials saved** (fresh install / after logout):
- [ ] Panel shows the **signed-out** hero: "Sign in in Settings"; Pause and Sync Now are disabled.

With **credentials saved** (see Settings below):
- [ ] **Watching**: green ✓ ring full, "Up to date".
- [ ] **Sync Now**: ring animates blue by progress; the current-item strip shows a thumbnail, filename, "Uploading · N of 12", and a per-file bar; returns to green "Up to date" when done.
- [ ] **Pause**: amber ⏸, "Paused", "N items waiting"; button reads "Resume".
- [ ] **Resume**: returns to green watching.
- [ ] Layout matches `docs/design/mockups/20260726-menubar-panel.html` in both light and dark.

## Settings (⌘, or footer "Settings…")
- [ ] Fields prefill from saved server URL + Keychain credentials on open.
- [ ] **Test Connection** against a reachable, correctly-authed server → green "Connected".
- [ ] Test Connection with wrong credentials → red "401 Unauthorized".
- [ ] Test Connection against an unreachable host → red with the failure reason.
- [ ] **Save** persists: panel leaves signed-out and shows "Up to date"; password is stored in Keychain (not UserDefaults).
- [ ] **Relaunch** the app → still signed in; Settings still prefilled.
- [ ] **Launch at login** toggle adds/removes the login item (System Settings › General › Login Items).

## Notes
- The sync behaviour is `StubSyncEngine` (no real photo backup this slice) — Sync Now
  runs a scripted 12-item sequence. The PhotoKit-backed engine lands next slice and
  replaces the stub with no UI change.
