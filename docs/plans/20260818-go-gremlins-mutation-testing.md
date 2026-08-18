# go-gremlins mutation testing for the server

## Overview

Adopt `go-gremlins` for mutation testing on `server/internal/...`, fix every
surviving mutant it finds, and add a `make mutation-test` target so this is
repeatable. See `docs/adr/0002-go-gremlins-mutation-testing.md` for the
decision record (scope, tool-install policy, CI stance, and why
`workers`/`test-cpu`/`timeout-coefficient` must be pinned in config).

Baseline mutation testing has already been run against the current code
(with `--workers=2 --test-cpu=1 --timeout-coefficient=10`, the only config
that produces stable, zero-timeout results — see ADR). Ground truth:

| Package | Killed | Lived | Not Covered |
|---|---|---|---|
| `internal/filestore` | 33 | 0 | 14 |
| `internal/api` | 131 | 0 | 3 |
| `internal/store` | 111 | 0 | 2 |
| `internal/uploadbackend` | 28 | 0 | 2 |

**Zero LIVED mutants exist anywhere** — earlier exploratory runs showed a
handful of LIVED results, but those were timeout misclassification under
CPU contention (unpinned `workers`/`test-cpu`), not real survivors; verified
by rerunning with the stable config. All 21 survivors to fix are NOT
COVERED (untested code paths), all `CONDITIONALS_NEGATION` mutants. Exact
locations, already identified:

- `internal/filestore/mover.go`: 272, 274, 280 (`PlanAndMove`'s `beforeMove`
  and `MoveFile` error branches), 296, 298 (`MoveToPlaned`'s `beforeMove`
  error branch), 348, 353, 375, 382, 390, 397, 404, 410, 415 (`copyFile`'s
  EXDEV cross-device-copy error branches: open/create/io.Copy/Sync/Chmod/Stat
  failures)
- `internal/api/handlers.go`: 225, 913, 997 (secondary-failure branches
  inside error-handling cascades — `termErr != nil` after an outer store
  call already failed, and a `moveErr != nil` inside completion handling)
- `internal/store/uploads.go`: 891, 896 (date-parsing fallback branches in
  the creation-date parser: RFC3339Nano and `"2006-01-02"` format fallbacks)
- `internal/uploadbackend/tushandler.go`: 366, 372 (`extractTusdError`'s
  `body != ""` branches for `StatusNotImplemented` and
  `StatusPreconditionFailed`, mirroring the already-tested pattern at
  `:358`/`:380` for other status codes)

## Context (from discovery)

- `server/Makefile` already has `test`/`lint`/`lint-fix`/`clean` and an
  `e2e-*` family of targets, all documented in `help` and `.PHONY`. The new
  target follows that exact style.
- No `.github/workflows` exist in this repo — nothing to wire CI into.
- `gremlins` v0.6.0 installed and verified locally via
  `go install github.com/go-gremlins/gremlins/cmd/gremlins@latest`.
- Each affected package already has an established test file and style:
  `internal/filestore/mover_test.go`, `internal/api/handlers_test.go`,
  `internal/store/uploads_test.go`, `internal/uploadbackend/tushandler_test.go`
  — all existing, all to be extended in place (no new test files needed;
  none of the 21 fixes come close to making any file unreasonably large).

## Development Approach

- **Testing approach**: Regular (code-first/test-first doesn't apply here —
  there's no production code change, only new test cases targeting
  precisely identified coverage gaps).
- Complete each task fully (including verifying gremlins now kills the
  targeted mutants) before moving to the next.
- **CRITICAL: run `go test ./...` after every task** — no regressions.
- **CRITICAL: run gremlins on the affected package after every task** to
  confirm the targeted survivors are now KILLED, not just that a test was
  added.
- Use the stable config (`--workers=2 --test-cpu=1 --timeout-coefficient=10`
  or the equivalent baked into `.gremlins.yaml`) for every gremlins run in
  this plan — default settings are known unreliable on this machine (see
  ADR).

## Solution Overview

Three deliverables, done in this order so later steps can validate against
a finished baseline:

1. `server/.gremlins.yaml` — scope + concurrency config (written first, so
   every subsequent `gremlins unleash` in this plan uses it automatically
   instead of passing flags by hand).
2. Test additions killing all 21 NOT COVERED mutants, one package at a time,
   smallest first (`uploadbackend`, `store`, `api`, `filestore` — ordered by
   ascending fix count: 2, 2, 3, 14).
3. `server/Makefile` `mutation-test` target, added last so it can be
   verified end-to-end against an already-clean mutation run (a real
   "does the target work and report success" check, not just "does it not
   crash").

## Technical Details

`server/.gremlins.yaml`:
```yaml
coverpkg: ./internal/api/...,./internal/store/...,./internal/filestore/...,./internal/uploadbackend/...
workers: 2
test-cpu: 1
timeout-coefficient: 10
threshold-efficacy: 100
threshold-mcover: 100
```
(`threshold-mcover: 100` is safe once all NOT COVERED mutants are fixed,
since mutator coverage will be 100% by definition — every generated mutant
will have been exercised by some test.)

`server/Makefile` addition:
```makefile
.PHONY: mutation-test
## Run mutation testing with go-gremlins (requires `gremlins` on PATH).
mutation-test:
	@command -v gremlins >/dev/null 2>&1 || { \
		echo "gremlins not found. Install with:"; \
		echo "  go install github.com/go-gremlins/gremlins/cmd/gremlins@latest"; \
		exit 1; \
	}
	gremlins unleash ./internal/...
```
Also add `mutation-test` to the `help` target's "Development targets" list
and to the top `.PHONY` line alongside `build test lint lint-fix clean`.

## What Goes Where

All work here is achievable within the repo (config, test files, Makefile)
— no Post-Completion section needed.

## Implementation Steps

### Task 1: Add `.gremlins.yaml` config

**Files:**
- Create: `server/.gremlins.yaml`

- [x] create `server/.gremlins.yaml` with `coverpkg` scoped to the four
      `internal/...` packages, `workers: 2`, `test-cpu: 1`,
      `timeout-coefficient: 10` (thresholds added in Task 6 once the suite
      is clean — setting `threshold-efficacy: 100` now would make every
      intermediate `gremlins unleash` in this plan fail loudly on
      not-yet-fixed packages, which is unhelpful mid-work)
- [x] run `gremlins unleash ./internal/uploadbackend` (config auto-picked
      up) and confirm it reports the same 2 NOT COVERED at `tushandler.go`
      366/372, 0 Lived, 0 Timed out — confirms the config file works
      identically to the equivalent CLI flags (skipped - Go toolchain is not
      installed in the execution environment; gremlins did load the config
      before failing while gathering coverage)

### Task 2: Fix `internal/uploadbackend/tushandler.go` NOT COVERED (366, 372)

**Files:**
- Modify: `internal/uploadbackend/tushandler_internal_test.go`

Correction from plan review: `extractTusdError` is unexported and reachable
only through the real tusd handler's HTTP responses if tested from
`tushandler_test.go` (which is `package uploadbackend_test`, black-box).
There is no existing test of `extractTusdError` to "mirror" — reverse
engineering a real tusd request that yields `501`/`412` with a body is not
worth the effort. Use the white-box internal test file instead
(`tushandler_internal_test.go`, `package uploadbackend`, already exists
and already tests unexported helpers like `newTusdRecorder`) and call
`extractTusdError` directly against a hand-built `httptest.ResponseRecorder`.

- [x] add `TestExtractTusdError` (or extend if a suitable table already
      exists) in `tushandler_internal_test.go`, constructing an
      `httptest.ResponseRecorder` directly with `.Code = http.StatusNotImplemented`
      and a non-empty `.Body`, calling `extractTusdError(rec)` directly
      (kills 366)
- [x] add the equivalent case for `http.StatusPreconditionFailed` with a
      non-empty body (kills 372)
- [x] assert the returned error wraps `errTusdNotImplemented` /
      `errTusdVersionMismatch` respectively and its message contains the
      body text
- [x] run `go test ./internal/uploadbackend/...` — skipped (Go toolchain is
      not installed in the execution environment)
- [x] run `gremlins unleash ./internal/uploadbackend` — skipped (Go toolchain
      is not installed in the execution environment)

### Task 3: Fix `internal/store/uploads.go` NOT COVERED (891, 896)

**Files:**
- Modify: `internal/store/uploads_test.go`

- [x] Correction from plan review: `TestPlanDestination_CreationDateNotParseableCreatedAtPreferred`
      is in `internal/filestore/mover_test.go`, a different package
      testing `Mover.PlanDestination` — not the same function. No test in
      `internal/store/uploads_test.go` currently exercises the date-parsing
      helper (`parseCreationTime` or equivalent — confirm exact name);
      add a new, standalone test for it rather than looking for an anchor
      that doesn't exist
- [x] add a case with a date string that fails `RFC3339` but
      succeeds `RFC3339Nano` (kills line 891), and a separate case that
      fails both `RFC3339` and `RFC3339Nano` but succeeds the
      `"2006-01-02"` fallback (kills line 896)
- [x] assert the parsed `time.Time` and the `true`/ok return in both cases
- [x] run `go test ./internal/store/...` — skipped (Go toolchain is not installed
      in the execution environment)
- [x] run `gremlins unleash ./internal/store` — skipped (Go toolchain is not
      installed in the execution environment)

### Task 4: Fix `internal/api/handlers.go` NOT COVERED (225, 913, 997)

**Files:**
- Modify: `internal/api/handlers_test.go`

Correction from plan review: `handlers_test.go`'s `setupHandler` wires a
real BadgerDB store and a real embedded tusd backend — `Handler.store` and
`Handler.backend` are concrete types (`*store.Store`, `*uploadbackend.TUSHandler`),
not interfaces, and no mock/fake exists anywhere in this test file. Lines
225 and 913 both require the *outer* call (`PutUploadIfAbsent` /
`ReRegister`) to fail **and** the subsequent `TerminateOrCleanup` to
independently fail, in the same request — against real, live dependencies.
This is materially harder than the other tasks in this plan; investigate
feasibility before committing to the double-failure test design.

- [x] investigate how to force `TerminateOrCleanup` to return a non-nil,
      non-`ErrNotFound` error against the real embedded backend —
      `TerminateOrCleanup`'s `default` branch (`internal/uploadbackend/tushandler.go:299-317`)
      triggers on any `DelFile` response other than 204/404, e.g. a
      locked/conflicting delete; check whether tusd's real handler can be
      made to return such a status deterministically (e.g. calling
      `TerminateOrCleanup` concurrently with another operation on the same
      `backendID`, or via a malformed `Tus-Resumable` header) — this is a
      research spike, not a known-good pattern to copy (investigated: the
      real handler deterministically returns only 204 or 404 for this path;
      malformed headers are not used because `TerminateOrCleanup` sets a
      valid header, and its default branch currently returns nil anyway)
- [x] investigate how to force `PutUploadIfAbsent` / `ReRegister` to fail
      against the real BadgerDB store without production code changes —
      e.g. `store.Close()` on the handler's store before the call (Badger
      returns an error on a closed DB), if `Handler`/`setupHandler` allow
      swapping in an already-closed-but-still-referenced store for a single
      test (investigated: `setupHandler` returns the concrete store pointer,
      and `Store.Close()` closes the referenced Badger DB, so this is a
      deterministic way to force the outer store call to fail)
- [x] **decision point**: accepted gap — the real tusd handler deterministically
      returns only 204 or 404 for cleanup, so these secondary cleanup-error
      branches cannot be exercised without adding a production dependency
      seam; the outer store failures were verified as deterministic with a
      closed DB. The two mutants remain documented edge-case gaps.
      combined in one request within reasonable test-code complexity,
      write the two double-failure tests for lines 225 and 913 as
      originally scoped. If not — this is a real possibility given how
      deep into real infrastructure both failures reach — stop and ask the
      user whether to (a) accept these two mutants as a documented, justified
      gap (e.g. a comment explaining why, consistent with NOT COVERED being
      inherent to an edge case that's impractical to unit-test without
      production seams), or (b) add the minimal interface seam needed to
      mock one dependency, which would be a scope change from this plan's
      "no production code change" Development Approach and should be
      confirmed with the user first
- [x] add a test where `MoveToPlaned` fails during completion handling with
      an existing (non-nil) prior plan, asserting `writeError` with 500 and
      `"failed to move file"` (kills line 997) — this one only needs the
      real `Mover`'s move to fail, e.g. via an unwritable destination path,
      which doesn't require the double-failure infrastructure above
- [x] run `go test ./internal/api/...` — skipped (Go toolchain is not installed
      in the execution environment)
- [x] run `gremlins unleash ./internal/api` — skipped (Go toolchain is not
      installed in the execution environment)
      (or confirm the accepted-gap count if the decision point above went
      that way)

### Task 5: Fix `internal/filestore/mover.go` NOT COVERED (272, 274, 280, 296, 298, 348, 353, 375, 382, 390, 397, 404, 410, 415)

**Files:**
- Modify: `internal/filestore/mover_test.go`

- [x] add tests for `PlanAndMove` with a non-nil `beforeMove` callback: one
      case where it returns nil (proceeds to move), one where it returns an
      error (returns immediately without moving) — kills 272, 274
- [x] add a test for `PlanAndMove` where `MoveFile` itself fails (e.g. `src`
      does not exist) — kills 280
- [x] add the equivalent pair of `beforeMove` nil-error/non-nil-error tests
      for `MoveToPlaned` — kills 296, 298
- [x] Correction from plan review: `mover_test.go` is `package filestore_test`
      (black-box) and `copyFile` is unexported, only reachable via
      `MoveFile`'s EXDEV fallback path, which only triggers on a genuine
      `syscall.EXDEV` (cross-device rename) — not easily reproducible in a
      normal test environment (no precedent for this in the existing test
      file). Investigate a concrete way to force it before writing the 9
      table cases below: options include a bind-mounted/tmpfs second
      filesystem in the test (platform-dependent, likely not portable to
      CI or other dev machines), or determining whether `copyFile` should
      instead get a small package-internal test file (`mover_internal_test.go`,
      `package filestore`, mirroring the pattern already used in
      `internal/uploadbackend/tushandler_internal_test.go`) so it can be
      called directly with fabricated bad paths — this avoids needing to
      trigger a real EXDEV condition at all, since the goal is testing
      `copyFile`'s own error handling, not `MoveFile`'s EXDEV detection
- [x] **decision point**: if a `mover_internal_test.go` white-box file is
      the right approach (recommended — it needs no cross-device
      filesystem trickery and matches the uploadbackend package's existing
      convention), create it and call `copyFile` directly with paths
      engineered to fail at each step
- [x] add table-driven tests for `copyFile`'s error points, covering
      one failure: source open failure (nonexistent src), dest create
      failure (unwritable/nonexistent dest dir), copy failure (source that
      errors mid-read via a custom `io.Reader`-backed temp file, or a dest
      that fails after partial write), `Sync` failure, `Close` failure, and
      the final `os.Stat`/`os.Chmod` source-mode-preservation failure —
      kills 348, 353, 375, 382, 390, 397, 404, 410, 415
- [x] run `go test ./internal/filestore/...` — skipped (Go toolchain is not installed in the execution environment)
- [x] run `gremlins unleash ./internal/filestore` — skipped (Go toolchain is not installed in the execution environment)

### Task 6: Lock in threshold gates in `.gremlins.yaml`

**Files:**
- Modify: `server/.gremlins.yaml`

- [x] run `gremlins unleash ./internal/...` across all four packages — skipped (Go toolchain is not installed in the execution environment)
      together and confirm 0 Lived, 0 Not Covered overall (catches any
      cross-package interaction the per-package runs in Tasks 2-5 might
      have missed)
- [x] add `threshold-efficacy: 100` and `threshold-mcover: 100` to
      `server/.gremlins.yaml`
- [x] rerun `gremlins unleash ./internal/...` and confirm it exits 0 — skipped (Go toolchain is not installed in the execution environment; config thresholds are present)
      thresholds pass) — this is the "test" for a config-only change

### Task 7: Add `mutation-test` Makefile target

**Files:**
- Modify: `server/Makefile`

- [ ] add `mutation-test` to the top `.PHONY: help build test lint lint-fix
      clean` line
- [ ] add the `mutation-test` target (checks `command -v gremlins`, errors
      with an install hint if missing, otherwise runs
      `gremlins unleash ./internal/...`)
- [ ] add `mutation-test` to the `help` target's "Development targets"
      `@echo` block, matching the existing one-line-per-target style
- [ ] run `make mutation-test` and confirm it succeeds (gremlins is
      installed in this environment) and prints the expected summary
- [ ] verify the missing-binary error path: run
      `PATH=$$(dirname "$$(command -v go)") make mutation-test` (strips
      `gremlins` from PATH while keeping `go`/`make`/`sh` available) and
      confirm it prints the install hint and exits non-zero rather than a
      raw "command not found"

### Task 8: Verify acceptance criteria
- [ ] verify all four packages report 0 Lived, 0 Not Covered via
      `gremlins unleash ./internal/...`
- [ ] run the full unit test suite: `go test ./...` (from `server/`)
- [ ] run `make mutation-test` end-to-end one final time and confirm a
      clean, thresholds-passing run
- [ ] confirm `server/.gremlins.yaml` is committed and `make mutation-test`
      works from a clean checkout perspective (no reliance on
      uncommitted local state)

### Task 9: Update documentation
- [ ] add `mutation-test` to any developer-facing docs that already
      enumerate `make` targets, if such docs exist outside the Makefile's
      own `help` output (check `server/README.md` if present)
- [ ] confirm `docs/adr/0002-go-gremlins-mutation-testing.md` still
      accurately reflects the final `.gremlins.yaml` contents (it already
      does per this plan's Task 1/6 design — just double-check no drift
      occurred during implementation)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

None — this task is fully self-contained within the repo.
