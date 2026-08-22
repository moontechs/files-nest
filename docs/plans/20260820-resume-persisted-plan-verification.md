# Manual verification — persisted resume + fast launch

Prereqs: a server with real credentials and a library with a real backlog
(enough that a full count is visibly slow).

Local server used for the automated rows below:

```bash
cd server && PORT=8099 STORAGE_PATH=/tmp/fn-server-data \
  BACKUP_USER=test BACKUP_PASS=test123 go run .
cd apple/FilesNestCore && FILESNEST_LIVE_SERVER=http://localhost:8099 \
  FILESNEST_LIVE_USER=test FILESNEST_LIVE_PASS=test123 swift test --filter LiveResume
```

Legend: `[x]` mechanism verified automatically (named test) · `[ ]` still needs
human eyes, because it depends on PhotoKit or on what the panel renders.

- [x] Start a sync, let some files upload, then Pause. Quit the app.
      → `cancelledSyncPersistsNotYetUploaded` proves a cancelled run persists exactly
        the not-yet-uploaded resources, and `cleanSyncClearsRemaining` proves a clean
        run leaves nothing behind.
- [x] Relaunch: it goes straight to "Backing up" (no "Counting… 0 of N"),
      uploads the remaining files, then briefly reconciles.
      → `coldLaunchWithASavedListUploadsBeforeAnyCount` drives the whole engine against
        a REAL server and asserts the published state order: `.syncing` first, no
        survey count before it, then exactly one `.counting(.verify)`, with the files
        confirmed `complete` on the server.
- [x] Pause mid-sync, then Resume: it continues backing up immediately — no
      "Counting 0 of N".
      → `resumeWithSavedListAndNoChangeFastPaths` (goes to `.syncing`, not `.watching`)
        and `resumedProgressContinuesInsteadOfRestartingAtZero` (the counter continues
        at "3 of 5" rather than restarting at "1 of 3").
- [ ] Add a few photos while paused, then Resume: the coalesced change path
      still reconciles (safe), and the new photos get uploaded.
      → engine half covered by `resumeWithPendingChangeFallsBackToRescan`; the PhotoKit
        change notification and the actual new-photo upload need a real library.
- [ ] Delete a photo that was pending, then resume/relaunch: it does not crash;
      the deleted item drops out after the reconcile (may flash once in Failed).
      → needs a real library: the read failure comes from PhotoKit.
- [x] Sign out and back in: no stale "Backing up" from a previous account.
      → `signOutClearsSavedList` and `reconcileClearsSavedList` prove the saved list is
        dropped, and `supersededRunCannotResurrectAClearedRemaining` proves a still-
        unwinding run cannot write it back afterwards.

## Verified against a real server (not just the fake)

- `resumeUploadsASavedListToARealServer` — a saved list uploads and finalizes.
- `alreadyCompletedOnARealServerCountsAsUploaded` — a saved entry already complete
  resolves as success. This is the row that caught the bodyless-HEAD-409 bug: the
  fake returned an error body on HEAD, which no real server can do.

## Still unobserved

Everything above exercises the state machine, not the pixels. A human still needs to
watch the panel on a real backlog to confirm the wording reads right ("Verifying
backup… · Checking for changes"), the ring animates sensibly across Pause → Resume,
and the counts on screen match expectations at 40k+ assets.
