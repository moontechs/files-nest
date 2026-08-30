# Apple: Local Folder Sync Destination

## Overview

Implements the `.localFolder` `SyncDestination` (currently a "coming soon"
placeholder) so FilesNest can back up the Mac Photos library directly to a
user-chosen folder, in addition to the existing FilesNest-server destination.
Writes happen directly from the Mac app's own process — no local server
subprocess (`docs/adr/0008-local-folder-sync-writes-directly-no-subprocess.md`).

This is the apple-side half of a two-ticket design; the companion server
ticket (`docs/plans/20260829-server-unify-organized-filename-suffix.md`)
changes the server to compute filenames the same way, but this ticket does
not depend on that one merging first — both sides independently compute the
identical `SafeID(resourceKey)` formula.

## Context (from discovery)

- `apple/FilesNestCore/Sources/FilesNestCore/SyncDestination.swift` — already
  has the `SyncDestination` enum, `SyncDestinationStore`, and
  `isDestinationReady` (hardcoded `false` for `.localFolder`).
- `apple/FilesNestCore/Sources/FilesNestCore/LiveSyncEngine.swift` — drives
  sync via `Perform`/`Resume` closure typealiases; already destination-agnostic,
  no changes needed here.
- `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` (lines 27-100) — the
  composition root. `init()` builds `LiveSyncEngine`'s `perform`/`resume`/
  `assess`/`isReady` closures, each currently hardcoded to build a
  `ServerClient` + `AssetUploader` + `SyncCoordinator`. This is where the
  destination branch goes.
- `apple/macos/FilesNest/FilesNest/SettingsModel.swift` — has `@Published var
  destination: SyncDestination`, already persists via `destinationStore`.
  Needs a bookmark-holding property/method for the picked folder.
- `apple/macos/FilesNest/FilesNest/SettingsView.swift` (lines 65-74) —
  `localFolderPlaceholder` is the exact view to replace with a real folder
  picker.
- `apple/FilesNestCore/Sources/FilesNestCore/{SyncCoordinator,SyncPlanner,
  SyncPlan,AssetUploader,AssetLibrary,ServerURLStore,DiskProbe,
  ResourceKey}.swift` — shape templates for the new local-folder types (NOT
  shared via protocol — see Solution Overview).
- `server/internal/api/ids.go`'s `SafeID` — `base64.RawURLEncoding(SHA256(x))`,
  43 chars. Swift must reproduce byte-identically via `CryptoKit.SHA256` +
  URL-safe base64 (no padding).
- `apple/FilesNestCore/Sources/FilesNestCore/AssetDataSource.swift` — the
  streaming/no-buffering read seam `LocalFolderWriter` must reuse (same
  contract `AssetUploader` already uses via `source.read(assetID:from:into:)`).

## Development Approach

- **Testing approach**: Regular (code first, then tests).
- Complete each task fully (including tests) before moving to the next.
- Follow `apple/CLAUDE.md`: engine/sync logic goes in `FilesNestCore`, not the
  app target. Fakes live in the test target only. No mocking of the
  filesystem where a real temp directory works (mirrors the existing "no
  mocking BadgerDB, use a real instance in a temp dir" convention).
- **CRITICAL: update this plan file when scope changes during implementation.**

## Testing Strategy

- **Unit tests (FilesNestCore)**: `LocalFolderPlanner` (pure diff, fakeable
  inputs, no I/O — test like `SyncPlanner`), `LocalFolderWriter` (real temp
  directory, fake `AssetDataSource`), `LocalFolderSyncCoordinator` (real temp
  directory + `FakeAssetLibrary`, mirrors `SyncCoordinatorTests`),
  `UserDefaultsLocalFolderStore` (round-trip with injected suite, mirrors
  `ServerURLStoreTests`), the Swift `SafeID`-equivalent function (known
  input/output vectors cross-checked against the Go implementation's actual
  output for the same strings).
- **App target (manual verification, no automated UI test harness for this
  app per `docs/architecture.md`'s testing-approach section — "UI tested
  manually")**: folder picker flow, bookmark persistence across relaunch,
  destination switch mid-sync, unavailable-folder error surfacing.

## Progress Tracking

- Mark completed items `[x]` immediately when done.
- Add newly discovered tasks with an ➕ prefix.
- Document blockers with a ⚠️ prefix.

## Solution Overview

New parallel types in `FilesNestCore`, mirroring the server-destination
path's shape but NOT sharing a protocol with it (rejected during brainstorm:
a `SyncExecutor` protocol for exactly two call sites, both already reachable
through `LiveSyncEngine`'s existing closures, is the interface-for-one-more-
implementation this codebase's own conventions avoid — see `ServerClient`,
which isn't behind a protocol for the identical reason).

The composition root (`FilesNestApp.swift`) branches on
`destinationStore.load()` inside each closure to decide which concrete
coordinator to build — same pattern already used there for reading
`urlStore`/`credStore` at sync time so a Settings change takes effect
immediately.

## Technical Details

**Naming**: `SafeID(resourceKey) = base64URLNoPad(SHA256(resourceKey.encoded))`,
a pure function ported 1:1 from the Go implementation. Every written file is
named `<originalFilename-stem>_<SafeID>.<ext>`, unconditionally — this is a
correctness requirement (see ADR 0008's companion reasoning: without a
server DB, a plain existence check can't distinguish "my own already-synced
file" from "a different asset's file at the same date+filename"), not a
cosmetic choice.

**Path**: `<destinationRoot>/YYYY/MM/DD/<filename>_<SafeID>.<ext>` — date
components from the asset's creation date, same rule the server uses.

**Upload plan**: for each library resource, compute its expected path
directly (no walk needed — the path is fully deterministic from the asset
alone); `FileManager.fileExists(atPath:)` → skip if present, else planned
upload.

**Delete plan** (`.all` range only): walk `destinationRoot`'s `YYYY/MM/DD/`
tree collecting actual file paths; compute expected paths for every current
library resource; `actual − expected` = orphans to delete. Restricting to
`.all` matches `SyncPlanner`'s own restriction (an incremental window can't
tell "deleted" from "outside the window").

**Write**: `DiskProbe.volumeFreeSpace(at: destinationRoot)` pre-flight →
read via the existing `AssetDataSource` seam → write to a temp filename in
the same target directory → `FileManager.replaceItem`/`moveItem` into place.
Whole-file retry on failure, no byte-offset resume. Serial (no concurrency
cap needed — writes are sequential by construction, not bounded by a
semaphore).

**Folder access**: `NSOpenPanel` (directory selection only) → create a
security-scoped bookmark (`URL.bookmarkData(options: .withSecurityScope,
...)`) → persist the `Data` via `LocalFolderStore` (`UserDefaults`, not
Keychain — not a secret, mirrors `ServerURLStore`).

A single resolve helper (added to `LocalFolderStore` or a small adjacent
type in Task 2 — decide the exact home during implementation) does:
`URL(resolvingBookmarkData:options: .withSecurityScope, bookmarkDataIsStale:
&stale, ...)`; if `stale == true`, immediately re-create and re-save fresh
bookmark data via `LocalFolderStore.save` before returning the URL (a stale
bookmark that still resolves today will eventually stop resolving if never
refreshed — this must not be left implicit). Both the readiness check
(Task 6) and the sync pass (Task 8) call this SAME helper, but each brackets
its OWN `startAccessingSecurityScopedResource()`/
`stopAccessingSecurityScopedResource()` pair around its own usage window —
they are two separate scope sessions (one short, for the readiness probe;
one spanning the whole sync), not one shared session, since the readiness
check and the sync pass run at different times and one must not hold the
resource open while the other isn't using it.

Entitlements: `FilesNest.entitlements` needs
`com.apple.security.files.user-selected.read-write` added — currently
absent (verified: entitlements file has only `app-sandbox`, `network.client`,
`personal-information.photos-library`). `com.apple.security.files.
bookmarks.app-scope` is a DIFFERENT, older bookmark mechanism (app-scoped
bookmarks, not the `.withSecurityScope` bookmarks used here) and is likely
NOT needed — Task 6 must verify this against Apple's current documentation
for `.withSecurityScope` specifically before adding it, rather than adding
both entitlements speculatively.

**Unavailable folder**: bookmark resolution failure, stale-and-unrefreshable
bookmark, or a resolved URL that no longer exists/isn't writable → surface a
`LocalFolderSyncError` equivalent to how `NotSignedInError`/connection
failures already flow into `LiveSyncEngine`'s `.error(message:)` status — no
queuing or retry loop for that cycle.

**`isDestinationReady(.localFolder)`**: currently hardcoded `false` in
`SyncDestination.swift` — becomes `true` when `LocalFolderStore.load()` is
non-nil AND resolves to an existing, writable directory.

## What Goes Where

- **Implementation Steps**: all `FilesNestCore` types, composition-root
  wiring, Settings UI, entitlements, tests.
- **Post-Completion**: manual verification of the folder-picker flow,
  bookmark persistence, and destination-switch behavior (no automated UI
  test harness exists for this app).

## Implementation Steps

### Task 1: Port `SafeID` to Swift

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/SafeID.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/SafeIDTests.swift`

- [x] Implement `func safeID(_ input: String) -> String` using `CryptoKit.SHA256`
      + `Data.base64URLEncodedString()` (no padding) — a small local
      base64url helper if `Foundation`/`CryptoKit` don't expose one directly.
      Encode the input via `input.data(using: .utf8)!` with NO Unicode
      normalization step (no `.precomposedStringWithCanonicalMapping` or
      similar) — Go's `[]byte(string)` hashes raw UTF-8 bytes unnormalized,
      so an implicit Swift-side normalization would silently diverge from
      the Go output for any non-ASCII `localIdentifier`
- [x] Write tests asserting these exact ground-truth vectors (computed and
      verified against the actual Go `SafeID` function during planning —
      same literal vectors are asserted in the companion server plan's
      Task 1, so both sides are checked against one shared ground truth
      rather than each independently trusting its own implementation):
      - `"AAAA-BBBB-CCCC-DDDD#photo"` → `"QEzizTsZbhLknu3BxIqchpZg6BiVPEM7p8HYKhmIpCc"`
      - `"AAAA-BBBB-CCCC-DDDD#pairedVideo"` → `"FlwSC0rmUccfKH1nEq9BAo3lHk_SeclzxNeV9Sp_-kw"`
      - `""` → `"47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"`
      - `"AAAA-BBBB-CCCC-DDDD-café#photo"` → `"8h9r2pPlYMjO0ke3F01cPwtzADNQkhqD2k72i46TAEk"`
        (non-ASCII vector — the one case where a Unicode-normalization bug
        would actually produce a wrong hash instead of coincidentally
        matching; catches the divergence risk noted above)
- [x] Write tests for edge cases: empty string, strings with `#` (the
      `resourceKey` separator), unicode filenames
- [x] Run `swift test` — skipped: Swift toolchain is unavailable in this environment

### Task 2: `LocalFolderStore`

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderStore.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderStoreTests.swift`

- [ ] Define `protocol LocalFolderStore: Sendable { func load() -> Data?;
      func save(_ bookmark: Data); func clear() }`, pattern-matching
      `ServerURLStore`
- [ ] Implement `UserDefaultsLocalFolderStore`, key
      `"com.filesnest.localFolderBookmark"`
- [ ] Add a resolve helper (e.g. `func resolveLocalFolder(store:
      LocalFolderStore) throws -> URL?` — free function or a small type,
      whichever reads more naturally next to `LocalFolderStore`) that:
      resolves the stored bookmark via `URL(resolvingBookmarkData:options:
      .withSecurityScope, bookmarkDataIsStale:...)`; if `isStale == true`,
      immediately re-creates bookmark data from the resolved URL and calls
      `store.save(...)` before returning; returns `nil` on missing/corrupt
      bookmark or resolution failure. This is the SINGLE place bookmark
      resolution + staleness-refresh happens — both Task 6 (readiness) and
      Task 8 (sync) call it rather than duplicating resolution logic
- [ ] Write round-trip tests with an injected `UserDefaults` suite (save →
      load matches; unset → nil; clear → nil after previously saved)
- [ ] Write tests for the empty/corrupt-data edge case (load returns nil
      rather than crashing on malformed stored data)
- [ ] Write tests for the resolve helper's staleness path: a bookmark
      flagged stale on resolution triggers a re-save with fresh data (assert
      `store.save` was called with different bytes than the original)
- [ ] Run `swift test` — must pass before task 3

### Task 3: `LocalFolderPlan` and `LocalFolderPlanner`

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderPlan.swift`
- Create: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderPlanner.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderPlannerTests.swift`

- [ ] Define `LocalFolderPlan { let uploads: [AssetResource]; let deletes:
      [(path: URL, key: ResourceKey)] }`, pattern-matching `SyncPlan` but
      without `.resume`/`.recover` modes
- [ ] Implement `LocalFolderPlanner.expectedPath(for asset: AssetResource,
      destinationRoot: URL) -> URL` — pure, deterministic (date + filename +
      `safeID(asset.key.encoded)` from Task 1), no I/O. This is the single
      source of truth for "where does this asset's file live," used both by
      the coordinator's per-asset upload check and by delete-diffing below.
- [ ] Implement `LocalFolderPlanner.planDeletes(library: [AssetResource],
      actualPaths: Set<URL>, destinationRoot: URL) -> [(path: URL, key:
      ResourceKey)]` — pure diff: `actualPaths` minus the set of
      `expectedPath` for every current library resource. `actualPaths` is
      already-walked data the caller passes in (exactly how `SyncPlanner.plan`
      takes `server: [UploadRecord]` as already-fetched data rather than
      injecting a fetcher) — no closure/protocol injection here. There is
      deliberately NO `LocalFolderPlanner.plan(...)` doing uploads+deletes
      together: the upload decision is a plain per-asset `FileManager`
      existence check the coordinator makes directly against
      `expectedPath`'s result (see Task 5) — it needs no walk and must work
      for any `SyncRange`, so it does not belong behind a "planner" that
      implies a full-snapshot diff
- [ ] Write tests for `expectedPath`: deterministic for a given asset,
      distinct for two assets sharing a date+filename (different `SafeID`
      suffixes)
- [ ] Write tests for `planDeletes`: orphan path present in `actualPaths`
      with no matching current asset → planned delete; a path matching a
      current asset's `expectedPath` → not planned; empty `actualPaths` →
      no deletes
- [ ] Write tests for two assets that would collide on a plain
      date+filename (same creation date, same original filename) — confirm
      both get distinct expected paths via their distinct `SafeID` suffixes
- [ ] Run `swift test` — must pass before task 4

### Task 4: `LocalFolderWriter`

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderWriter.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderWriterTests.swift`

- [ ] Implement `LocalFolderWriter` with `func write(assetID: String,
      destinationPath: URL) async throws`, reading via the existing
      `AssetDataSource` seam (`source.read(assetID:from:into:)`, same
      no-buffering contract `AssetUploader` uses) and writing to a temp
      filename in the same directory as `destinationPath`, then
      `FileManager.replaceItem`/`moveItem` into place
- [ ] Integrate `DiskProbe.volumeFreeSpace(at:)` as a pre-flight check;
      throw a typed error when free space is insufficient (define a
      reasonable safety margin, matching whatever margin convention — if
      any — the iCloud-materialization pre-flight already uses)
- [ ] Write tests using a real temp directory (per project convention — no
      filesystem mocking) and a fake `AssetDataSource`: successful write
      lands at the exact destination path; a failure mid-write leaves no
      partial file at the final destination (temp file cleaned up or simply
      never renamed)
- [ ] Write tests for the free-space pre-flight failure path
- [ ] Run `swift test` — must pass before task 5

### Task 5: `LocalFolderSyncCoordinator`

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderSyncCoordinator.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderSyncCoordinatorTests.swift`

- [ ] Implement `LocalFolderSyncCoordinator` with `sync(range:onProgress:)
      async throws -> SyncReport` and `resume(resources:onProgress:) async
      throws -> SyncReport` — same public shape `LiveSyncEngine`'s
      `Perform`/`Resume` closures expect, composing `AssetLibrary`,
      `LocalFolderPlanner`, and `LocalFolderWriter` the way `SyncCoordinator`
      composes its equivalents
- [ ] `sync`: enumerate library; for each resource compute
      `LocalFolderPlanner.expectedPath` and check `FileManager.fileExists`
      directly (no walk, works for any range) to build the upload plan; ONLY
      when `range == .all`, additionally walk the destination's `YYYY/MM/DD`
      tree to collect `actualPaths` and call `LocalFolderPlanner.planDeletes`
      — an incremental (`.modifiedSince`) sync skips the walk entirely and
      plans zero deletes, matching `SyncPlanner`'s own incremental-is-
      upload-only restriction; then run uploads serially via
      `LocalFolderWriter` reporting progress, run deletes via
      `FileManager.removeItem`, assemble `SyncReport`
- [ ] A per-asset `LocalFolderWriter.write` failure (mid-write I/O error,
      e.g. an ejected external drive) is caught and appended to
      `SyncReport.failed` like `SyncCoordinator`'s own upload-failure
      handling (see `SyncCoordinator.swift`'s `runUploads`) — it must NOT
      abort the whole `sync`/`resume` run or trigger an immediate in-process
      retry of that same file. A flaky/disconnected volume would otherwise
      turn one stuck large file into a retry-storm hammering the same write
      repeatedly within a single cycle; the existing failed-item/next-cycle
      retry model is what bounds that, so this coordinator must route
      through it rather than adding its own retry loop. `CancellationError`
      still propagates and stops the run, matching `SyncCoordinator`.
- [ ] `resume`: re-drive a saved resource list directly through
      `LocalFolderWriter` without re-planning, mirroring
      `SyncCoordinator.resume` — for each resource, recompute its
      destination via `LocalFolderPlanner.expectedPath` (same function the
      upload-plan path uses; `resume` does not get a precomputed path, it
      only has the `AssetResource` list, so this recomputation is required,
      not optional)
- [ ] `resume` must re-resolve `destinationRoot` via the Task 2 resolve
      helper at the START of `resume`, not reuse a root captured when the
      failed resource list was first queued. If the resolved root's bookmark
      data differs from what was active when the queued resources were
      planned (user switched the local folder in Settings between the
      failed attempt and this resume), fail the whole `resume` call with a
      typed error instead of writing some resources under the old root and
      some under the new one — silently splitting one resume batch across
      two destination folders is worse than asking for a fresh full sync
- [ ] Write tests using `FakeAssetLibrary` + a real temp directory: full
      sync uploads missing assets, skips already-present ones, deletes
      orphans; resume re-drives a list without re-scanning; a mid-write
      failure on one resource lands in `SyncReport.failed` and does not
      stop the remaining resources in the same run
- [ ] Write tests for the unavailable-destination-folder failure path
      (temp directory removed/made unwritable mid-run → typed error, not a
      crash)
- [ ] Run `swift test` — must pass before task 6

### Task 6: `isDestinationReady(.localFolder)`, its call sites, and entitlements

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncDestination.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNest.entitlements`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift` (call-site
  plumbing only — NOT the destination-branching logic, that's Task 8)
- Modify: `apple/macos/FilesNest/FilesNest/SettingsAnchorView.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/ShellStoresTests.swift`
  (or wherever `isDestinationReady` is currently tested — confirm at
  implementation time)

`isDestinationReady`'s signature changes (`localFolderStore` param added),
and it has 5 existing call sites: 4 in `FilesNestApp.swift`'s `perform`/
`resume`/`assess`/`isReady` closures, plus one in
`SettingsAnchorView.swift`. All 5 must be updated HERE, in the same task as
the signature change — deferring them to Task 8 would leave the app target
failing to compile at Task 7's `xcodebuild test` gate, one task before the
fix. Task 8 only needs to add the actual `LocalFolderSyncCoordinator`
branching inside those closures' bodies, not touch this plumbing again.

- [ ] Change `isDestinationReady`'s signature to accept a `localFolderStore:
      any LocalFolderStore` parameter; its `.localFolder` case calls the
      Task 2 resolve helper, brackets the check with
      `startAccessingSecurityScopedResource()`/
      `stopAccessingSecurityScopedResource()` around the resolved URL for
      the duration of the existence/writability check ONLY (a short-lived
      scope session, separate from Task 8's sync-pass session — see
      Technical Details), and returns `true` only if resolution succeeded
      AND the resolved URL exists and is writable
- [ ] In `FilesNestApp.swift`'s `init()`, construct a
      `UserDefaultsLocalFolderStore` instance alongside the existing stores
      (Task 8 will reuse this same instance for the coordinator branching)
      and pass it through all 4 `isDestinationReady(...)` call sites in the
      `perform`/`resume`/`assess`/`isReady` closures
- [ ] Add a `localFolderStore` property to `SettingsAnchorView` and pass it
      from `FilesNestApp.swift`'s `Window("settings-anchor")` construction;
      update its `isDestinationReady(...)` call site
- [ ] Verify against Apple's current documentation for `URL.bookmarkData(
      options: .withSecurityScope)` whether
      `com.apple.security.files.bookmarks.app-scope` is actually required —
      it is a distinct, older app-scoped-bookmark mechanism and is likely
      NOT needed for this flow (see Technical Details). Add
      `com.apple.security.files.user-selected.read-write` to
      `FilesNest.entitlements` regardless; add `bookmarks.app-scope` ONLY if
      this verification step shows it's actually required — do not add it
      speculatively
- [ ] Write tests for `isDestinationReady(.localFolder)`: no bookmark →
      false; valid bookmark resolving to an existing writable dir → true;
      bookmark resolving to a missing/unwritable path → false; stale
      bookmark that the resolve helper successfully refreshes → true
- [ ] Run `swift test` AND `xcodebuild -project FilesNest.xcodeproj -scheme
      FilesNest test` — both must pass before task 7 (the app target must
      compile cleanly here, not just the FilesNestCore package)

### Task 7: Folder picker UI

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/SettingsModel.swift`
- Modify: `apple/macos/FilesNest/FilesNest/SettingsView.swift`

- [ ] `SettingsModel`: add a `LocalFolderStore` dependency, a published
      property for the currently-selected folder's display path (resolved
      from the bookmark, or nil), and a method that opens `NSOpenPanel`
      (directories only, `canChooseFiles = false`, `canChooseDirectories =
      true`), creates the security-scoped bookmark from the chosen URL, and
      saves it via `LocalFolderStore`
- [ ] **Wire destination changes to `engine.reconcile()`.** Currently
      `destination`'s `didSet` only calls `destinationStore.save(destination)`
      — it never calls `onSaved` (which `FilesNestApp.swift` wires to
      `appModel.restart() → engine.reconcile()`; today only `connect()`'s
      success path triggers it). Without this, switching the destination
      picker takes effect only on the NEXT sync cycle rather than cancelling
      an in-flight run immediately, contradicting the settled "switching
      cancels the old destination's in-flight run" design (this was
      `docs/design/20260826-settings-window-and-destination.md` §6 Open Item
      1, deferred to this ticket — resolve it here). Call `onSaved?()` from
      `destination`'s `didSet` AND from the new folder-selection method's
      completion (picking a new folder while `.localFolder` is already
      selected doesn't change `destination`'s value, so it needs its own
      trigger). `doReconcile`'s existing not-ready handling
      (`resetToSignedOut()`) already covers the case where `.localFolder` is
      selected but no folder has been chosen yet — no new engine-side logic
      needed, only these two call sites
- [ ] `SettingsView`: replace `localFolderPlaceholder` with a real picker —
      "Choose Folder…" button, display the currently-selected path (or "No
      folder selected"), and a way to change it
- [ ] Write `SettingsModelTests` coverage for the new folder-selection
      method using a fake/injectable panel-presenting seam (do not launch a
      real `NSOpenPanel` in unit tests — mirror however
      `SettingsModelTests.swift` already fakes `ConnectionProbe`/stores for
      the server-settings flow)
- [ ] Write a test confirming `onSaved` fires when `destination` changes,
      and confirming it fires again after a successful folder selection
- [ ] Run `xcodebuild -project FilesNest.xcodeproj -scheme FilesNest test`
      — must pass before task 8

### Task 8: Wire the composition root

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`

- [ ] Reuse the `LocalFolderStore` instance already constructed in Task 6
      (do not create a second one)
- [ ] In the `perform` and `resume` closures (lines 27-100), branch on
      `destinationStore.load()`: `.server` keeps today's
      `ServerClient`/`AssetUploader`/`SyncCoordinator` path; `.localFolder`
      calls the Task 2 resolve helper, brackets the WHOLE sync/resume call
      with its own `startAccessingSecurityScopedResource()`/
      `stopAccessingSecurityScopedResource()` pair (a separate, longer-lived
      scope session from Task 6's short readiness-check session — see
      Technical Details), and builds a `LocalFolderSyncCoordinator` instead
- [ ] Update the `isReady` closure the same way (already delegates to
      `isDestinationReady`, which Task 6 already made destination-aware —
      confirm no further change needed here beyond passing the right
      dependencies)
- [ ] The `assess` closure's server-diff logic (`SyncPlanner.plan` against
      `GET /uploads` records) has no local-folder equivalent needed. There is
      deliberately no `LocalFolderPlanner.plan(...)` (Task 3 rejected a
      combined uploads+deletes method) — for `.localFolder`, build the
      assessment counts the same way `sync` does in Task 5: per-asset
      `expectedPath` + `fileExists` for the pending/backed-up counts, and
      (for `.all` only) the destination-tree walk + `planDeletes` if a
      failed-count is needed. Do not introduce a new `LocalFolderPlanner`
      API for this — reuse the exact functions Task 5 already has
- [ ] Extract the `.server`-vs-`.localFolder` branch condition itself
      (reading `destinationStore.load()` and deciding which coordinator
      family to build) into a small, pure, testable function or switch —
      e.g. `func coordinatorKind(for destination: SyncDestination) ->
      CoordinatorKind` — rather than leaving the decision inline inside the
      four `FilesNestApp.swift` closures. This is the single highest-risk
      line in the whole feature (wrong branch = a `.localFolder` user's
      sync silently uses the server path, or vice versa) and it must not be
      the one piece of Task 8 covered only by manual verification. Unit-test
      this extracted function directly for both destination cases; the
      remaining closure body (actually constructing `ServerClient` vs
      `LocalFolderSyncCoordinator` and bracketing the security-scoped
      session) may still fall back to manual verification per the note
      below, since `FilesNestApp.init()` itself isn't easily unit-testable
- [ ] Write/update `FilesNestTests` coverage exercising the composition
      root's destination branch (confirm the existing test structure for
      this file — it may be exercised only via `AppModel`/integration-style
      tests rather than directly, given `FilesNestApp.init()` isn't easily
      unit-testable as SwiftUI `App` — note in the plan if this task ends up
      being manual-verification-only for the composition root specifically,
      and rely on Tasks 1-6's unit coverage plus the extracted branch
      function's tests above for the underlying logic)
- [ ] Run `xcodebuild -project FilesNest.xcodeproj -scheme FilesNest test`
      — must pass before task 9

### Task 9: Verify acceptance criteria
- [ ] Verify every requirement from Overview/Technical Details is
      implemented: always-suffixed naming, no `incoming`/`organized`
      staging, delete mirroring, `.all`-only deletes, serial writes,
      free-space pre-flight, unavailable-folder error surfacing,
      destination-switch cancellation behavior
- [ ] Run full test suite: `swift test` (from `apple/FilesNestCore/`)
- [ ] Run `xcodebuild -project FilesNest.xcodeproj -scheme FilesNest test`
- [ ] Verify test coverage matches the project's existing density for
      comparable components (`SyncCoordinatorTests`, `AssetUploaderTests`)

### Task 10: [Final] Update documentation
- [ ] Update `docs/design/20260826-settings-window-and-destination.md`'s
      "Open items" §1 (destination switch mid-flight) — mark it resolved per
      this plan's Technical Details (independent per-destination tracking,
      switching cancels the old destination's in-flight run)
- [ ] Update `apple/CLAUDE.md` if this introduces a new pattern worth
      documenting (e.g. the no-shared-protocol convention now has two real
      examples instead of zero)
- [ ] Move this plan to `docs/plans/completed/`

## Post-Completion

**Manual verification** (no automated UI test harness for this app):
- Folder picker flow end-to-end: choose a folder, confirm it persists across
  an app relaunch (bookmark resolves correctly).
- Revoke/move the chosen folder externally (e.g. eject an external drive),
  confirm sync surfaces a clear error rather than crashing or hanging.
- Switch Server → Local Folder mid-sync and confirm the old destination's
  run is cancelled cleanly (per `LiveSyncEngine.doReconcile`'s existing
  behavior for a server-URL change).
- Confirm Live Photo JPEG/MOV pairs both land under the same date directory
  with distinct `SafeID` suffixes (per their distinct `ResourceKey`s).
- Confirm a deleted Photos-library asset's corresponding local-folder file
  is removed on the next full sync.
