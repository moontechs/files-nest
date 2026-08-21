# Real file timestamps on organized files + ctime-based orphan guard

## Overview

Files moved into `organized/` currently keep the filesystem `mtime` they
got from `os.Rename`/`io.Copy` at upload time — the client-supplied
`creation_date` (already used to build the `organized/YYYY/MM/DD/...`
path) is never applied to the file itself. This plan makes the on-disk
`mtime`/`atime` match the real `creation_date` via `os.Chtimes`, so
filesystem tools, backups, and directory listings show the photo's actual
capture date.

That change breaks an existing safety mechanism: `orphans.FilterMinAge`
uses a candidate's `mtime` as a race guard, assuming it's close to "now"
(the moment the file was written). Once `mtime` is driven by client input
it can be years in the past, defeating the guard for the common case (old
backed-up photos) the instant they land on disk. This plan replaces that
guard's timestamp source with `ctime` (inode status-change time), which
`Chtimes` cannot spoof — see `docs/adr/0006-ctime-based-orphan-age-guard.md`
for the full rationale, the local-POSIX-filesystem assumption this rests
on, and rejected alternatives.

Client-supplied `creation_date` is presently only checked for
*parseability*, not plausibility — a known failure mode for the exact
data source this plan targets: camera/phone clocks with a dead RTC
battery routinely produce epoch (1970) or far-future EXIF dates. Because
`Chtimes` writes are permanent filesystem state with no separate "this
looked suspicious" record, this plan adds a sanity-range clamp so
implausible dates fall back to upload-time behavior instead of being
burned into the file forever (see Task 4).

## Context (from discovery)

- **Files/components involved**:
  - `server/internal/filestore/mover.go` — date parsing (3x duplicated),
    `PlanDestination`, `MoveFile` (method + free function), `PlanAndMove`,
    `MoveToPlaned`
  - `server/internal/store/completion.go` — `CompletionIntent` struct
  - `server/internal/api/handlers.go` — completion intent creation (~line 1003)
  - `server/internal/api/recovery.go` — `moveIntentSource` (crash recovery move path)
  - `server/internal/orphans/scan.go` — `Candidate` struct, `Scan`
  - `server/internal/orphans/filter.go` — `FilterMinAge`
  - `server/main.go` — `orphanMinCandidateAge` (3h default, unchanged)
- **Related patterns found**: date parsing already tries `time.RFC3339`
  then falls back to `"2006-01-02"` in three separate functions
  (`OrganizedPath`, `datePathSegments`, `isParseableDate`); best-effort
  error handling (log-and-continue) is the established pattern in
  `orphans.Apply` and `server/main.go` for non-fatal I/O failures.
- **Dependencies identified**: `filestore` and `orphans` are currently
  "pure" packages with no `"log"` import — `filestore` gains its first
  `log.Printf` call in this plan (deliberate, see Task 4). `orphans` gains
  two build-tag files (`ctime_linux.go`, `ctime_darwin.go`) since the
  server has no Windows target.

## Development Approach

- **Testing approach**: Regular (code first, then tests per task)
- Complete each task fully before moving to the next
- Make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** — no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- Run tests after each change
- Maintain backward compatibility (existing `MoveFile`/`PlanDestination` callers outside this plan's scope, if any, must keep working)

## Testing Strategy

- **Unit tests**: required for every task (Go `testing` package, table-driven where the existing test files already use that style — `mover_test.go`, `filter_test.go`, `scan_test.go` all do)
- **E2E tests**: `server/e2e/` exists (`fixtures_test.go`, `listing_test.go`) but targets HTTP-level upload/listing behavior, not filesystem timestamps — out of scope unless a task explicitly calls it out (none do; this is a filesystem/internal-package change, not an API surface change)

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

Eight feature tasks (1-8) plus a verification task, a mutation-testing
pass, and a documentation task (9-11) — eleven tasks total, each
independently testable, in dependency order: date-parsing dedup →
`PlanDestResult.DateUsed` → `CompletionIntent` schema → `MoveFile` gains
`Chtimes` → recovery path wiring → `ctime` platform helper →
`orphans.Candidate` rename → `FilterMinAge` ctime semantics → verify →
mutation test → docs. This order means every task's tests can pass in
isolation without forward references to a not-yet-built piece.

Key design decisions (from grill-with-docs + brainstorm sessions, all
confirmed with the user):

1. **Single resolved-date field, no fallback logic inside `MoveFile`.**
   `PlanDestination` already computes the best-available date string for
   path construction; `PlanDestResult.DateUsed` exposes that same
   already-resolved value instead of re-deriving fallback logic a second
   time in the mover.
2. **`ctime` over `mtime` for the orphan-scan race guard**, because
   `Chtimes` can set `mtime`/`atime` but never `ctime` — see ADR 0006.
3. **`CompletionIntent` gains `CreationDate`** so the crash-recovery move
   path (`recovery.go:moveIntentSource`) gets correct `Chtimes` too,
   instead of silently degrading to "recovery time" for files recovered
   after a crash.
4. **`Chtimes` failures are best-effort** (logged, not fatal) — the move
   itself already succeeded; a missing/wrong timestamp is a lesser defect
   than losing the uploaded file.
5. **Backfilling `mtime` for files already in `organized/`** is explicitly
   out of scope — see Post-Completion. This is a *permanent* decision, not
   a deferral: post-ship, `organized/` holds two populations of files
   where `mtime` means structurally different things (pre-change: upload
   time; post-change: capture time), indistinguishable from each other
   without cross-referencing `ctime` or the DB. Any future tooling built
   on `mtime` (a "recently added" view, a backup diff) inherits this
   ambiguity unless a backfill is scoped later.
6. **Implausible `creation_date` values are clamped, not trusted
   verbatim.** A parseable-but-insane date (epoch, far future) is common
   EXIF corruption, not a hypothetical — see Overview.
7. **Date-only `creation_date` (`YYYY-MM-DD`, no time/zone) parses to UTC
   midnight** — pre-existing behavior for path construction, but this
   plan newly makes it visible in `mtime`/`atime` on disk: a file dated
   this way shows as the *previous* calendar day in any negative-UTC-offset
   timezone's file browser. Not fixed by this plan (would require guessing
   a timezone with no basis to do so) — documented here so it's a known
   tradeoff, not a surprise.

## Technical Details

- `parseCreationDate(s string) (time.Time, bool)` — new shared helper,
  tries `time.RFC3339` then `"2006-01-02"`, replacing duplicated parsing
  in `OrganizedPath`, `datePathSegments`, `isParseableDate`.
- `PlanDestResult` gains `DateUsed string`.
- `CompletionIntent` gains `CreationDate string` (JSON tag `creation_date`).
- Sanity-range clamp applied only at the `Chtimes` call site (does not
  change path-building behavior, which is unrelated pre-existing scope):
  ```go
  var (
      minSaneCreationDate = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
      maxSaneCreationDateSkew = 24 * time.Hour // allow for client clock skew
  )

  func isSaneCreationDate(t, now time.Time) bool {
      return !t.Before(minSaneCreationDate) && !t.After(now.Add(maxSaneCreationDateSkew))
  }
  ```
  A date outside this window is treated the same as an unparseable one:
  `Chtimes` is skipped, file keeps upload-time `mtime`, no log line (this
  is an expected/common case for this data source, not an error).
- Free function `filestore.MoveFile(src, dst, creationDate string) error`
  (was `MoveFile(src, dst string) error`) — parses `creationDate`, checks
  `isSaneCreationDate`, calls `os.Chtimes(dst, t, t)` on success, logs on
  `Chtimes` error, silently skips on unparseable/empty/out-of-range input.
- `orphans.Candidate.ModTime time.Time` → `orphans.Candidate.CTime time.Time`.
- New unexported `ctime(info fs.FileInfo) (time.Time, error)` in
  `orphans`, platform-specific (`//go:build linux` / `//go:build darwin`).
- `orphans.FilterMinAge` body: `now.Sub(c.CTime) >= minAge` (was `c.ModTime`).

## What Goes Where

- **Implementation Steps** (`[ ]` checkboxes): all code/test changes below — entirely within this repo.
- **Post-Completion** (no checkboxes): backfill of pre-existing `organized/` files' timestamps — needs its own scoping/risk discussion, not part of this change.

## Implementation Steps

### Task 1: Extract shared `parseCreationDate` helper

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/filestore/mover_internal_test.go` (unexported helper, so tests live in the `filestore` package, not the external `filestore_test` `mover_test.go`)

- [x] add `func parseCreationDate(s string) (time.Time, bool)` to `mover.go`, trying `time.Parse(time.RFC3339, s)` then `time.Parse("2006-01-02", s)`
- [x] rewrite `OrganizedPath` (lines ~71-100) to call `parseCreationDate` instead of its own inline `time.Parse` calls, preserving existing fallback-to-`SafePathSegment` behavior on failure
- [x] rewrite `datePathSegments` (~line 466) to call `parseCreationDate`
- [x] rewrite `isParseableDate` (~line 487) to call `parseCreationDate` and return the `ok` bool
- [x] write table-driven tests for `parseCreationDate`: valid RFC3339, valid `YYYY-MM-DD`, RFC3339Nano, empty string, garbage string, whitespace-only string (plus RFC3339 offset and non-parsing RFC3339-no-seconds cases)
- [x] run existing `mover_test.go` suite (`TestOrganizedPath_*`, `TestMoveFile_*`) — must still pass unchanged, confirming the refactor preserves behavior
- [x] run tests — must pass before task 2

⚠️ baseline fix applied this iteration, not part of Task 1's scope but required for `go test ./...` to compile: `server/internal/orphans/filter_test.go` declared `package orphans_test` but referenced `Candidate`/`FilterMinAge` unqualified with no `orphans` import, so the whole orphans test package failed to build from the start. Added the import and qualified the two identifiers (Task 7/8 will rewrite the field names in this file).

### Task 2: Add `PlanDestResult.DateUsed`

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/filestore/mover_test.go`

- [x] add `DateUsed string` field to `PlanDestResult` struct
- [x] in `PlanDestination` (~line 170), assign `DateUsed: dateToUse` (the already-computed fallback value) when constructing the returned `PlanDestResult`
- [x] write test asserting `PlanDestination(...)` returns `DateUsed` equal to `creationDate` when valid
- [x] write test asserting `DateUsed` falls back to `createdAt` when `creationDate` is empty/unparseable (mirrors existing `TestOrganizedPath_UnparseableDateFallsBack` fixture data)
- [x] write test asserting `DateUsed` is the raw (possibly unparseable) string when neither `creationDate` nor `createdAt` parse — matches current fallback-to-raw-string behavior
- [x] run tests — must pass before task 3

### Task 3: Add `CompletionIntent.CreationDate`

**Files:**
- Modify: `server/internal/store/completion.go`
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/store/completion_test.go` (create if it doesn't exist)

- [x] add `CreationDate string` field (JSON tag `creation_date`) to `store.CompletionIntent`, positioned before `CreatedAt` (server bookkeeping time) to keep client-vs-server date fields visually grouped
- [x] in `handlers.go` (~line 1003), populate `CreationDate: plan.DateUsed` when constructing the `store.CompletionIntent{...}` literal (`plan` is the `PlanDestResult` from Task 2, already in scope at that call site)
- [x] write/update test verifying `SaveCompletionIntent`/`GetCompletionIntent` round-trips `CreationDate` correctly (Badger JSON marshal/unmarshal)
- [x] write test verifying the completion-intent construction in the handler populates `CreationDate` from `plan.DateUsed` (existing handler test fixture, or new one if none covers this path)
- [x] run tests — must pass before task 4

### Task 4: `MoveFile` sets real timestamps via `Chtimes`

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/filestore/mover_test.go`

- [x] add `"log"` import to `mover.go` (first use of logging in this package — deliberate, see ADR/brainstorm notes: best-effort `Chtimes` failures must be observable, not silently swallowed)
- [x] add `minSaneCreationDate`, `maxSaneCreationDateSkew`, and `isSaneCreationDate(t, now time.Time) bool` per Technical Details
- [x] change free function signature to `func MoveFile(src, dst, creationDate string) error`, keeping the existing rename/copy body unchanged
- [x] after a successful rename/copy, call `parseCreationDate(creationDate)`; on success, check `isSaneCreationDate(t, time.Now())`; if sane, call `os.Chtimes(dst, t, t)`, logging via `log.Printf("filestore: chtimes %s failed: %v", dst, err)` on error (non-fatal — function still returns `nil`)
- [x] on unparseable/empty/out-of-range `creationDate`, skip `Chtimes` entirely with no log line (expected case, not an error — see Overview on EXIF clock corruption)
- [x] update the three other in-package callers of the free `MoveFile` to pass a date: `(m *Mover) MoveFile` method passes its own `creationDate` param through directly; `PlanAndMove` passes `plan.DateUsed`; `MoveToPlaned` passes `plan.DateUsed` (from its `PlanDestResult` param)
- [x] update `server/internal/api/recovery.go:241` (`moveIntentSource`) call site to pass `intent.CreationDate` — full wiring happens in Task 5, but the signature change means this call site must compile; pass `intent.CreationDate` now since Task 3 already added the field
- [x] note (no code change): `Chtimes` runs while `PlanAndMove`/`MoveToPlaned`/the method `MoveFile` hold `m.moveMu`, the single mutex serializing all moves (`mover.go:36-44`) — acceptable since `Chtimes` is a fast local syscall, but flag in a code comment at the call site so a future slow-storage change (e.g. the network-mount case ADR 0006 warns against) doesn't silently turn this into a throughput bottleneck nobody expected
- [x] write test: `MoveFile` with a valid `creationDate` — resulting file's `os.Stat(dst).ModTime()` matches the parsed time (within filesystem timestamp resolution, e.g. assert `.Equal()` after truncating to `time.Second`)
- [x] write test: `MoveFile` with empty `creationDate` — move succeeds, no panic, `Chtimes` not called (assert resulting `mtime` is close to `time.Now()`, i.e. upload-time behavior unchanged)
- [x] write test: `MoveFile` with garbage `creationDate` — same as empty case, move still succeeds
- [x] write test: `MoveFile` with an out-of-range but *parseable* `creationDate` (epoch `1970-01-01T00:00:00Z` and a far-future date like `9999-01-01T00:00:00Z`) — move succeeds, `Chtimes` not called, resulting `mtime` stays close to `time.Now()` (proves the clamp, not just the parser)
- [x] update existing `TestMoveFile_Success`, `TestMoveFile_CreatesDestinationDirectory`, and the deduplication tests (`TestMoveFile_DeduplicatesWhenDestinationExists`, `TestMoveFile_MultipleDeduplications`, `TestMoveFile_DeduplicationWithExtension`, `TestMoveFile_DeduplicationNoExtension`, `TestMoveFile_DeduplicationWithMultipleDots`) to assert the moved file's `mtime` matches the `creationDate` string each test already passes in
- [x] run tests — must pass before task 5

### Task 5: Wire crash-recovery path to real timestamps

**Files:**
- Modify: `server/internal/api/recovery.go`
- Modify: `server/internal/api/recovery_test.go`

- [x] confirm `moveIntentSource` (line ~233-247) passes `intent.CreationDate` to `filestore.MoveFile` (should already compile from Task 4; this task is the dedicated test coverage for the recovery path specifically)
- [x] write test: construct a `CompletionIntent` with `CreationDate` set, run recovery's move path, assert the resulting file's `mtime` reflects `CreationDate`, not recovery-time
- [x] write test: construct a `CompletionIntent` with empty `CreationDate` (e.g. an intent persisted before this feature shipped, simulating upgrade-in-place) — recovery move still succeeds, file gets recovery-time `mtime` (graceful degradation, not a crash)
- [x] run tests — must pass before task 6

### Task 6: Platform-specific `ctime` helper in `orphans`

**Files:**
- Create: `server/internal/orphans/ctime_linux.go`
- Create: `server/internal/orphans/ctime_darwin.go`
- Create: `server/internal/orphans/ctime_test.go`

- [ ] create `ctime_linux.go` with `//go:build linux`, package `orphans`, `func ctime(info fs.FileInfo) (time.Time, error)` reading `info.Sys().(*syscall.Stat_t)` and returning `time.Unix(st.Ctim.Sec, st.Ctim.Nsec)`; return a descriptive error on failed type assertion
- [ ] create `ctime_darwin.go` with `//go:build darwin`, identical shape but `st.Ctimespec.Sec`/`st.Ctimespec.Nsec`
- [ ] write test: create a real temp file, stat it, call `ctime(info)`, assert the returned time is within a few seconds of `time.Now()` and `err == nil`
- [ ] write test (**this is the regression proof for the whole plan, not just a `ctime` unit test**): touch the file's `mtime` via `os.Chtimes` to a date far in the past, call `ctime(info)` again on a fresh stat, assert the returned `ctime` is still recent — this is the concrete demonstration that `Chtimes` cannot move `ctime` backward, i.e. that the guard this plan builds (Task 8) actually holds against the exact attack (client-controlled `mtime`) it's designed to defend against
- [ ] run `go build ./...` and `go test ./server/internal/orphans/...` on the current dev platform (confirms the build-tag file for the host OS compiles; CI should already cover both if it runs on Linux — verify in Task 9's acceptance pass)
- [ ] run tests — must pass before task 7

### Task 7: `Candidate.ModTime` → `Candidate.CTime`

**Files:**
- Modify: `server/internal/orphans/scan.go`
- Modify: `server/internal/orphans/scan_test.go`
- Modify: `server/main.go`

- [ ] rename `Candidate.ModTime` field to `Candidate.CTime` in `scan.go`
- [ ] update the doc comment on `Candidate` (currently says "ModTime is populated only for files actually flagged as candidates...") to describe `CTime` and why (link to ADR 0006)
- [ ] in the `WalkDir` callback (~lines 97-106), after `d.Info()` succeeds, call `ctime(info)`; on error append to `result.Errors` (same pattern as the existing `d.Info()` error branch) and skip the candidate (`return nil`) rather than falling back to `ModTime` — an undetermined age must never be treated as "old enough"
- [ ] on success, populate `Candidate{Path: path, CTime: ct}`
- [ ] in `server/main.go`'s `gcOrphansCycle` (~line 246-276), the existing `for _, e := range result.Errors { log.Printf(...) }` loop already surfaces every individual `ctime`/stat failure — add one aggregate line logged when `len(result.Errors) > 0` (e.g. `log.Printf("gc-orphans: %d scan errors this cycle (see above)", len(result.Errors))`) so a *sustained* run of `ctime`-read failures (per ADR 0006's network-filesystem warning) is a single grep-able/alertable signal instead of only per-file noise
- [ ] rename `TestScan_NoRecordFileFlaggedWithModTime` to `TestScan_NoRecordFileFlaggedWithCTime`; remove the `os.Chtimes` backdating (lines ~143-147 — `ctime` cannot be set backward by any process); replace the exact-value assertion with `time.Since(got.CTime) < 5*time.Second`
- [ ] audit remaining `scan_test.go` tests referencing `ModTime` (`candidatePaths` helper and others) and update field references to `CTime`
- [ ] run tests — must pass before task 8

### Task 8: `FilterMinAge` uses `ctime` semantics

**Files:**
- Modify: `server/internal/orphans/filter.go`
- Modify: `server/internal/orphans/filter_test.go`

- [ ] update `FilterMinAge` body: `now.Sub(c.CTime) >= minAge` (was `c.ModTime`)
- [ ] rewrite the doc comment to explain the guard now keys off `ctime` specifically because `mtime`/`atime` are client-controlled since `filestore.MoveFile` started calling `Chtimes` (Task 4) — reference `docs/adr/0006-ctime-based-orphan-age-guard.md`
- [ ] mechanically update all `Candidate{Path: ..., ModTime: ...}` literals in `filter_test.go` to `CTime: ...` (purely synthetic values, no real files involved, so no other change needed)
- [ ] add new test case demonstrating the fix's purpose: a candidate with `CTime` set to "just now" (fresh write) but conceptually representing a file whose `mtime` would be years old under the old scheme — assert it is correctly *dropped* by `FilterMinAge` (too young by ctime); pair this with Task 6's "Chtimes doesn't move ctime backward" test in the doc comment — together the two tests are the plan's proof that the guard survives client-controlled `mtime`, not just an assertion in the ADR
- [ ] run full `orphans` package test suite — must pass before task 9

### Task 9: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented: real `creation_date` reflected as on-disk `mtime`/`atime` for newly completed uploads (both normal completion and crash-recovery paths); orphan-scan race guard uses `ctime`, unaffected by the new `Chtimes` calls
- [ ] verify edge cases: empty `creation_date`, unparseable `creation_date`, `Chtimes` permission error (best-effort, non-fatal), pre-existing `CompletionIntent` records without `CreationDate` (upgrade-in-place)
- [ ] run full test suite: `cd server && go test ./...`
- [ ] run `go vet ./...` and existing lint command if configured (check `server/Makefile` or CI config for the exact invocation)
- [ ] confirm no e2e test regressions: `cd server && go test ./e2e/...` (if run separately from the main suite)
- [ ] verify test coverage for all new/modified functions listed in Technical Details

### Task 10: Mutation testing pass and fix survivors

**Files:**
- Modify: whichever test files gremlins flags (likely `mover_test.go`, `filter_test.go`, `scan_test.go`, `ctime_test.go`, `completion_test.go`, `recovery_test.go`)

Per `docs/adr/0002-go-gremlins-mutation-testing.md`'s established convention: passing unit tests don't prove strong assertions, only that some path was exercised. Every new/changed function from Tasks 1-8 needs its test suite verified against mutation, not just coverage.

- [ ] run `make mutation-test` (or the equivalent `gremlins` invocation per the `server/Makefile` target) scoped to `internal/filestore`, `internal/store`, `internal/orphans` — the three packages this plan touches
- [ ] review the report for surviving mutants specifically in the new/changed code from this plan: `parseCreationDate`, `isSaneCreationDate`, the `Chtimes` branch in `MoveFile`, `PlanDestResult.DateUsed` plumbing, `CompletionIntent.CreationDate` round-trip, both `ctime()` platform functions, `Candidate.CTime` population in `Scan`, and `FilterMinAge`'s `CTime`-based comparison
- [ ] for each surviving mutant, strengthen the corresponding test's assertions (per ADR 0002: fix by strengthening tests, not by loosening gremlins' config or ignoring survivors) — pay particular attention to the boundary conditions this plan introduces: the `isSaneCreationDate` min/max edges, the `FilterMinAge` `>=` boundary re-verified against `CTime` instead of `ModTime`, and the `Chtimes` error-vs-success branch
- [ ] re-run `make mutation-test` after fixes — must show no new survivors in the touched packages before task 11
- [ ] run full test suite again: `cd server && go test ./...` — must still pass after test strengthening
- [ ] run tests — must pass before task 11

### Task 11: Update documentation

- [ ] confirm `docs/adr/0006-ctime-based-orphan-age-guard.md` accurately reflects the shipped implementation (field names, file names) — amend if anything drifted during implementation
- [ ] update `CONTEXT.md` only if implementation surfaced a new domain term worth capturing (none anticipated — this is a mechanism change, not a new domain concept per the domain-modeling session)
- [ ] update package doc comments in `orphans/scan.go` (currently states the package is "pure... no I/O" for the guard policy — confirm this framing still holds, or adjust if `ctime` platform files change that characterization)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Explicitly out of scope for this plan:**
- Backfilling `mtime`/`atime` for files already present in `organized/`
  before this feature ships. Those files keep whatever timestamp they
  currently have; a backfill would need its own scoping (risk of a bulk
  metadata-writing pass across potentially large `organized/` trees,
  interaction with any concurrent orphan scan, and confirming `CreationDate`
  is still available in the DB for every affected `complete`-status record).

**Manual verification** (if applicable):
- After deploying, spot-check a freshly completed upload on the real
  server: confirm `stat <file>` shows `mtime`/`atime` matching the
  client's `creation_date` and `ctime` close to the actual completion
  time.
- Confirm the orphan-scan goroutine (`runGCOrphans` in `server/main.go`)
  still runs cleanly against real data post-deploy — no unexpected spike
  in candidates flagged (which would indicate the `ctime` extraction is
  misbehaving on the production OS/filesystem).
