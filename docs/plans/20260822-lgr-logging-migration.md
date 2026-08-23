# Migrate server/ logging to go-pkgz/lgr

## Overview

Replace the server's plain stdlib `log` usage with `github.com/go-pkgz/lgr`,
adding runtime-configurable log levels (`LOG_LEVEL=info|debug|trace`) and a
consistent severity policy: INFO for lifecycle events and (once explicitly
marked) warnings/errors, DEBUG for happy-path/per-request noise, WARN/ERROR
for genuine problems.

The mechanism is `lgr.SetupStdLogger(opts...)`, which redirects the
*existing* stdlib `log.Printf`/`log.Println` calls through `lgr`'s
formatting and level filtering. No call site changes its function
signature; no logger instance is threaded through structs/handlers. This
keeps the diff to: one setup call in `main.go`, and per-message text edits
(adding/moving a level-prefix word) across the existing call sites.

See `docs/adr/0007-lgr-via-stdlib-redirect.md` for the full rationale,
including why `SetupStdLogger` was chosen over threading an explicit
`lgr.Logger`, and why JSON output is deliberately out of scope.

## Context (from discovery)

- Server uses stdlib `log` exclusively today, no wrapper/interface, no
  levels, one setup line at `server/main.go:44`
  (`log.SetFlags(log.LstdFlags | log.Lshortfile)`).
- ~83 log call sites total, concentrated in `internal/api/handlers.go` (29)
  and `internal/api/recovery.go` (23), `main.go` (18), with single sites in
  `auth.go`, `limiter.go`, `router.go` (x2), `internal/filestore/mover.go`,
  `internal/uploadbackend/tushandler.go`.
- `lgr`'s actual level-detection logic (`extractLevel`, read from source):
  `strings.HasPrefix(line, lv)` for `lv` in
  `{TRACE,DEBUG,INFO,WARN,ERROR,PANIC,FATAL}` (or bracketed `"["+lv+"]"`),
  checked only at the very start of the message, case-sensitive, defaulting
  to INFO if nothing matches.
- **Known landmine**: `"WARNING: ..."` matches `HasPrefix(line, "WARN")` —
  the "WARN" is stripped, leaving `"ING: ..."` as the visible message. All
  three existing `"WARNING: ..."` messages in `main.go` must become
  `"WARN ..."` (no `ING:`).
- **Known landmine**: level words embedded *after* a subsystem prefix
  (`"gc-orphans: ERROR: ..."`, `"gc-orphans: scan failed: ..."`) are never
  recognized by `HasPrefix`-at-start matching — the level word must move to
  the very front of the message, subsystem tag after it.
- **Stream reassignment (deliberate, not incidental)**: stdlib `log`
  defaults to `os.Stderr` — every one of the ~83 current call sites writes
  to stderr today. `lgr.SetupStdLogger`'s output path writes INFO/DEBUG/WARN
  lines to stdout only, adding stderr as a *second* destination solely for
  ERROR/FATAL/PANIC. So this migration moves the bulk of log output from
  stderr-only to stdout-only. This is accepted as fine here because
  `docker compose logs` (the only log consumer found during discovery, see
  `server/README.md`'s `make e2e-logs` target) merges both streams by
  default — but it is a real behavior change, not something already true
  before this migration (ADR-0007 should be read with this in mind: "logs go
  to stdout" becomes true *because of* this change, not before it).
- **Caller-location info retained**: the line being replaced
  (`log.SetFlags(log.LstdFlags | log.Lshortfile)`) includes `Lshortfile`,
  so every current log line carries `file.go:123`. `logOptionsFromEnv`'s
  base options include `lgr.CallerFile` (in addition to `lgr.Msec`) so this
  is preserved rather than silently dropped — see Technical Details.
- No test in the codebase currently couples to app log output format (one
  test builds an `slog.Logger` to capture a third-party `tusd` library's own
  logs — unrelated).
- Relevant `lgr` identifiers confirmed from docs/source: `lgr.New(opts...)`,
  `lgr.SetupStdLogger(opts...)`, options `lgr.Debug`, `lgr.Trace`,
  `lgr.Msec`, `lgr.CallerFile`/`CallerFunc`/`CallerPkg`, `lgr.LevelBraces`,
  `lgr.Out`/`lgr.Err`, `lgr.SlogHandler` (JSON path, unused here).

## Development Approach

- **testing approach**: Regular (code first, then tests) — the migration is
  almost entirely mechanical message-text edits; the one piece of genuinely
  new logic (LOG_LEVEL parsing/fallback) gets its own small test.
- complete each task fully before moving to the next.
- **CRITICAL: every task that introduces new logic MUST include tests for
  it.** Tasks that are pure message-text edits (no branching logic) don't
  need new tests beyond compiling and existing tests passing — there is no
  new behavior to assert on, only text content, and the project has no
  existing tests coupled to log text (confirmed above) so none break.
- **CRITICAL: all tests must pass before starting next task** — no
  exceptions.
- run `make test` after each task; run `make lint` before considering the
  whole migration done (zero-tolerance per `server/CLAUDE.md`).

## Testing Strategy

- **unit tests**: one new table-driven test for the `LOG_LEVEL` parsing/
  fallback function introduced in `main.go` (covers `""`, `"info"`,
  `"debug"`, `"trace"`, and an invalid value falling back to `info` with a
  warning).
- No e2e test changes needed — `server/e2e/` tests exercise HTTP behavior,
  not log output; this migration doesn't change response behavior.
- No mocks/log-capture tests needed elsewhere — nothing in the existing
  suite asserts on the app's own log text.

## Solution Overview

1. Add the `github.com/go-pkgz/lgr` dependency.
2. In `main.go`, extract `LOG_LEVEL` env parsing into a small function
   returning `[]lgr.Option`, call `lgr.SetupStdLogger(opts...)` in place of
   the current `log.SetFlags(...)` line.
3. Fix the three `"WARNING: ..."` messages in `main.go` to `"WARN ..."`.
4. Re-point the `gc-orphans` messages so the level word leads.
5. Add `"ERROR "` prefixes to every genuine failure-path log across
   `main.go`, `internal/api/*.go`, `internal/filestore/mover.go`,
   `internal/uploadbackend/tushandler.go` — leaving lifecycle/status
   narration lines unmarked (INFO).
6. Give the HTTP access log in `router.go` a runtime 3-way level split by
   status code, and add `"ERROR "` to the panic-recovery log.
7. Verify with `make test` and `make lint`.

## Technical Details

### `main.go` — level setup

Replace:
```go
log.SetFlags(log.LstdFlags | log.Lshortfile)
```
with:
```go
lgr.SetupStdLogger(logOptionsFromEnv(getEnv("LOG_LEVEL", "info"))...)
```
where `logOptionsFromEnv` is a new small function:
```go
// logOptionsFromEnv maps LOG_LEVEL ("info", "debug", "trace") to lgr
// options. An unrecognized value falls back to "info" with a warning
// logged via the base (pre-level) options, since the level option itself
// isn't known yet at that point.
func logOptionsFromEnv(level string) []lgr.Option {
	opts := []lgr.Option{lgr.Msec, lgr.CallerFile}

	switch level {
	case "", "info":
	case "debug":
		opts = append(opts, lgr.Debug)
	case "trace":
		opts = append(opts, lgr.Trace)
	default:
		log.Printf("WARN invalid LOG_LEVEL=%q, falling back to info", level)
	}

	return opts
}
```
Note: the `log.Printf` inside the `default` case goes through the
*stdlib* `log` package, not `lgr` — Go evaluates `logOptionsFromEnv(...)`
(and any `log.Printf` inside it) fully before `lgr.SetupStdLogger(...)` is
called with its result, so this warning is always emitted via plain stdlib
formatting, deterministically. This is fine: it's a single startup-time
warning either way.

### Level-prefix edits — full enumeration

**`server/main.go`**
| Line | Current | Change |
|---|---|---|
| 38 | `"fatal error: %v"` | → `"ERROR fatal error: %v"` — **not** `"FATAL "`: `lgr`'s `Logger.logf()` treats an extracted `FATAL` level as a command, not just a label — it unconditionally calls `l.fatal()`, whose default implementation is `os.Exit(1)`, synchronously from inside the `log.Printf` call. Using `"FATAL "` here would make the explicit `os.Exit(1)` on the next line dead code and conflate logging with process-exit control flow. `"ERROR "` keeps the exit mechanism explicit and in application code, where it's visible and testable. |
| 57-58 | `"WARNING: invalid MAX_CONCURRENT_UPLOADS=..."` | → `"WARN invalid MAX_CONCURRENT_UPLOADS=..."` |
| 69-70 | `"WARNING: invalid GC_ORPHANS_INTERVAL=..."` | → `"WARN invalid GC_ORPHANS_INTERVAL=..."` |
| 76 | `"opening store at %s"` | unmarked (INFO) — no change |
| 103 | `"startup recovery completed with errors: %v"` | → `"ERROR startup recovery completed with errors: %v"` |
| 123-124 | `"WARNING: BACKUP_USER/BACKUP_PASS not set..."` | → `"WARN BACKUP_USER/BACKUP_PASS not set..."` |
| 158 | `"received %v, shutting down"` | unmarked (INFO) — no change |
| 166 | `"server shutdown error: %v"` | → `"ERROR server shutdown error: %v"` |
| 170 | `"server listening on :%s"` | unmarked (INFO) — no change |
| 177 | `"server stopped"` | unmarked (INFO) — no change |
| 202 | `"badger value log GC error: %v"` | → `"ERROR badger value log GC error: %v"` |
| 249 | `"gc-orphans: scan failed: %v"` | → `"ERROR gc-orphans: scan failed: %v"` |
| 260-262 | `"gc-orphans: ERROR: %d candidates exceeds circuit breaker (%d, known-complete=%d) — skipping delete this cycle"` | → `"WARN gc-orphans: %d candidates exceeds circuit breaker (%d, known-complete=%d) — skipping delete this cycle"` (drop redundant embedded `ERROR:`; WARN not ERROR — a circuit-breaker skip is a caution, not a failure) |
| 268 | `"gc-orphans: removed orphan %s"` | unmarked (INFO) — stays, audit trail for a destructive action |
| 271 | `"gc-orphans: error: %v"` (in `applied.Removed`-adjacent errors loop) | → `"ERROR gc-orphans: error: %v"` |
| 274 | `"gc-orphans: %d scan errors this cycle (see above)"` | → `"ERROR gc-orphans: %d scan errors this cycle (see above)"` |
| 277 | `"gc-orphans: error: %v"` (applied.Errors loop) | → `"ERROR gc-orphans: error: %v"` |

**`server/internal/api/recovery.go`** — add `"ERROR "` prefix to:
- line 65 `"recovery: backend lost recovery error: %v"`
- line 106 `"recovery: failed to recover intent %s: %v"`
- line 139 `"recovery: intent %s: failed to remove source file after move: %v"`
- line 162 `"recovery: intent %s: neither src (%s) nor dst (%s) exists for
  upload record %s (backend %s); keeping intent for manual repair"` — this
  is the `default:` branch's **data-loss** path (neither file exists, kept
  for manual operator repair), not the "moving file" branch (that's line
  151, which stays unmarked/INFO)
- line 177 `"recovery: intent %s: UpdateComplete failed: %v"`
- line 186 `"recovery: intent %s: failed to delete completion intent: %v"`
- line 209 `"recovery: intent %s: failed to delete stale intent: %v"`
- line 227 `"recovery: intent %s: failed to clean up tusd backend: %v"`
- line 293 `"recovery: failed to set backend_lost for %s: %v"`
- line 300 `"recovery: backend check failed for %s: %v"`

Leave unmarked (INFO, lifecycle/status narration, not failures):
- line 54 `"recovery: starting startup recovery..."`
- line 68 `"recovery: startup recovery complete"`
- line 90 `"recovery: no pending completion intents found"`
- line 95 `"recovery: found %d pending completion intents"`
- line 118 `"recovery: processing intent %s (src=%s dst=%s)"`
- line 135 `"recovery: intent %s: both src and dst exist, removing src"`
- line 146 `"recovery: intent %s: file already moved, completing DB record"`
- line 151 `"recovery: intent %s: moving file to organized directory"`
- line 175 `"recovery: intent %s: upload record not found, cleaning up"`
- line 205 `"recovery: intent %s: record already complete, deleting stale intent"`
- line 280 `"recovery: skipping backend check for %s (has pending completion intent)"`
- line 288 `"recovery: backend lost for upload %s (status %s, backend %s)"` —
  judgment call: treat as **WARN** (backend loss is a real anomaly worth
  flagging, distinct from routine status narration; add `"WARN "` prefix)
- line 306 `"recovery: checked %d in-progress uploads, %d backends lost"`

**`server/internal/api/handlers.go`** — add `"ERROR "` prefix to all 29
sites (every one is an existing failure message: `writeJSON encode error`,
`tusd CreateUpload failed`, `PutUploadIfAbsent failed`, `failed to
terminate tusd upload ... after DB error`, `ListByDateRange failed`,
`GetUpload failed` ×4, `GetInfo failed`, `IsComplete failed`, `FilePath
failed`, `failed to terminate tusd upload ... during delete`, `failed to
remove organized file`, `failed to update status to deleted`, `failed to
re-read upload ... before backend_lost`, `failed to set backend_lost`,
`ReRegister failed`, `failed to terminate tusd upload ... after
re-register failure`, `failed to terminate redundant tusd upload`, `failed
to read existing completion intent`, `completion move failed` ×2, `failed
to update status to complete`, `failed to delete completion intent`,
`failed to terminate tusd upload ... after completion`, `GetOffset
failed`, `ForwardPatch failed`). Enumerate exact line numbers at
implementation time via `grep -n 'log\.Printf' internal/api/handlers.go`.

**Single-site files** — add `"ERROR "` prefix:
- `server/internal/api/auth.go:107` `"writeUnauthorized encode error: %v"`
- `server/internal/api/limiter.go:48` `"rejected upload: over concurrency
  limit (cap=%d)"`
- `server/internal/filestore/mover.go:375` `"filestore: chtimes %s failed:
  %v"`
- `server/internal/uploadbackend/tushandler.go:321` `"tusd: failed to
  remove info sidecar %s: %v"`

**`server/internal/api/router.go`**
- line 76-78 (panic recovery): → `"ERROR panic recovered: %v (path=%s
  method=%s)\n%s"`
- line 104 (access log): runtime 3-way split —
  ```go
  level := "DEBUG "
  switch {
  case lrw.statusCode >= 500:
  	level = "ERROR "
  case lrw.statusCode >= 400:
  	level = "WARN "
  }
  log.Printf(level+"%s %s %d %s", strconv.Quote(r.Method), strconv.Quote(r.URL.Path), lrw.statusCode, duration)
  ```

## What Goes Where

- **Implementation Steps**: dependency addition, `main.go` level-setup
  logic + its test, all message-prefix edits, `router.go` access-log
  split, final lint/test verification.
- **Post-Completion**: none — this change is fully self-contained within
  `server/`, no consuming project or deployment config depends on log
  format today.

## Implementation Steps

### Task 1: Add lgr dependency and wire up LOG_LEVEL-driven setup in main.go

**Files:**
- Modify: `server/go.mod`, `server/go.sum`
- Modify: `server/main.go`
- Create: `server/main_internal_test.go` (or add to an existing internal
  test file in package `main` if one exists — check first)

- [x] `cd server && go get github.com/go-pkgz/lgr@latest && go mod tidy`
- [x] add `"github.com/go-pkgz/lgr"` import to `main.go`
- [x] implement `logOptionsFromEnv(level string) []lgr.Option` per Technical
      Details above — the invalid-level warning is always emitted via plain
      stdlib formatting (see Technical Details for why), no ordering
      decision needed at implementation time
- [x] replace `log.SetFlags(log.LstdFlags | log.Lshortfile)` at line 44 with
      `lgr.SetupStdLogger(logOptionsFromEnv(getEnv("LOG_LEVEL", "info"))...)`
- [x] write table-driven test for `logOptionsFromEnv`: cases `""`→base only,
      `"info"`→base only, `"debug"`→base+Debug, `"trace"`→base+Trace,
      `"bogus"`→base only. `lgr.Option` values aren't comparable directly, so
      assert behaviorally: apply the returned options to a fresh `lgr.New(...)`
      and check its resulting level-filtering behavior (e.g. does it emit a
      `"DEBUG "`-prefixed line or suppress it), not by inspecting the option
      slice itself
- [x] write a test specifically for the `"bogus"` case asserting the warning
      is actually logged: redirect `log.SetOutput` to a `bytes.Buffer` for the
      duration of the test (restore via `defer`), call
      `logOptionsFromEnv("bogus")`, assert the buffer contains
      `"invalid LOG_LEVEL"` — this assertion is required, not optional
- [x] run tests — must pass before task 2: `cd server && go test ./... -run TestLogOptionsFromEnv -v`

### Task 2: Fix WARNING landmines and gc-orphans level placement in main.go

**Files:**
- Modify: `server/main.go`

- [ ] change lines 57-58 `"WARNING: invalid MAX_CONCURRENT_UPLOADS=..."` to
      `"WARN invalid MAX_CONCURRENT_UPLOADS=..."`
- [ ] change lines 69-70 `"WARNING: invalid GC_ORPHANS_INTERVAL=..."` to
      `"WARN invalid GC_ORPHANS_INTERVAL=..."`
- [ ] change lines 123-124 `"WARNING: BACKUP_USER/BACKUP_PASS not set..."`
      to `"WARN BACKUP_USER/BACKUP_PASS not set..."`
- [ ] change line 260-262 to lead with `"WARN gc-orphans: ..."` and drop the
      redundant embedded `"ERROR:"` text (see Technical Details table)
- [ ] change line 249 to `"ERROR gc-orphans: scan failed: %v"`
- [ ] change lines 271, 274, 277 to lead with `"ERROR gc-orphans: ..."`
- [ ] change line 103 to `"ERROR startup recovery completed with errors: %v"`
- [ ] change line 166 to `"ERROR server shutdown error: %v"`
- [ ] change line 202 to `"ERROR badger value log GC error: %v"`
- [ ] change line 38 to `"ERROR fatal error: %v"` (not `"FATAL "` — see
      Technical Details for why)
- [ ] no new logic here (pure text edits) — run existing tests to confirm
      nothing depends on this text: `cd server && go test ./... -count=1`
- [ ] run tests — must pass before task 3

### Task 3: Add ERROR prefixes across internal/api (handlers.go, recovery.go, auth.go, limiter.go)

**Files:**
- Modify: `server/internal/api/handlers.go`
- Modify: `server/internal/api/recovery.go`
- Modify: `server/internal/api/auth.go`
- Modify: `server/internal/api/limiter.go`

- [ ] `grep -n 'log\.Printf' server/internal/api/handlers.go` and add
      `"ERROR "` prefix to all 29 messages (all are failure-path per the
      enumeration above)
- [ ] add `"ERROR "` prefix to the 10 failure-path messages in
      `recovery.go` listed in Technical Details; add `"WARN "` to line 288
      (`"backend lost for upload ..."`); leave the other 12 status-narration
      lines in `recovery.go` unmarked
- [ ] add `"ERROR "` prefix to `auth.go:107`
- [ ] add `"ERROR "` prefix to `limiter.go:48`
- [ ] no new branching logic introduced — confirm via full package tests
      that message-text changes don't break anything:
      `cd server && go test ./internal/api/... -count=1 -v`
- [ ] run tests — must pass before task 4

### Task 4: Add ERROR prefixes in filestore/mover.go and uploadbackend/tushandler.go

**Files:**
- Modify: `server/internal/filestore/mover.go`
- Modify: `server/internal/uploadbackend/tushandler.go`

- [ ] add `"ERROR "` prefix to `mover.go:375`
      (`"filestore: chtimes %s failed: %v"`)
- [ ] add `"ERROR "` prefix to `tushandler.go:321`
      (`"tusd: failed to remove info sidecar %s: %v"`)
- [ ] run existing package tests to confirm no coupling to old text:
      `cd server && go test ./internal/filestore/... ./internal/uploadbackend/... -count=1 -v`
- [ ] run tests — must pass before task 5

### Task 5: Router access-log level split and panic-recovery ERROR prefix

**Files:**
- Modify: `server/internal/api/router.go`
- Modify: `server/internal/api/router_internal_test.go` (create if it
  doesn't already exist — check first)

- [ ] add `"ERROR "` prefix to the panic-recovery log at line 76-78
- [ ] implement the 3-way status-code level split at line 104 per Technical
      Details (`lrw.statusCode >= 500` → `"ERROR "`, `>= 400` → `"WARN "`,
      else → `"DEBUG "`)
- [ ] write test(s) covering the level-split branch: a 2xx request logs
      with `DEBUG` prefix, a 404 logs with `WARN` prefix, a 500 logs with
      `ERROR` prefix — capture the logged line (redirect `log.SetOutput` to
      a buffer for the test, restore after) and assert on the prefix.
      **Note:** this technique mutates the global `log` package output for
      the duration of the test, which is unsafe if run under `t.Parallel()`
      — nothing in `internal/api`'s tests currently uses `t.Parallel()`, so
      this is safe today, but do not add `t.Parallel()` to this test or any
      other test in the package without first moving off global-`log`
      mutation
- [ ] write test for the existing success-case coverage if none currently
      exercises `requestLogMiddleware`'s status-code capture path (check
      `router_test.go`/`router_internal_test.go` first — only add what's
      missing)
- [ ] run tests — must pass before task 6: `cd server && go test ./internal/api/... -run TestRequestLogMiddleware -v`

### Task 6: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented: `LOG_LEVEL`
      env var works end-to-end, all landmine messages fixed, all failure
      paths carry `ERROR`, access log carries the 3-way split, no JSON
      wiring present
- [ ] verify edge cases: empty `LOG_LEVEL`, invalid `LOG_LEVEL`, a 3xx
      redirect response in the access log (should be DEBUG, not WARN)
- [ ] run full test suite: `cd server && make test`
- [ ] run lint (zero-tolerance per `server/CLAUDE.md`): `cd server && make lint`
- [ ] spot-check real output: run the server locally with
      `LOG_LEVEL=debug`, hit `/health` and an authenticated route, confirm
      DEBUG-level access-log lines appear and are correctly formatted (no
      `"ING:"` artifacts, no double level-prefixes)

### Task 7: Update documentation and close out the plan

- [ ] update `server/CLAUDE.md` if a new convention is worth capturing
      (e.g. "log level policy: DEBUG for happy-path, WARN/ERROR for
      problems, see ADR-0007") — only if genuinely useful for future
      contributors, not required
- [ ] confirm `docs/adr/0007-lgr-via-stdlib-redirect.md` still accurately
      reflects what was built (it was written before implementation; amend
      only if implementation diverged)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

*None — this change is fully contained within `server/`, with no
consuming project, deployment config, or third-party integration affected.*
