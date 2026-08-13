# Manual verification: panel real stats + failed-items detail

SwiftUI panel behavior is manual-verify. Run the dev-signed app (Cmd+R) against a live server.

## Tiles
- [x] Signed out: Backed up and Pending show "—"; Failed shows "0" and is not tappable.
- [x] Signed in **on launch, before any sync this session**: Backed up shows the real server count (not 0) — sourced from `listUploads`, not the last-sync summary.
- [x] After Sign in + a successful Sync Now: Backed up reflects the server count, Pending is 0 at rest, Failed is 0.
- [x] During a sync: Pending counts down (total − completed) alongside the ring; it returns to 0 when done.

## Scanning state
- [x] Right after tapping Sync Now (before the first upload), the hero shows "Syncing… / Scanning library…" with **no** "Uploading · 0 of 0" strip — it no longer looks stuck at "0 of 0" during the pre-upload enumeration.
- [x] Once uploads begin, it switches to "N of M" with the current-item strip.

## Pause
- [x] During an active sync, click **Pause** → the upload actually stops (watch the server log go quiet) and the panel shows "Paused" (not "Syncing").
- [x] **Resume** → returns to idle ("watching"); pressing **Sync Now** continues (re-diff resumes from server offsets; already-uploaded files are skipped).

## Failed items
- [x] Induce a failure if feasible (e.g. revoke access to one asset, or point at a server that rejects one item). Failed tile turns orange and becomes tappable.
- [x] Tap Failed → slides to "Failed items" showing each filename + reason.
- [x] Back returns to the dashboard with the left/right slide.

## Regression
- [x] Settings slide still works (Settings ⇄ dashboard) and is unaffected by the new Failed slide.
