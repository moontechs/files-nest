# Restart forces a full reconcile — Apple clients #16

Status: approved design — ready for implementation plan.
Date: 2026-08-01.

## 1. Goal

A Settings-save (configuration change — e.g. a new server URL or credentials)
must force a full `.all` reconcile with the **new** configuration, even when a
count or sync is already running. Today `AppModel.restart()` calls `engine.start()`,
and `doStart()` is a **no-op while a run is active** (the deliberate
`!isSyncingStatus && !isCountingStatus` guard from #11, which exists so a launch
`start()` can't orphan an in-flight child). So a mid-run Settings-save is silently
ignored: no full reconcile, and an in-flight sync keeps uploading with the old
config (wrong if the server URL changed). This was Codex finding #2, deferred from
the incremental-range slice (#15).

## 2. Approach (settled during brainstorming)

- **Distinct signal.** Introduce `SyncEngine.reconcile()` for the Settings-save
  path, separate from `start()` (launch). `start()` keeps its "don't disturb an
  active run" guard and its tests unchanged; only `reconcile()` supersedes. This
  resolves the conflict Codex flagged without weakening launch behavior.
- **Supersede immediately.** On a Settings-save during an active run, **cancel the
  in-flight run** and start a fresh `.all` count+sync with the new config right
  away (mirroring the sign-out cancel pattern). Rationale: when the change matters
  (new server), the old-config run must stop rather than keep uploading to the old
  server; uploads are resumable (TUS), so re-syncing wastes little. Simpler than a
  deferred-flag approach.

## 3. Seam

- `SyncEngine` gains `func reconcile() async`.
- `LiveSyncEngine.reconcile()` enqueues `Command.reconcile` (enqueue-only, like the
  other public methods).
- `StubSyncEngine.reconcile()` = `await start()` (re-reads credentials → signed-out
  / watching; the stub has no in-flight run to supersede).
- `AppModel.restart()` calls `engine.reconcile()` instead of `engine.start()`.
- `FilesNestApp.init` launch bootstrap still calls `engine.start()`; the
  `settingsModel.onSaved = { appModel.restart() }` wiring is unchanged.

Net effect: `start()` is now **launch-only**, so `startWhileCountingDoesNotRestartAssess`
and `startDuringSyncDoesNotStrandTheEngine` remain valid, unchanged.

## 4. Engine behavior (`LiveSyncEngine`)

New `Command.reconcile` → `doReconcile()`. Unlike `doStart` (which reconciles only
when idle), `doReconcile` **always supersedes**:

```
doReconcile():
  creds = await credentials.basicCredentials()
  if creds == nil: resetToSignedOut(); return          // shared with doStart
  signedIn = true
  syncChild?.cancel();  syncChild = nil                 // stop old-config work
  assessChild?.cancel(); assessChild = nil
  incrementalAnchor = nil                               // config may point at a new server →
                                                        // re-establish the anchor via the forced .all
  if let cached = cachedAssessment?():                  // warm summary seed
      setSummary(backedUp: cached.backedUp, pending: cached.pending, failed: currentSummary.failed)
  if isPausedStatus:
      pendingLibraryChange = false
      startIdleCount(range: .all, autoSync: false)      // paused → refresh Pending, do NOT upload (#14 parity)
  else:
      startIdleCount(range: .all, autoSync: true)       // force full reconcile + upload with new config
```

- `isPausedStatus` is read **before** `startIdleCount` flips status to `.counting`,
  so it reflects the pre-reconcile state.
- `startIdleCount` bumps the generation, so the cancelled children's late
  `.finished` / `.assessFinished` commands are generation-dropped.
- **Refactor:** extract `doStart`'s signed-out branch into a private
  `resetToSignedOut()` and call it from both `doStart` and `doReconcile` (it already
  resets `generation`, `signedIn`, children, `lastProgress`, `pendingLibraryChange`,
  `autoSyncRange`, and — per #15 — `incrementalAnchor`, then sets `.signedOut` +
  empty summary).

Everything else (`doStart`, `doSyncNow`, `finishSync`, the incremental anchor, the
`.libraryChanged` handling) is unchanged.

## 5. Why reset the incremental anchor

A config change can repoint the app at a different server whose upload state is
unknown, so a `.modifiedSince(oldAnchor)` window computed against the new server
could skip items. Clearing `incrementalAnchor` forces `incrementalRange()` back to
`.all` until the reconcile's forced `.all` sync finishes cleanly and re-grounds the
anchor. Belt-and-suspenders even in the paused case (which runs no sync): a later
resume-drained change falls back to `.all` rather than trusting a stale anchor.

## 6. Testing (Core, TDD)

- **reconcile while syncing** supersedes: the in-flight sync is cancelled and a
  fresh `.all` count+sync runs (assert a second `perform`, with `.all`).
- **reconcile while counting** supersedes the count with a fresh `.all` count
  (assert `assess` runs again — contrast with `startWhileCountingDoesNotRestartAssess`,
  which asserts `start()` does NOT).
- **reconcile while paused** runs an `.all` count with **no** upload (autoSync
  false), unpausing to `.watching` (assert `perform` not called; ends `.watching`).
- **reconcile while idle / signed-out** matches `start()`.
- **reconcile resets the incremental anchor**: after a clean incremental steady
  state, a `reconcile()` makes the next library change scan `.all` (record the
  range) rather than `.modifiedSince`.
- **StubSyncEngine.reconcile** behaves like `start` (signed-out / watching).
- Existing `start()`-during-run tests remain, unchanged.

## 7. App / manual verify

`docs/plans/20260801-restart-forces-reconcile-verification.md`: save Settings
mid-sync → the in-flight run is superseded and a fresh `range=all` reconcile runs
(console `🟢 FN library: enumeration start (range=all)` + `🟣 FN engine`
supersede/counting logs); change the server URL mid-sync → the new server receives
the full backup.

## 8. Ships as

`Apple clients: Restart forces reconcile (#16)` off `main`, with this design doc and
a plan in `files-nest/docs/plans/`.
