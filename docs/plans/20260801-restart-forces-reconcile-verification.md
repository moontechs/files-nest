# Restart forces reconcile — manual verification

Prereqs: signed in; a photo library backed up to a steady state. Watch the console
(`🟢 FN library: enumeration start (range=…)`, `🟣 FN engine`).

- [ ] **Save Settings mid-sync.** Trigger a change so a sync is running, then open
      Settings and Save. The in-flight run is superseded and a fresh `range=all`
      count+sync starts immediately (not deferred).
- [ ] **Change the server URL mid-sync** (point at a second, empty server) and Save.
      The new server receives the full backup (`range=all`), not just a window.
- [ ] **Save Settings while paused.** Reconcile refreshes Pending (`range=all` count)
      but does NOT upload; status stays paused-then-watching without a sync.
- [ ] **Save Settings while idle.** Behaves like before (an `.all` count + catch-up).
- [ ] After a reconcile, the next library change first scans `range=all` (anchor
      reset), then returns to `range=modifiedSince(…)` once a clean sync re-grounds it.
