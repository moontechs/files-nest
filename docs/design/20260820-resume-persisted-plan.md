# Resume & fast warm-launch via a persisted upload plan — Apple clients #NN

Status: approved design — ready for implementation plan.
Date: 2026-08-20.

## 1. Goal

Make both **Resume** (after Pause) and **cold app launch** skip the full library
re-count and start backing up immediately, by persisting the list of files still
to upload and re-driving it directly.

Today the engine never stores queue position ("crash-resume is emergent from
re-diffing", `SyncStateStore`), so any Resume or launch first re-enumerates the
whole library — the determinate "Counting… 0 of 46,167" a user hit. On a large
library that reads as "it restarted from scratch."

Fixing it well requires the plan on **disk** (in-memory would still leave cold
launch slow). One mechanism — a persisted "remaining uploads" list — serves both
Resume and launch identically: upload the saved list first, then reconcile.

Scope: `FilesNestCore` (state store, coordinator, engine) plus the app
composition root wiring. Non-goals: a new UI; changing the delete flow; changing
per-file TUS resume (already correct).

## 2. Why this is safe (design invariants)

- **Per-file byte resume is unchanged.** Every upload still `HEAD`s the server
  offset before sending, so a saved entry that is half-done, fully-done, or
  brand-new all resolve correctly. The saved list only decides *which* files to
  attempt, never *from what offset*.
- **Reconcile-after is the safety net.** The fast upload gets bytes flowing; a
  normal `.all` reconcile runs immediately after and catches everything the saved
  list couldn't know (deletions, photos added while the app was closed) and
  refreshes exact counts. This is the existing deletion-detection pass, just moved
  after the fast upload instead of before it.
- **One child at a time.** Fast-upload then reconcile run **sequentially** under
  the existing generation-gated single-child model — no new concurrency, so the
  TOCTOU surface that was carefully closed stays closed.
- **Eventual consistency is already the contract.** A stale saved entry self-heals
  on the following reconcile; nothing is lost.

## 3. What is persisted, and when

- **Persist `[AssetResource]` (the remaining files), not modes.** On re-drive each
  is rebuilt as a **`.create`** `PlannedUpload`. `.create` is idempotent
  server-side (POST returns the existing record if one exists), so a stale entry
  never fails on a dead `uploadID`. `AssetResource` carries everything `POST`
  needs (key, filename, creationDate, bundleID).
- **Write points:** **throttled during a run** (every *K* completions, default
  ~500 — one `O(N)` encode of a shrinking list per tick, not per file), a **final
  write on cancel** (Pause), and **clear on a clean finish**. This single throttled
  mechanism covers Pause, graceful quit, and hard crash (a crash loses at most the
  last <K completions, which re-verify via `HEAD` and skip).
- **Codable:** add `Codable` to `AssetResource`, `ResourceKey`, `ResourceKind`
  (plain value types). Stored as JSON in `SyncStateStore`.

## 4. `SyncStateStore` additions

```
func saveRemainingUploads(_ resources: [AssetResource])
func loadRemainingUploads() -> [AssetResource]     // [] when absent/undecodable
func clearRemainingUploads()
```

`UserDefaultsSyncStateStore` stores JSON under a new key
(`com.filesnest.sync.remainingUploads`); `loadRemainingUploads` returns `[]` on a
missing or undecodable value (clean fallback to a normal count).

## 5. `SyncCoordinator` refactor

`SyncCoordinator` already holds the `SyncStateStore`, so persistence lives here
(Core, testable), not in the engine.

- **Extract** the bounded sliding-window upload loop (from PR #27) into a private
  `runUploads(_ uploads: [PlannedUpload], onProgress:) async throws -> (uploaded: [ResourceKey], failed: [FailedItem])`.
- **Two entry points feed it:**
  - `sync(range:onProgress:)` — unchanged path: enumerate → page → plan →
    `runUploads` → deletes → report.
  - **`resume(resources:onProgress:) async throws -> SyncReport`** — new. Rebuilds
    each `AssetResource` into a `.create` `PlannedUpload`, runs `runUploads` only
    (no enumerate, no deletes), returns a report.
- **Persistence inside `runUploads`:** after every completion compute
  `remaining = uploads.filter { !uploadedKeys.contains($0.resource.key) }.map(\.resource)`;
  every *K* completions and once on cancel, `state.saveRemainingUploads(remaining)`.
  On a clean finish (loop drained), `state.clearRemainingUploads()`.
- **Idempotent re-drive:** treat `ServerClientError.alreadyCompleted` as a
  **successful** upload (append to `uploaded`), not a failure — a saved item
  already on the server resolves cleanly instead of landing in Failed.

`uploadedKeys` is the authoritative set from PR #27's concurrent loop (completion
order is not plan order, so a count cannot define "remaining").

## 6. `LiveSyncEngine` lifecycle

The engine reads `state.loadRemainingUploads()` to decide the fast-path and drives
re-drive through a **new injected closure**:

```
public typealias Resume =
    @Sendable ([AssetResource], @Sendable (SyncProgress) -> Void) async throws -> SyncReport
```

built in the app root as `SyncCoordinator(...).resume(resources:onProgress:)`.
`perform`'s signature is unchanged. `StubSyncEngine` gets a no-op `resume`.

- **Cold launch (`doStart`, idle branch):** if `loadRemainingUploads()` is
  non-empty → **fast-path**: go straight to `.syncing` and upload the saved list
  (no "Counting…"); on finish, chain the normal `.all` reconcile
  (`startIdleCount(.all, autoSync: true)`). If empty → today's behavior.
- **Pause → Resume (`doResume`):** if the saved list is non-empty **and
  `pendingLibraryChange == false`** → same fast-path then reconcile. If a change
  was coalesced during the pause → today's rescan (so in-session edits are caught).
  If no saved list → today's behavior.
- **Fast-path child** mirrors `doSyncNow`: bump generation, set `.syncing`, launch
  a child calling `resume(saved) { submit(.progress …) }`, then `.finished` →
  `finishSync` → chain the reconcile. Generation-gating drops a superseded run.
- **Clearing:** the coordinator clears the saved list on clean finish; the engine
  also clears (via `state.clearRemainingUploads()`) on **sign-out**
  (`resetToSignedOut`) and **reconcile** (`doReconcile`, config/server change).

## 7. Staleness & error edges

- Deleted-while-away → read fails → one Failed item on the fast-pass → dropped by
  the following reconcile. Transient, self-healing.
- Added-while-away → skipped by the fast-pass → uploaded by the reconcile-after.
- Already-complete saved entry → `alreadyCompleted` = success (§5).
- Corrupt/undecodable saved blob → `[]` → normal count.
- Hard crash between writes → saved list misses <K completions → re-verified via
  `HEAD`, skipped.
- Server/account switch (reconcile) and sign-out clear the saved list, so a list
  from one server never drives uploads against another.

## 8. Testing

Core:
- `SyncStateStore`: `save`/`load`/`clear` round-trip; `load` returns `[]` on
  missing/undecodable; `AssetResource`/`ResourceKey`/`ResourceKind` Codable.
- Coordinator: cancel mid-`sync` persists exactly the not-yet-uploaded resources;
  clean `sync` clears the saved list; throttled write shrinks the saved list
  during a run; `resume(resources:)` uploads them as `.create` **without**
  enumerating (inject a `FakeAssetLibrary` whose `resources(in:)` fails, proving no
  scan); `alreadyCompleted` → counted in `uploaded`, not `failed`.
- Engine: launch **with** a saved list drives `resume` before any count (assert
  order); launch with an **empty** list keeps count-then-sync; resume with a saved
  list and no pending change fast-paths; resume with a pending change falls back to
  rescan; sign-out and reconcile clear the saved list.

Manual (checklist doc in `docs/plans/`): cold launch on a real backlog shows
"Backing up" immediately then reconciles; quit mid-sync → relaunch resumes fast;
Resume after Pause no longer shows "Counting 0 of N".

## 9. Sequencing

Builds on **PR #27** (concurrent uploads): "remaining = planned − uploaded set"
requires the authoritative uploaded set from #27's loop. This slice branches off
`apple-clients/upload-concurrency` (or lands after #27 merges), not bare `main`.
Ships as its own PR: `Apple clients: Persisted resume + fast launch (#NN)`.
