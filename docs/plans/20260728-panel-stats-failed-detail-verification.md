# Manual verification: panel real stats + failed-items detail

SwiftUI panel behavior is manual-verify. Run the dev-signed app (Cmd+R) against a live server.

## Tiles
- [ ] Signed out: Backed up and Pending show "—"; Failed shows "0" and is not tappable.
- [ ] After Sign in + a successful Sync Now: Backed up shows the synced count, Pending is 0 at rest, Failed is 0.
- [ ] During a sync: Pending counts down (total − completed) alongside the ring; it returns to 0 when done.

## Failed items
- [ ] Induce a failure if feasible (e.g. revoke access to one asset, or point at a server that rejects one item). Failed tile turns orange and becomes tappable.
- [ ] Tap Failed → slides to "Failed items" showing each filename + reason.
- [ ] Back returns to the dashboard with the left/right slide.

## Regression
- [ ] Settings slide still works (Settings ⇄ dashboard) and is unaffected by the new Failed slide.
