# Manual verification — persisted resume + fast launch

Prereqs: a server with real credentials and a library with a real backlog
(enough that a full count is visibly slow).

- [ ] Start a sync, let some files upload, then Pause. Quit the app.
- [ ] Relaunch: it goes straight to "Backing up" (no "Counting… 0 of N"),
      uploads the remaining files, then briefly reconciles.
- [ ] Pause mid-sync, then Resume: it continues backing up immediately — no
      "Counting 0 of N".
- [ ] Add a few photos while paused, then Resume: the coalesced change path
      still reconciles (safe), and the new photos get uploaded.
- [ ] Delete a photo that was pending, then resume/relaunch: it does not crash;
      the deleted item drops out after the reconcile (may flash once in Failed).
- [ ] Sign out and back in: no stale "Backing up" from a previous account.
