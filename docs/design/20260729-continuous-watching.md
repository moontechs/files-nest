# Continuous Watching (Apple clients #13)

Status: approved design — ready for implementation plan.
Date: 2026-07-29.

## 1. Goal

Make the macOS menu-bar agent **back up automatically** when the photo library
changes, without a manual Sync Now, and keep the idle count honest. This
completes the "watcher" promised by `SyncStatus.watching` and turns the app into
a real auto-backup agent.

Concretely, when a photo is added / imported / edited (a burst, debounced) the
app: invalidates its cached scan, **counts** (showing the determinate
`.counting` state + updated Pending), and then **auto-syncs** to upload the new
work — count-first-then-upload. On launch it does the same catch-up: count, then
auto-sync anything pending.

Non-goals (deferred — see §8): incremental `.dates` scan range, determinate
progress on the sync's own scan, `resourceTotal` display.

## 2. Scope decisions (settled during brainstorming)

- **Scan range: `.all`.** Every count and sync uses the full library range.
  Incremental (`.dates`) windowing was considered and **deferred** to its own
  slice: a `creationDate`-based window silently misses imported-old-media
  (e.g. a photo taken in 2020 imported today has an old `creationDate`, falls
  outside `[lastSyncStarted … now]`, and would never back up). Doing it safely
  needs `modificationDate` windowing + matching planner/delete semantics — a
  materially bigger, correctness-sensitive change. A full `.all` re-scan of a
  ~70k library is a few background seconds, only after a debounced change-burst
  (a handful of times a day) — correct and fast enough for a menu-bar agent.
  Because everything stays `.all`, the existing report-derived `finishSync`
  summary remains correct with **no change**.

- **Count-first-then-upload.** A change drives the visible `.counting` state
  (updated Pending) before the upload starts, reusing the existing count
  machinery rather than jumping straight to `.syncing`.

- **Launch auto-syncs too (option A).** When the startup count finds pending
  work the app auto-syncs it (catch-up on open), for a fully consistent
  "it just backs up" behavior. Same mechanism as on-change.

## 3. Architecture — the seam

Core stays PhotoKit-free by construction. `PHPhotoLibraryChangeObserver` is
PhotoKit, so the observer lives in the **app target** and signals Core through
the same closure/protocol seam pattern already used for `perform`/`assess`.

```
PhotoKit change ─▶ PhotoLibraryWatcher (app target, debounced)
                        │  await library.invalidate()   (app owns the cache)
                        ▼
                   engine.libraryDidChange()  ─▶  LiveSyncEngine (Core)
                        │  enqueue .libraryChanged
                        ▼
                   count (.counting) ─▶ auto-sync if pending ─▶ .watching
```

Rationale for the split: the app owns cache invalidation and the (inherently
fuzzy) debounce timer — both sit next to PhotoKit where the app is manual-verify
anyway. The engine stays library/cache-agnostic and fully unit-testable; it only
learns "the library changed" and reacts with deterministic state-machine logic.

## 4. Core changes (`FilesNestCore`)

### 4.1 `SyncEngine` protocol

Add one method:

```swift
func libraryDidChange() async   // a debounced library-change signal from the host
```

`StubSyncEngine` implements it as a no-op.

### 4.2 `LiveSyncEngine`

New command and consumer-only state:

- `Command.libraryChanged`
- `private var pendingLibraryChange = false` — coalesces a change that arrives
  while a run is in flight.
- `private var autoSyncAfterCount = false` — whether the count currently in
  flight should chain into a sync when it settles.

New public method (enqueue only, matching the existing pattern):

```swift
public func libraryDidChange() async { submit(.libraryChanged) }
```

**`.libraryChanged` handler** (ignored unless `signedIn`):

| Current status        | Action                                                        |
|-----------------------|---------------------------------------------------------------|
| `.watching` / `.error`| `startIdleCount(autoSync: true)` — count, then sync if pending |
| `.syncing` / `.counting` | `pendingLibraryChange = true` (handled when the run finishes) |
| `.paused`             | `pendingLibraryChange = true` (honored on resume; never upload while paused) |
| `.signedOut`          | ignore                                                        |

**`startIdleCount(autoSync:)`** — extracted from `doStart`'s existing idle
branch so both share it:

```swift
private func startIdleCount(autoSync: Bool) {
    guard signedIn else { return }
    generation &+= 1
    lastProgress = nil
    autoSyncAfterCount = autoSync
    beginCounting(gen: generation)
}
```

- `doStart` idle branch keeps its **warm-launch seed** (show last-known Pending
  instantly from `cachedAssessment` before the count lands) and then calls
  `startIdleCount(autoSync: true)` (launch/restart catch-up — option A):

  ```swift
  if !isSyncingStatus && !isCountingStatus {
      if let cached = cachedAssessment?() {
          setSummary(SyncSummary(backedUp: cached.backedUp, pending: cached.pending,
                                 failed: currentSummary.failed))
      }
      startIdleCount(autoSync: true)
  }
  ```

  The seed is **not** in `startIdleCount` on purpose: a mid-session
  `.libraryChanged` already has a live summary, so re-seeding from the (older)
  cache would briefly flash a stale value. Only the launch/restart path (empty
  summary) seeds.

- `doStart`'s signed-out branch additionally resets
  `pendingLibraryChange = false` and `autoSyncAfterCount = false`.

**`.assessFinished` handler** — chain the sync (count-then-upload) and drain
coalesced changes:

```swift
case .assessFinished(let gen, let a):
    if gen == generation {
        assessChild = nil
        if let a { setSummary(SyncSummary(backedUp: a.backedUp, pending: a.pending,
                                          failed: currentSummary.failed)) }
        setStatus(.watching(lastSync: lastSync))
        let shouldSync = autoSyncAfterCount && (a?.pending ?? 0) > 0
        autoSyncAfterCount = false
        if shouldSync { doSyncNow() } else { drainPendingChangeIfAny() }
    }
```

**Drain helper** — re-count (which re-chains a sync if anything remains):

```swift
private func drainPendingChangeIfAny() {
    guard pendingLibraryChange else { return }
    pendingLibraryChange = false
    startIdleCount(autoSync: true)
}
```

**Call sites for the drain:**
- `finishSync` — after it sets `.watching`.
- `doResume` — after it sets `.watching` (honors a change that arrived while paused).
- `.assessFinished` — the `else` branch above (count found nothing to sync).

**Deliberately not drained:**
- `.failed` — leave `pendingLibraryChange` as-is to avoid a tight retry loop on
  a persistent error. The next real change or a manual Sync Now recovers.
- `doPause` — leaves `pendingLibraryChange` intact so resume honors it.

**Convergence & safety:** every step is a discrete command processed by the
single serial consumer, so there is no reentrancy. A `.all` count/sync that
finds nothing new settles to `.watching` with the flag clear — the loop
terminates. `startIdleCount` guards on `signedIn`. Our uploads never mutate the
photo library (the coordinator deletes *server* records, not local assets), so
the app never triggers its own change notifications — no feedback loop.

## 5. App-target changes (`apple/macos/FilesNest`)

### 5.1 New `PhotoLibraryWatcher`

```swift
import Photos
import FilesNestCore

final class PhotoLibraryWatcher: NSObject, PHPhotoLibraryChangeObserver {
    private let library: CachingAssetLibrary
    private let engine: any SyncEngine
    private let debounce: Duration
    private var debounceTask: Task<Void, Never>?   // MainActor-isolated

    init(library: CachingAssetLibrary, engine: any SyncEngine,
         debounce: Duration = .seconds(2)) {
        self.library = library; self.engine = engine; self.debounce = debounce
        super.init()
    }

    func startObserving() { PHPhotoLibrary.shared().register(self) }

    // Called by PhotoKit on an arbitrary queue.
    func photoLibraryDidChange(_ changeInstance: PHChange) {
        Task { @MainActor in self.scheduleFlush() }
    }

    @MainActor private func scheduleFlush() {
        debounceTask?.cancel()
        debounceTask = Task { [library, engine, debounce] in
            try? await Task.sleep(for: debounce)
            if Task.isCancelled { return }
            await library.invalidate()        // app owns invalidation; before notifying
            await engine.libraryDidChange()
        }
    }

    deinit { PHPhotoLibrary.shared().unregisterChangeObserver(self) }
}
```

- Debounce coalesces a burst (e.g. importing 500 photos → many notifications)
  into one signal after ~2s of quiescence.
- `invalidate()` runs **before** `libraryDidChange()` so the subsequent count/sync
  re-scans fresh.
- Registration is harmless before photo-library authorization; it simply won't
  fire until access is granted (the app already requests auth on first scan).

### 5.2 Composition root (`FilesNestApp.init`)

- `CachingAssetLibrary(wrapping: PhotosAssetLibrary(), ttl: 300)` — change-based
  `invalidate()` becomes the primary freshness mechanism; the 5-minute TTL is a
  self-healing backstop for a missed observer signal (not `.infinity`).
- After building `engine`, create and retain the watcher, then start observing:

```swift
let watcher = PhotoLibraryWatcher(library: library, engine: engine)
watcher.startObserving()
```

  Retain it as a `private let` on `FilesNestApp` (or on `AppModel`) so it lives
  for the app's lifetime.

## 6. UI

No new views. The change flow reuses existing states: `.counting` hero (with the
determinate "Counting N of M"), the Pending tile, and the `.syncing` strip. The
user simply sees the panel count then sync on its own after adding photos.

## 7. Testing

### 7.1 Core (deterministic, via the existing `settle()` barrier)

Extend `LiveSyncEngineTests` with a fake `perform`/`assess` (existing
`FakeAssetLibrary`/`FakeServer` support):

- change-while-idle → a count runs, then a sync runs (perform invoked once).
- change-with-nothing-new → count runs, **no** sync, flag clears (convergence /
  no loop).
- change-while-syncing → coalesced; exactly one follow-up count+sync after the
  in-flight sync finishes.
- change-while-counting → coalesced; a single re-count after the current count
  settles (no supersede/starvation).
- change-while-paused → no sync; a sync runs on resume.
- change-while-signed-out → ignored.
- launch with pending>0 → auto-syncs (option A); launch with pending==0 →
  settles to `.watching`, no sync.
- `libraryDidChange` before `start()` / after sign-out → no crash, no sync.

`StubSyncEngine` no-op covered by existing stub tests (add one assertion that it
does nothing).

### 7.2 App (manual-verify checklist)

Written as `docs/plans/20260729-continuous-watching-verification.md`:
add a photo → panel counts then syncs within a couple seconds; import a burst →
single coalesced sync (debounced); add while syncing → follow-up sync; add while
paused → nothing until resume; launch with a backlog → auto catch-up sync.

## 8. Deferred / out of scope (tracked)

1. **Incremental `.dates` range** — its own slice, with `modificationDate`
   windowing to catch imported-old-media, plus matching planner/delete
   semantics and tests. (§2.)
2. **Determinate progress on the sync's own scan** — `resources(in:onProgress:)`
   already supports it; `SyncCoordinator` passes `nil` today.
3. **`resourceTotal` display** (e.g. "63,201 of 70,444 backed up") — cached, not
   shown.
4. **Auto-sync policy knobs** (e.g. Wi-Fi-only, quiet hours, a user toggle for
   auto-upload) — not in scope; the agent auto-syncs unconditionally when signed
   in and not paused.

## 9. Ships as

`Apple clients: Continuous watching (#13)` off `main`, with this design doc and
a plan in `files-nest/docs/plans/`.
