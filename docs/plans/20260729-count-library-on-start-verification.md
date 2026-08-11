# Manual verification: Count library on start

Checklist for the counting-on-launch / exact at-rest Pending slice (design `docs/design/20260729-count-library-on-start.md`). Run the built app (`xcodebuild … build` then launch, or from Xcode). Watch the `🟣 FN engine` / `🟢 FN library` DEBUG logs.

## Cold launch (no cache yet — first ever run, or after deleting the app's UserDefaults)
- [ ] On open, the hero shows **"Counting…"** with an indeterminate spinner, then a determinate ring.
- [ ] The subtitle ticks **"N of 46,039"** upward (asset count) — it does **not** look frozen.
- [ ] Backed up / Pending tiles show **"—"** while counting (no cache).
- [ ] When the scan finishes, the hero settles to **"Up to date"** and Pending shows the **exact** backlog (`plan.uploads.count`), Backed up shows the server complete count.
- [ ] Pause is **disabled** while counting; Sync Now is enabled.

## Warm launch (cache exists — relaunch after a completed count)
- [ ] On open, Backed up / Pending show the **last cached numbers immediately** (not "—", not frozen).
- [ ] The hero enters **"Counting…"** and re-counts in the background; tiles update when it finishes.

## Interactions during counting
- [ ] **Sync Now during counting** cancels the count and starts a sync (hero → "Syncing…"); the scan log stops climbing.
- [ ] **Sign out (clear creds in Settings) during counting** → hero goes to "Sign in in Settings", tiles show "—".
- [ ] Settings **Save** (restart) while idle re-triggers a count.

## Post-sync (no re-scan)
- [ ] After a Sync Now completes, Pending drops to the failure count (≈0) and Backed up reflects skipped+uploaded — **without** a second ~60s scan (no new `🟢 FN library: enumerated …` line for a count after the sync).

## Notes
- Progress throttle is every 250 assets (+ final); confirm the ring animates smoothly without stutter. Tune the constant in `PhotosAssetLibrary.resources(in:onProgress:)` if needed.
- A *count* failure (Photos denied / server unreachable) must fall back to "Up to date" (not an error banner); the sync path still surfaces `.error` normally.
