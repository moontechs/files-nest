# gc-orphans: background orphaned-file cleanup

## Overview

`HandleDeleteUpload` (`server/internal/api/handlers.go:818-889`) deletes a
completed upload's on-disk file before marking its record `deleted` in
BadgerDB; a failed `os.Remove` is only logged, never retried. Combined with
the fact that nothing today scans `organized/` against BadgerDB records,
files with no live (`complete`-status) record can accumulate on disk
silently — see `CONTEXT.md` terms "Orphan file", "Organized path" and
`docs/adr/0005-gc-orphans-readonly-badger.md` for the full rationale.

This plan adds an orphan-cleanup cycle that runs as a **background goroutine
inside the server process** (`server/main.go`), alongside the existing
`runBadgerGC` goroutine. It reuses the server's already-open `*store.Store`
handle — no second Badger handle is ever opened, so there is nothing to
reason about with respect to Badger's directory lock.

An earlier draft of this plan proposed a standalone `cmd/gc-orphans` CLI
opening BadgerDB read-only (`store.OpenReadOnly`, `WithReadOnly(true)`) so it
could run against a live server without downtime. That premise turned out to
be false: Badger's directory lock is a `flock`, exclusive (`LOCK_EX`) for the
server's read-write open and shared (`LOCK_SH`) for a read-only open — and an
exclusive lock held by one process blocks *any* other process's lock
request, shared or exclusive, non-blocking (`flock(LOCK_NB)` fails
immediately). Confirmed empirically: opening a second Badger handle
read-only against a directory already held read-write by another process
fails immediately with `"Cannot acquire directory lock ... Another process
is using this Badger database."` See `docs/adr/0005-gc-orphans-readonly-badger.md`
(rewritten) for the corrected rationale and the in-process design chosen
instead.

## Context (from discovery)

- `server/main.go:165-189` — `runBadgerGC(ctx, db)` is the existing
  background-maintenance-goroutine pattern this plan follows: a
  `time.NewTicker`, `select` on `ctx.Done()`/`ticker.C`, started via `go
  runBadgerGC(ctx, dbStore.DB())` right after the store opens, cancelled via
  the same `context.WithCancel` used elsewhere in `run()`.
- `server/internal/api/recovery.go:76` (`Recoverer.Recover`) — runs
  synchronously in `run()` at `server/main.go:86-88`, *after* `runBadgerGC`
  starts but *before* the HTTP server starts serving traffic. It reconciles
  files already moved into `organized/` whose DB record hasn't yet
  committed as `complete` (crash recovery). `runGCOrphans`'s immediate
  first cycle must run after `Recover()` returns, not alongside
  `runBadgerGC` — see Solution Overview's "Startup ordering" note for why
  (a crash followed by a long-enough outage can otherwise make
  `runGCOrphans` delete a file `Recover()` was about to reconcile, since
  the 3-hour age guard doesn't distinguish that case from a genuine
  orphan).
- `server/main.go:52-58` — `MAX_CONCURRENT_UPLOADS` env var parsing is the
  existing fallback-with-warning pattern this plan follows for
  `GC_ORPHANS_INTERVAL`: invalid/unset/non-positive falls back to a default,
  with a logged `WARNING`.
- `server/main.go:23-26` — existing `const` block already holds policy
  constants for background goroutines (e.g. `valueLogGCDiscardRatio`); the
  new `orphanMinCandidateAge` constant belongs there, not inside the
  `orphans` package — `orphans` stays a pure, fully-parameterized library
  with no baked-in policy.
- `server/internal/store/uploads.go:631` — `Store.ListByStatus(status)`
  already exists and iterates the status index via `s.db.View`. Reused
  as-is; no read-only store variant is needed since there's only ever one
  `*store.Store` handle now.
- `server/main.go:44,62` — `STORAGE_PATH` env var (default `./data`), DB at
  `STORAGE_PATH/db`. `organized/` lives as a sibling (`filestore.Mover`,
  `server/internal/filestore/mover.go`).
- **`Upload.OrganizedPath` is stored relative to `STORAGE_PATH`, already
  including the `organized/` prefix** — confirmed via
  `server/internal/api/handlers.go:804`
  (`h.finalizeCompletion(w, r, upload, plan.Rel)`) and
  `server/internal/filestore/mover.go:97`
  (`rel := filepath.Join("organized", year, month, day, filename)`). It is
  **not** relative to the `organized/` directory itself. `Scan` must build
  its known-path set as `filepath.Join(storagePath, rec.OrganizedPath)` —
  joining `storagePath`, not `organizedRoot` — to land on the same absolute
  form `filepath.WalkDir` produces when walking `organizedRoot =
  filepath.Join(storagePath, "organized")`. (Doubling the `organized/`
  segment by joining `organizedRoot` + `rec.OrganizedPath` instead was
  caught during review of the earlier draft — an easy bug to reintroduce,
  documented here so it isn't.)
- Test convention across `internal/store`, `internal/filestore`: plain
  `testing` package, table-driven tests, `t.TempDir()` for on-disk fixtures
  (e.g. `server/internal/store/store_test.go:12`). No testify, no TDD
  requirement elsewhere in the repo.
- Logging convention: plain `log.Printf`/`log.Println` (stdlib `log`)
  everywhere in `server/` production code — no `slog`, no JSON logging.
  Matched here; not introducing a new logging style for this feature.
  (A separate, later, whole-server swap to `github.com/go-pkgz/lgr` was
  discussed and deliberately deferred — out of scope for this plan.)
- `server/README.md:119-135` — env vars are documented as a table row plus a
  dedicated subsection (see `MAX_CONCURRENT_UPLOADS`'s "Concurrency Limit"
  section). `GC_ORPHANS_INTERVAL` follows the same shape.

## Development Approach

- **Testing approach**: Regular (code first, tests same task) — matches
  existing repo convention.
- Complete each task fully, with passing tests, before starting the next.
- Every task that adds/changes behavior includes tests as separate checklist
  items (success + error paths).
- Update this plan's checkboxes immediately as work completes; add ➕ for
  newly discovered tasks, ⚠️ for blockers.

## Solution Overview

- `server/internal/orphans` (new package): pure scan/filter/apply logic,
  unit-testable without the server process.
  - `Candidate{Path string, ModTime time.Time}` — `ModTime` is populated
    only for files actually flagged as candidates (from `d.Info()` in the
    `WalkDir` callback), not for every walked file, so matched files cost no
    extra stat.
  - `Result{Candidates []Candidate, Removed []Candidate, Errors []error,
    KnownComplete int}` — `KnownComplete` is the count of `complete`-status
    records `Scan` built its known-path set from, exposed so `runGCOrphans`
    can size the circuit breaker below without a second DB call.
  - `Scan(db *store.Store, storagePath string) (Result, error)`: builds the
    known-path set from `db.ListByStatus(store.StatusComplete)` (see Context
    note on `OrganizedPath`), walks `filepath.Join(storagePath,
    "organized")` via `filepath.WalkDir`. Inside the callback, the known-set
    membership check happens **first** — `d.Info()` is only called for a
    file that's *not* in the known set, i.e. only for files already being
    flagged as candidates. This ordering is what makes the "no extra stat
    for matched files" property true; it's not incidental, so keep the
    known-set check ahead of the `d.Info()` call when implementing. A
    `d.Info()` failure on a to-be-flagged file is collected into
    `Result.Errors` and that file is excluded as a candidate (its age can't
    be determined, so it can't safely pass `FilterMinAge` below). A fatal
    error (the organized root itself unreadable) is returned directly.
  - `FilterMinAge(candidates []Candidate, minAge time.Duration, now
    time.Time) []Candidate`: pure function, no I/O. Drops any candidate
    younger than `minAge`; keeps a candidate exactly `minAge` old
    (`now.Sub(c.ModTime) >= minAge`). `now` is captured once by the caller
    (`runGCOrphans`, via `time.Now()`) *after* `Scan` returns — not
    per-candidate — so on a large `organized/` tree, a candidate whose
    `ModTime` was read early in a slow scan is compared against a slightly
    later `now` than a candidate read near the end. This makes the filter
    marginally more conservative (slightly more likely to keep a borderline
    candidate one more cycle), never less safe, and needs no correction —
    documented here so it's a known property, not a surprise found in
    review. Exists as a race guard: an in-flight
    upload's `moveCompletedFile` can write a file to `organized/` moments
    before its DB record commits as `complete`, which `Scan` would otherwise
    flag as orphaned.
  - `Apply(candidates []Candidate) Result`: `os.Remove`s each candidate,
    collecting per-file errors the same best-effort way, appending
    successes to `Result.Removed`.
- `server/main.go`: new `runGCOrphans(ctx context.Context, db *store.Store,
  storagePath string, interval time.Duration, minAge time.Duration)`
  goroutine. Shape mirrors `runBadgerGC` (ticker + `select` on
  `ctx.Done()`/`ticker.C`) with two differences: it runs one cycle
  immediately before entering the ticker wait, so pre-existing orphans
  aren't left for up to a full interval after a restart; and it doesn't
  start alongside `runBadgerGC`.
  - **Startup ordering**: `runGCOrphans` is started *after*
    `recoverer.Recover()` returns (`server/main.go:86-88`), not alongside
    `runBadgerGC` (`server/main.go:75`). `Recover()` reconciles files
    already moved into `organized/` whose DB record hasn't yet committed as
    `complete` (crash recovery, `server/internal/api/recovery.go:76`). If a
    crash happened and the server was down for longer than the 3-hour age
    guard, such a file's `ModTime` is already old enough to pass
    `FilterMinAge` — so if the orphan scan's immediate first cycle ran
    concurrently with (or before) `Recover()`, it could delete the exact
    file recovery was about to reconcile. Running `runGCOrphans` strictly
    after `Recover()` completes closes this race entirely, since by then
    every recoverable file already has its `complete` record committed and
    is in `Scan`'s known-path set.
  - **Circuit breaker**: before calling `orphans.Apply`, if
    `len(candidates) > max(50, result.KnownComplete/5)` (more than 20% of
    the known-complete set, with a floor of 50 so small deployments don't
    trip on a handful of files), skip `Apply` for this cycle, log a single
    loud `ERROR`-style line with the candidate count and known-complete
    count, and return — don't delete anything. This caps the damage a
    future regression in `Scan`'s known-path matching (the plan's Context
    section already documents one such bug being caught in review) can do
    on its first bad cycle, given there's no dry-run step to catch it
    first. The next cycle tries again from scratch.
  - `GC_ORPHANS_INTERVAL` env var (default `48h`), parsed with
    `time.ParseDuration`, same fallback-with-warning shape as
    `MAX_CONCURRENT_UPLOADS`.
  - `orphanMinCandidateAge = 3 * time.Hour` — a `const` in `main.go`
    alongside `valueLogGCDiscardRatio`, passed into `runGCOrphans` as a
    parameter. Policy lives in `main.go`; `orphans` stays parameter-driven.
  - No dedicated unit test for `runGCOrphans` itself — matches the existing
    precedent that `runBadgerGC` has zero unit tests (no `main_test.go`
    exists); correctness rests on the already-tested `Scan`/`FilterMinAge`/
    `Apply` functions underneath. Unlike `runBadgerGC` (whose underlying
    `RunValueLogGC` is idempotent and non-destructive to user data),
    `runGCOrphans` deletes user files with no undo — this precedent match
    is about code shape, not risk parity; the startup-ordering fix and
    circuit breaker above are what actually manage that higher risk, not
    the testing approach.

## Technical Details

**`Candidate`** (`server/internal/orphans/scan.go`):
```go
type Candidate struct {
    Path    string    // absolute path under organized/
    ModTime time.Time
}
```

**`Result`**:
```go
type Result struct {
    Candidates    []Candidate
    Removed       []Candidate
    Errors        []error
    KnownComplete int // count of complete-status records the known-path set was built from
}
```

**Cycle flow** (`runGCOrphans`, one iteration):
```go
result, err := orphans.Scan(db, storagePath)
if err != nil {
    log.Printf("gc-orphans: scan failed: %v", err)
    return // this cycle only; the ticker will try again next interval
}
candidates := orphans.FilterMinAge(result.Candidates, minAge, time.Now())

if breaker := max(50, result.KnownComplete/5); len(candidates) > breaker {
    log.Printf("gc-orphans: ERROR: %d candidates exceeds circuit breaker "+
        "(%d, known-complete=%d) — skipping delete this cycle",
        len(candidates), breaker, result.KnownComplete)
    return
}

applied := orphans.Apply(candidates)
for _, c := range applied.Removed {
    log.Printf("gc-orphans: removed orphan %s", c.Path)
}
for _, e := range append(result.Errors, applied.Errors...) {
    log.Printf("gc-orphans: error: %v", e)
}
```

**Scope guards**: `Scan` only walks the given `organizedRoot`
(`STORAGE_PATH/organized`) — `incoming/` is a different root entirely and is
never passed in. No directory removal after file deletion (empty dirs are
left as-is).

**Fully autonomous deletion**: candidates surviving the age filter are
deleted every cycle with no dry-run flag and no manual-approval step. This
is a deliberate choice made with the risk understood. Two guards manage
that risk: the 3-hour age floor (protects against a race with an in-flight
upload completion) and the circuit breaker above (protects against a future
regression in `Scan`'s matching logic causing mass deletion on its first
bad cycle, since there's no dry-run step left to catch it beforehand).

## What Goes Where

- **Implementation Steps**: all code, tests, and doc updates below.
- **Post-Completion**: manual verification against a real `STORAGE_PATH`
  tree, and updating any ops runbook that references the old CLI framing.

## Implementation Steps

### Task 1: `orphans.Scan` — build known-path set and diff against disk

**Files:**
- Create: `server/internal/orphans/scan.go`
- Create: `server/internal/orphans/scan_test.go`

- [x] define `Candidate` (with `Path`, `ModTime`) and `Result` types in `server/internal/orphans/scan.go`
- [x] implement `Scan(db *store.Store, storagePath string) (Result, error)`: call `db.ListByStatus(store.StatusComplete)`, set `Result.KnownComplete` to the count of records returned, build `map[string]struct{}` keyed by `filepath.Join(storagePath, rec.OrganizedPath)` (NOT `filepath.Join(organizedRoot, rec.OrganizedPath)` — see Context note); inside the `filepath.WalkDir` callback over `filepath.Join(storagePath, "organized")`, check known-set membership **first**, and only call `d.Info()` (to populate `ModTime`) for files not in the set — this ordering is what keeps matched files free of extra stat cost, so don't call `d.Info()` unconditionally
- [x] per-file/per-subdirectory walk errors (including `d.Info()` failures while building a candidate's `ModTime`) append to `Result.Errors` and scanning continues (best-effort), excluding that file as a candidate since its age can't be determined; an error opening the organized root itself returns as the function's error return (fatal)
- [x] write test: file referenced by a `complete` record is NOT flagged
- [x] write test: file referenced only by a `deleted`-status record's stale `OrganizedPath` IS flagged
- [x] write test: file with no matching record at all IS flagged, with `ModTime` matching the file's actual mtime
- [x] write test: unreadable subdirectory produces an entry in `Result.Errors`, scan continues over sibling files
- [x] write test: nonexistent organized root returns a fatal error
- [x] write test: known-path matching correctly joins `storagePath` + `OrganizedPath` (regression test pinning the path-form bug caught in review — a record's `OrganizedPath` of `organized/2026/08/20/x.jpg` under `storagePath` must match the file found by walking `storagePath/organized`, not `storagePath/organized/organized/...`)
- [x] run tests - must pass before task 2

### Task 2: `orphans.FilterMinAge` — race guard against in-flight uploads

**Files:**
- Create: `server/internal/orphans/filter.go`
- Create: `server/internal/orphans/filter_test.go`

- [x] implement `FilterMinAge(candidates []Candidate, minAge time.Duration, now time.Time) []Candidate` in `server/internal/orphans/filter.go`: pure function, no I/O — keeps a candidate when `now.Sub(c.ModTime) >= minAge`
- [x] write test: candidate older than `minAge` is kept
- [x] write test: candidate younger than `minAge` is dropped
- [x] write test: candidate exactly `minAge` old is kept (boundary case)
- [x] write test: empty input returns empty output
- [x] run tests - must pass before task 3

### Task 3: `orphans.Apply` — delete candidates

**Files:**
- Create: `server/internal/orphans/apply.go`
- Create: `server/internal/orphans/apply_test.go`

- [x] implement `Apply(candidates []Candidate) Result` in `server/internal/orphans/apply.go`: `os.Remove`s each candidate path, appends to `Result.Removed` on success, appends to `Result.Errors` on failure, continues regardless (best-effort)
- [x] write test: all candidates removable → all appear in `Result.Removed`, `Result.Errors` empty
- [x] write test: one candidate's file already gone / unremovable → its error lands in `Result.Errors`, remaining candidates still removed
- [x] run tests - must pass before task 4

### Task 4: `runGCOrphans` goroutine wired into `main.go`

**Files:**
- Modify: `server/main.go`

- [x] add `orphanMinCandidateAge = 3 * time.Hour` to the existing `const` block (`server/main.go:23-26`), alongside `valueLogGCDiscardRatio`
- [x] parse `GC_ORPHANS_INTERVAL` env var via `time.ParseDuration`, default `"48h"`; on parse error or non-positive duration, log a `WARNING` and fall back to the default — mirrors the `MAX_CONCURRENT_UPLOADS` block (`server/main.go:52-58`)
- [x] implement `runGCOrphans(ctx context.Context, db *store.Store, storagePath string, interval, minAge time.Duration)`: runs one `orphans.Scan` → `orphans.FilterMinAge` cycle immediately, applies the circuit breaker (skip `Apply` and log loudly if `len(candidates) > max(50, result.KnownComplete/5)`), otherwise `orphans.Apply`s and logs each candidate found/removed/errored via `log.Printf("gc-orphans: ...")`, then loops on `select { case <-ctx.Done(): return; case <-ticker.C: <same cycle> }` — mirrors `runBadgerGC`'s shape (`server/main.go:165-189`) with the added immediate-first-run and breaker
- [x] start it via `go runGCOrphans(ctx, dbStore, storagePath, gcOrphansInterval, orphanMinCandidateAge)` **after** `recoverer.Recover()` returns (`server/main.go:86-88`) — NOT alongside `go runBadgerGC(ctx, dbStore.DB())` at line 75 — so the immediate first cycle can never race a file `Recover()` hasn't reconciled yet; reuse the same `ctx`/`cancel` already in `run()`
- [x] no dedicated unit test for `runGCOrphans` itself (matches `runBadgerGC`'s zero-test precedent in code shape — see Solution Overview's note on why that precedent covers shape, not risk parity); correctness is covered by Tasks 1-3's tests plus the startup-ordering and circuit-breaker guards above
- [x] run `cd server && go build ./...` and `cd server && go test ./...` — must pass before task 5

### Task 5: Verify acceptance criteria

- [ ] verify orphan detection covers both drift directions (no record at all; `deleted`-status record with surviving file) per Overview
- [ ] verify `incoming/` is never scanned (no reference to it anywhere in `server/internal/orphans`)
- [ ] verify empty directories are left behind after cleanup (no directory-removal code present)
- [ ] verify `runGCOrphans` is started after `recoverer.Recover()` returns, not alongside `runBadgerGC` (read `server/main.go`, confirm call ordering matches Task 4)
- [ ] verify the circuit breaker: with a small `t.TempDir()`-based `KnownComplete` count, confirm `len(candidates) > max(50, KnownComplete/5)` causes `Apply` to be skipped and a log line to be emitted instead of deletion (covered by a `FilterMinAge`/breaker-logic unit test, or noted here if the breaker's arithmetic is simple enough to verify by inspection)
- [ ] verify the server starts, runs one orphan-scan cycle immediately after recovery completes (not concurrently with it), logs any orphans found/removed in that cycle, and subsequent cycles fire on `GC_ORPHANS_INTERVAL` — manual check in Post-Completion (needs a running server + a real orphan file)
- [ ] run full test suite: `cd server && go test ./...`
- [ ] run e2e suite if present and fast enough: `cd server && go test ./e2e/...` (or project's documented e2e command)
- [ ] run `go test -cover ./internal/orphans/...` and confirm no exported function lacks a test exercising it (inspect by name, not by a numeric coverage threshold)

### Task 6: Update documentation

- [ ] add `GC_ORPHANS_INTERVAL` to the env var table in `server/README.md:119-123`, and a short subsection (matching the shape of "Concurrency Limit") explaining the immediate-on-startup + interval cycle, the 3-hour age guard, and that cleanup is automatic (no manual trigger)
- [ ] update `CLAUDE.md` if the new `internal/orphans` package or `main.go` goroutine changes any documented architecture overview
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*Items requiring manual intervention or external systems*

**Manual verification**:
- Start the server against a real `STORAGE_PATH` tree with an orphan file
  manually placed in `organized/` (mtime older than 3 hours, e.g. via `touch
  -t`), confirm the immediate on-startup cycle logs and removes it.
- Confirm a file placed in `organized/` with a recent mtime (younger than 3
  hours) survives a cycle (age guard working).
- Confirm `GC_ORPHANS_INTERVAL` overrides the default when set, and that an
  invalid value logs a `WARNING` and falls back to `48h`.

**External system updates**:
- Update any ops runbook or deployment doc that referenced the earlier
  `cmd/gc-orphans` CLI framing (cron/docker invocation, `--apply --json`
  flags) — none of that exists anymore; cleanup is now automatic and
  internal to the server process.
