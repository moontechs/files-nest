# Server: Always Append Identifier Suffix to Organized Filenames

## Overview

`filestore.Mover.PlanDestination` currently appends a `_<backendID>` suffix to
an organized file's name **only when a collision is detected** (an
`os.Stat` on the computed `organized/YYYY/MM/DD/<filename>` path finds
something already there). This plan changes that to **always** append a
suffix, and swaps its source from `upload.BackendID` (tusd's opaque,
per-upload-attempt ID) to `upload.ID` (the already-computed, stable
`SafeID(resourceKey)` — a SHA-256/base64url hash of `<localIdentifier>#<kind>`,
per `server/internal/api/ids.go`).

This is the server-side half of a two-ticket design produced during a
brainstorm session for local-folder sync (a companion `apple/` ticket, planned
separately). Local-folder sync writes files with no server/DB behind it, so
"has this asset already been synced" can only be answered by checking file
existence on disk — which is only reliable if the filename always embeds a
stable per-asset identifier. Since local-folder must always-suffix for its
own correctness, the server is changed to match, so both destinations produce
identical filenames for the same asset. See the two ADRs this session
produced: `docs/adr/0008-local-folder-sync-writes-directly-no-subprocess.md`
and the naming-unification ADR added by this plan (Task 5).

**Explicitly prospective only.** Files already organized before this change
ships keep their current names (no suffix, or `_<backendID>`) forever — no
migration, no rename pass over existing deployments. Old and new naming
schemes coexist on disk indefinitely for pre-existing content; only files
organized *after* this ships get `_<id>`.

## Context (from discovery)

- `server/internal/filestore/mover.go`:
  - `PlanDestination(creationDate, createdAt, filename, backendID string) PlanDestResult` —
    computes `organized/YYYY/MM/DD/<filename>`, then `os.Stat`s it; only on a
    hit does it insert `_<backendID>` before the extension (lines ~170-201).
  - `(m *Mover) MoveFile(...)` (method, distinct from the package-level
    `MoveFile` func) computes `Deduplicated` by comparing against
    `OrganizedPath`'s un-suffixed base path — this becomes always-`true` and
    meaningless once suffixing is unconditional. No caller outside this
    package's own tests uses this method or `MoveResult.Deduplicated`
    (confirmed via repo-wide grep) — candidate for removal rather than
    fixing, per YAGNI.
  - `OrganizedPath(creationDate, filename string) (string, string)` — the
    plain (no-suffix) path builder, used internally by the method above for
    the dedup comparison. Also candidate for removal if its only caller goes
    away.
- `server/internal/api/handlers.go:1040` (`moveCompletedFile`) — the actual
  production call site: `h.mover.PlanAndMove(srcPath, upload.CreationDate,
  upload.CreatedAt, upload.Filename, upload.BackendID, saveIntent)`. Change
  `upload.BackendID` → `upload.ID`. `upload.ID` is already in scope here
  (it's the `store.Upload` struct field, already `SafeID(resourceKey)` per
  `docs/architecture.md`'s BadgerDB schema section) — no new plumbing needed.
- **`store.Store.ReRegister`** (`server/internal/store/uploads.go:507`)
  reassigns `upload.BackendID` on a `backend_lost` recovery while
  `upload.ID` stays fixed for the record's lifetime. This is exactly why
  `moveCompletedFile`'s doc comment (`handlers.go:971-991`) justifies
  reusing a persisted `CompletionIntent`'s destination verbatim on retry
  instead of recomputing it: recomputing with the OLD collision-only logic
  would `os.Stat` the already-moved file, treat it as a collision, and
  suffix with the CURRENT `backendID` — which may differ from the first
  attempt's if `ReRegister` ran in between, producing a different path than
  where the file actually lives. Once the suffix source is `upload.ID`
  (stable across `ReRegister`) AND suffixing is unconditional (no
  disk-state-dependent branch at all), recomputing the destination is fully
  deterministic and always reproduces the same path — this specific
  divergence risk goes away. The doc comment's reasoning is specific to the
  old mechanism and becomes stale once this ships; Task 3 must update it
  rather than leave it describing a scenario the new code can no longer
  produce.
- `server/internal/api/recovery.go:241` calls the package-level
  `filestore.MoveFile(intent.Src, intent.Dst, intent.CreationDate)` directly
  with an already-planned destination (from a persisted `CompletionIntent`)
  — unaffected by this change, since the intent's `Dst`/`DstRel` were
  computed once by `PlanAndMove` before being persisted.
- `server/internal/filestore/mover_test.go` and `mover_internal_test.go` —
  extensive existing coverage of the collision-only behavior (e.g.
  `TestPlanDestination_CollisionInsertsBackendID`,
  `TestPlanDestination_PreservesExistingOnCollision`,
  `TestMoveFile_DeduplicatesWhenDestinationExists`,
  `TestMoveFile_SameDateFilenameDifferentBackendIDs`,
  `TestPlanDestinationWithCollisionThenMoveFile`, and several
  `TestMoveFile_Success`/`TestPlanDestination_*` cases that assert a *plain*
  (un-suffixed) filename in the non-collision case) — all need updating to
  reflect always-append.
- Related but out of scope for this ticket: `internal/orphans/` (gc-orphans
  scan) — reconciles `organized/` files against DB records by path already
  stored on each record (`OrganizedPath` field), not by recomputing the
  expected path, so it is unaffected by the suffix rule changing.

## Development Approach

- **Testing approach**: Regular (code first, then tests) — this codebase's
  existing `filestore` tests are already comprehensive; extend/update them
  rather than write new ones test-first.
- Complete each task fully (including its tests, and `make lint`) before
  moving to the next.
- Follow `server/CLAUDE.md`: table-driven Go tests, `make test` and `make
  lint` (zero-tolerance) before considering a task done.
- **CRITICAL: update this plan file when scope changes during implementation.**

## Solution Overview

`PlanDestination`'s signature keeps a fourth string parameter (renamed from
`backendID` to `id` for clarity, since it's no longer specifically a backend
ID) but the collision-detection `os.Stat` branch is removed — the suffix is
now unconditional. `PlanAndMove`, `(m *Mover) MoveFile`, and their doc
comments are updated to match. The one production call site
(`handlers.go`'s `moveCompletedFile`) switches from passing
`upload.BackendID` to `upload.ID`.

## Technical Details

- New `PlanDestination` behavior: `organized/YYYY/MM/DD/<filename>` becomes
  `organized/YYYY/MM/DD/<filename-stem>_<id><ext>` unconditionally. No
  `os.Stat` call needed in the planning step at all — this also removes a
  filesystem read from the hot path.
- `id` is passed through as an opaque string by `filestore` (the package
  doesn't need to know it's `SafeID(resourceKey)` specifically — same
  separation of concerns as today, where it didn't need to know `backendID`
  came from tusd).
- **No collision fallback.** Two *new* records cannot compute the same final
  `<filename>_<id>` path: `id` is `SafeID(resourceKey)`, unique per record by
  construction (distinct `resourceKey`s hash to distinct IDs), and the
  `moveMu` lock already forecloses the TOCTOU window that mattered under the
  old collision-detection scheme. There is no identified scenario where two
  live records collide on the new path shape, so no `os.Stat`-and-retry
  fallback is added. (An old, pre-change file happening to already occupy a
  path shaped like `<filename>_<id>` is not possible either: old files never
  had this suffix shape, by construction of the prospective-only rollout.)

## What Goes Where

- **Implementation Steps**: code changes to `filestore`/`handlers.go`, their
  tests, the new ADR, and the `CONTEXT.md` glossary update.
- **Post-Completion**: none — this is a self-contained server-side change
  with no external/manual verification beyond the existing `make e2e` suite.

## Implementation Steps

### Task 1: Always-append the suffix in `PlanDestination`

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/filestore/mover_test.go`
- Modify: `server/internal/filestore/mover_internal_test.go`
- Modify: `server/internal/api/ids_test.go`

- [ ] Add (if not already covered) assertions in `ids_test.go` that
      `SafeID` produces these exact ground-truth vectors — the same literal
      values are asserted independently in the companion apple plan's Task 1
      (`docs/plans/20260829-apple-local-folder-sync.md`), so both
      implementations are checked against one shared ground truth rather
      than each trusting its own output:
      - `"AAAA-BBBB-CCCC-DDDD#photo"` → `"QEzizTsZbhLknu3BxIqchpZg6BiVPEM7p8HYKhmIpCc"`
      - `"AAAA-BBBB-CCCC-DDDD#pairedVideo"` → `"FlwSC0rmUccfKH1nEq9BAo3lHk_SeclzxNeV9Sp_-kw"`
      - `""` → `"47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"`
- [ ] In `PlanDestination`, remove the `os.Stat`-gated collision branch
      entirely (no fallback replaces it — see Technical Details' "No
      collision fallback" note); always compute `rel`/`abs` as
      `organized/YYYY/MM/DD/<stem>_<id><ext>` (rename the `backendID`
      parameter to `id`)
- [ ] Update `PlanDestination`'s doc comment to describe unconditional
      suffixing instead of collision-only, and to state plainly why no
      collision fallback exists (unique-by-construction `id`, no identified
      collision scenario)
- [ ] Check filename length: with the suffix always present (43-char
      `SafeID` + `_`), confirm `mover.go`'s `maxPathSegmentLen = 200` (or any
      other filesystem name-length handling) still holds for realistic
      filenames — add a test with a long original filename to confirm the
      final `<stem>_<id><ext>` doesn't silently exceed a filesystem limit
- [ ] Update existing collision-only tests
      (`TestPlanDestination_CollisionInsertsBackendID`,
      `TestPlanDestination_PreservesExistingOnCollision`,
      `TestPlanDestination_CollisionWithNoExtension`,
      `TestPlanDestination_MultipleDotsExtension`, and any
      `TestPlanDestination_*`/`TestMoveFile_*`/`TestOrganizedPath_*` case
      asserting a plain, un-suffixed filename in the non-collision case) to
      assert the new always-suffixed paths
- [ ] Run `make test` and `make lint` — must pass before task 2

### Task 2: Remove now-dead `Deduplicated`/`(m *Mover) MoveFile` method if unused

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/filestore/mover_test.go`

- [ ] Re-confirm (repo-wide grep) that `(m *Mover) MoveFile` (the method) and
      `MoveResult.Deduplicated` have no callers outside this package's own
      tests
- [ ] If confirmed unused: delete the method, `MoveResult`, and
      `OrganizedPath`'s only remaining caller-path if it too becomes unused;
      remove their now-orphaned tests. If `OrganizedPath` is still used
      elsewhere (e.g. by another package needing the plain-path shape for a
      non-suffix reason), keep it and only remove the dedup-comparison usage
- [ ] If NOT confirmed unused (a caller exists this discovery missed): STOP
      and flag it back rather than silently patching — this task's premise
      (safe to delete) would be wrong and the plan needs re-scoping, not a
      quick workaround
- [ ] Run `make test` and `make lint` — must pass before task 3

### Task 3: Switch the production call site to `upload.ID`

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/handlers_test.go` (or wherever
  `moveCompletedFile`/completion-flow tests live — confirm exact file at
  implementation time)

- [ ] In `moveCompletedFile` (`handlers.go:1040`), change the
      `PlanAndMove(...)` call's suffix argument from `upload.BackendID` to
      `upload.ID`
- [ ] Before rewriting the doc comment, grep every read of
      `CompletionIntent.BackendID` (it's set from `upload.BackendID` at
      `saveIntent` time, `handlers.go:1005`) and confirm none of them compare
      it against a *current* `upload.BackendID` to detect a stale intent —
      if such a comparison exists, document whether it remains correct now
      that the destination path no longer depends on `backendID` at all; if
      none exists, note that `CompletionIntent.BackendID` is now purely
      informational
- [ ] Update the `moveCompletedFile` doc comment (`handlers.go:971-991`,
      specifically the "Retry safety" paragraph) — it currently justifies
      completion-intent reuse by describing a `backend_id`-driven path
      divergence across `ReRegister` retries (see the Context section's
      `ReRegister` note above) that can no longer happen once the suffix is
      `upload.ID`-based and unconditional. Rewrite it to state plainly that
      recomputing the destination is now fully deterministic (no disk-state
      dependency, no `backendID` dependency), and that intent-reuse is kept
      as a deliberate simplicity/consistency choice (avoids recomputing on
      every retry) rather than as a correctness requirement for this
      specific failure mode — do not just delete the paragraph, a future
      reader needs to know why intent-reuse still exists
- [ ] Grep for any other test fixtures asserting the old `_<backendID>`
      suffix shape in completion/finalize handler tests and update them
- [ ] Write/update a handler-level integration test (`httptest`, per
      `docs/architecture.md`'s testing-approach section) asserting a
      completed upload's organized filename ends in `_<upload.ID>`, not
      `_<backendID>`
- [ ] Run `make test` and `make lint` — must pass before task 4

### Task 4: End-to-end verification against the live stack

**Files:**
- Modify: `server/e2e/` (whichever existing e2e test exercises the full
  upload → complete → organized-file flow — confirm exact file at
  implementation time)

- [ ] Update or extend the relevant e2e test to assert the organized file's
      final path includes the `_<upload.ID>` suffix
- [ ] Run `make e2e` — must pass before task 5

### Task 5: ADR and glossary updates

**Files:**
- Create: `docs/adr/0009-unify-organized-filename-suffix.md`
- Modify: `CONTEXT.md`

- [ ] Write ADR 0009 (next available number after 0008): what changed
      (always-append `_<id>` instead of collision-only `_<backendID>`), why
      (local-folder sync's correctness requirement, established during the
      brainstorm session referenced in Overview, plus the deliberate choice
      for cross-destination filename parity), and the explicit
      prospective-only/no-migration decision and its accepted consequence
      (two naming schemes coexist indefinitely on already-deployed servers)
- [ ] Update `CONTEXT.md`'s "Organized path" entry to describe the final,
      unified rule precisely (it currently describes pre-change,
      collision-only behavior plus an earlier edit anticipating
      local-folder's own always-suffixed scheme as a *different* shape —
      reconcile into one accurate description: both destinations now use
      the identical `_<id>` rule, only the root path shape differs
      — `organized/YYYY/MM/DD/...` under the server's storage root vs.
      `YYYY/MM/DD/...` directly under a local-folder destination)
- [ ] No tests needed for this task (docs-only)

### Task 6: Verify acceptance criteria
- [ ] Verify every already-organized-file naming assumption elsewhere in the
      codebase (orphans scan, any other path-recomputation) was checked and
      is unaffected (per the Context section's orphans-scan note — confirm
      this holds, don't just assume it)
- [ ] Run full test suite: `make test`
- [ ] Run `make e2e`
- [ ] Run `make lint` (zero-tolerance) with no findings
- [ ] Confirm no code path still assumes collision-only suffixing

### Task 7: [Final] Update documentation
- [ ] Update `server/README.md` if it documents the organized-file naming
      convention
- [ ] Update `docs/architecture.md`'s "File organization" section to
      describe the new always-suffixed convention
- [ ] Move this plan to `docs/plans/completed/`

## Post-Completion

None — this ticket is fully self-contained within `server/` and its own test
suites; no external system or manual-verification step is required beyond
the `make e2e` run already covered in Task 4.
