# Manual verification: PhotoKit AssetLibrary + real Sync Now

The PhotoKit enumeration adapter (`PhotosAssetLibrary`) is untestable in `swift test`
(it needs the real photo library + TCC). Verify by hand against a live FilesNest server.

## Setup
- [x] Build & run the app (`xcodebuild … build`, then launch the product, or Run in Xcode).
- [x] Confirm it launches as a background agent (menu-bar icon, no Dock icon).
- [x] In Settings, enter a valid server URL + Basic Auth creds; Test Connection → ok; Save.

## Authorization
- [x] First Sync Now triggers the macOS Photos permission prompt
      (copy: "FilesNest reads your photo and video originals to back them up to your server.").
- [x] Grant access.

## Real sync
- [x] Sync Now uploads real originals: the ring advances, and the current-item strip shows
      real filenames (e.g. `IMG_xxxx.HEIC`).
- [x] On completion the panel returns to "watching" with a "last synced" timestamp.
- [x] Server side: uploaded files appear under the user's `year/month/day` tree.
- [x] A **Live Photo** produces two server records sharing a `bundleID` (the `#photo`
      and `#pairedVideo` resources).
- [x] Running Sync Now again with no new photos completes quickly with no new uploads
      (already-in-sync → skipped).

## Failure paths
- [x] Deny Photos access (reset via `tccutil reset Photos <bundle-id>` or System Settings),
      Sync Now → panel shows an error state (not a crash).
- [x] Clear the server URL/creds → Sync Now → panel shows signed-out / error (not a crash).

## Notes
- `bytesRemaining` is intentionally `nil` (PhotoKit doesn't expose resource size publicly),
  so the ring shows count progress only — no byte countdown.
- If enumeration is blocked by the app sandbox rather than TCC, add the Photos entitlement
  to the target; the usage string alone is already present.
