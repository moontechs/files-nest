# Strict Go Linting + Pre-commit Hook for server/

## Overview
- Add a maximum-strictness `golangci-lint` configuration for the Go server (`server/.golangci.yml`): `default: all`, disabling only the two linters that are genuinely deprecated (`wsl`, `gomodguard` — both have non-deprecated successors, `wsl_v5`/`gomodguard_v2`, also enabled). Every other non-deprecated linter stays on, per explicit user decision — this is a deliberately larger remediation than a "reasonable strict baseline" would require.
- **Verified scope** (measured directly against this tree with `default: all`, truncation caps removed): 2716 total violations. Splitting test vs. non-test files: **~490 in non-test source**, **~2226 in `_test.go` files** (dominated by `wsl` 811, `paralleltest` 328, `varnamelen` 325, `noinlineerr` 176 in tests alone). Because tests get relaxed exclusions for style-only linters (see below), the real remaining manual-fix surface is roughly **~300 in non-test code + ~320 in test code that survive the test exclusions** (gosec, errcheck, bodyclose, noctx, cyclop, depguard, err113, exhaustruct, revive, goconst, etc. all still apply to tests) — call it **~600-700 manual fixes total**, most single-line/mechanical, some requiring real judgment (see gosec note below). This is a multi-session effort, not a quick pass — sized honestly here rather than assumed away.
- Fix all existing lint violations so the repo starts from a clean state, with **one explicit scope carve-out**: `_test.go` files get relaxed exclusions for linters that are pure code-style/formatting with no correctness signal (`wsl`, `wsl_v5`, `nlreturn`, `noinlineerr`, `varnamelen`, `paralleltest`) — every other linter, including all defect-relevant ones (gosec, errcheck, bodyclose, noctx, etc.), still applies to test files at full strictness. This is a deliberate, user-confirmed deviation from a literal "zero violations everywhere," stated here rather than left implicit.
- **Security note**: `gosec` currently flags ~47 issues, ~24 of them in non-test code inside `internal/filestore/mover.go`, `internal/api/handlers.go`, `internal/api/router.go`, `internal/uploadbackend/tushandler.go`, and `internal/api/recovery.go` — precisely the file-path-handling and HTTP-auth code paths in a self-hosted backup server where a `gosec` finding (e.g. path traversal, file permissions) could be a real vulnerability rather than noise. These are called out as requiring genuine security review, not mechanical `//nolint` suppression.
- Add a plain shell pre-commit hook (no new dependency) that runs the linter only when staged files live under `server/`, blocking the commit on failure.
- Document manual lint invocation and one-time hook installation in `server/README.md`.
- CI is explicitly out of scope for this plan (repo has no `.github/workflows` today) — issue's CI criterion is conditional and unmet, so it's satisfied by omission.

Closes moontechs/files-nest#19.

## Context (from discovery)
- Server module: `server/` (module `github.com/moontechs/files-nest/server`, go 1.26.2), 4 packages under `internal/` (`api`, `filestore`, `store`, `uploadbackend`), ~19k LOC total, no vendored tusd (`go.mod` dependency instead).
- No existing `.golangci.yml` anywhere in the project. No `.github/workflows` directory exists — there is no CI to extend.
- No git hook framework present (no lefthook/husky/pre-commit-framework), and `.git/hooks/` has only the default `.sample` files.
- `server/Makefile` already has `help`/`build`/`test`/`clean`/e2e targets in a documented format — new `lint`/`lint-fix` targets should follow the same `## comment` style used for `make help` auto-generation.
- `golangci-lint` v2.12.2 is installed locally; the golang-lint skill ships a reference config at `.agents/skills/golang-lint/assets/.golangci.yml` (33-linter baseline) to start from, but per user decision we go further and enable nearly all non-deprecated linters.
- Repo uses `git config core.hooksPath` pattern is not yet set up; hooks must be checked into the repo (e.g. `server/.githooks/pre-commit` or top-level `.githooks/`) since `.git/hooks/` isn't versioned.

## Development Approach
- **Testing approach**: Regular (code first, then tests) — this is tooling/config work, not application logic.
- Complete each task fully (config change or code fix) before moving to the next.
- Make small, focused changes; fix lint violations package-by-package to keep diffs reviewable.
- **CRITICAL: every task MUST include new/updated tests** where the task changes runnable logic (the hook script itself gets a self-check test; pure `.golangci.yml` config and doc changes do not need Go tests but the hook DOES need a script-level check).
- **CRITICAL: all tests must pass before starting next task** - no exceptions.
- **CRITICAL: update this plan file when scope changes during implementation.**
- Run `golangci-lint run ./...` and `go test ./...` after each change.
- Maintain backward compatibility (server behavior must not change — lint fixes are style/safety only, no behavior changes without calling it out).

## Testing Strategy
- **unit tests**: N/A for lint config changes themselves; any behavior-relevant fix (e.g. an `errcheck` fix that changes a code path) gets a regression test in the affected package.
- **hook self-check**: a small bash test script (`server/.githooks/pre-commit_test.sh`) that stages a fake violation in a temp git repo and asserts the hook blocks the commit, and asserts it's skipped when no `server/` files are staged.
- **e2e tests**: not applicable — this change has no UI/HTTP-facing behavior.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Solution Overview
- Config lives at `server/.golangci.yml` (scoped to the Go module) rather than repo root, since only `server/` is Go code and the hook only ever runs from `server/`.
- Linter set: `linters: {default: all, disable: [wsl, gomodguard]}` — true maximum strictness per user decision. Only these two are disabled, both because they are marked `[deprecated]` by golangci-lint itself with non-deprecated successors already enabled under `default: all` (`wsl_v5`, `gomodguard_v2`) — not a style judgment call, a mechanical "don't run a dead linter" call.
- `depguard` (enabled, 44 baseline hits) needs an explicit allow-list or it flags every import; configure it in Task 1 by allow-listing Go stdlib plus every module prefix already present in `server/go.mod` (`github.com/dgraph-io/*`, `github.com/tus/tusd/v2`, `github.com/stretchr/testify`, `go.opentelemetry.io/*`, `golang.org/x/*`, `google.golang.org/protobuf`, `gopkg.in/yaml.v3`, and their transitive deps already in `go.sum`) plus the module's own import path. This is config work, not a per-callsite fix.
- Test files (`_test.go`) get relaxed exclusions in `linters.exclusions.rules` for exactly the linters that are pure formatting/style with no correctness signal: `wsl`, `wsl_v5`, `nlreturn`, `noinlineerr`, `varnamelen`, `paralleltest`. Every other linter — `gosec`, `errcheck`, `bodyclose`, `noctx`, `cyclop`, `depguard`, `err113`, `exhaustruct`, `revive`, `goconst`, etc. — still applies to test files at full strictness. This exclusion set and its boundary are the user-confirmed scope decision recorded in Overview.
- Hook mechanism: a versioned script at `.githooks/pre-commit` (repo root, since the hook must inspect staged paths across the whole repo to decide relevance) that:
  1. checks `golangci-lint` is on PATH; if not, fails with a clear install hint (never silently skips)
  2. runs `git diff --cached --name-only --diff-filter=ACM` to get staged files
  3. greps for paths under `server/`
  4. if none, exits 0 immediately (no lint run)
  5. if any, `cd server && golangci-lint run ./...` and blocks (non-zero exit) on failure
- Installation: documented as `git config core.hooksPath .githooks` (one command, no new tooling), added to `server/README.md`.
- Manual lint command: `make lint` (new Makefile target) documented as the single canonical command, wrapping `golangci-lint run ./...` from within `server/`.

## Technical Details
- `.golangci.yml` uses golangci-lint v2 schema (`version: "2"`, `linters:`/`formatters:` split, matching the v2.12.2 CLI already installed). README notes golangci-lint v2+ is required.
- `formatters:` block (not `linters:`) carries `gofmt`/`gofumpt`/`goimports`-equivalent — the most common v2 config mistake is listing these under `linters`.
- Baseline measurement in Task 1 MUST set `issues.max-issues-per-linter: 0` and `issues.max-same-issues: 0` — v2 defaults (50 / 3) silently truncate output and would under-count the real violation surface by ~5x. (Independently reproduced during planning: 2716 total violations with these caps removed — matches the plan's sizing.)
- No explicit `run.timeout` override — v2 defaults to no timeout, which is correct for an uncached local/pre-commit run on this module size; a low cap only adds a spurious failure mode on a cold cache.
- Linters requiring config knobs to be tolerable at this strictness: `lll` (line length), `funlen`/`gocognit`/`cyclop`/`nestif`/`maintidx` (complexity thresholds), `mnd` (magic number exceptions for common values), `varnamelen` (min-length + exceptions for conventional short names like `id`, `ok`, `db`, `w`, `r` — still enabled per user decision, so tune rather than disable), `tagliatelle`/`tagalign` (struct tag style — check current JSON tag conventions in `internal/store`/`internal/api` before configuring), `exhaustruct` (exclude generated/vendored-shape structs if any exist, otherwise fix at callsites).
- Deliberately disabled linters get a one-line YAML comment stating why — with only `wsl`/`gomodguard` disabled, this is a short, easily-justified list rather than a long one requiring case-by-case defense.

## What Goes Where
- **Implementation Steps**: `.golangci.yml`, Makefile targets, all Go source fixes, hook script + its test, README docs.
- **Post-Completion**: verifying hook behavior against the user's real local git config, any editor/IDE golangci-lint integration setup (VS Code / GoLand settings) is left to the individual developer.

## Implementation Steps

### Task 1: Add maximum-strictness `.golangci.yml` and baseline the violation surface

**Files:**
- Create: `server/.golangci.yml`

- [ ] create `server/.golangci.yml` (v2 schema) with `linters: {default: all, disable: [wsl, gomodguard]}` (one-line rationale comment each: both deprecated with enabled successors `wsl_v5`/`gomodguard_v2`)
- [ ] configure `depguard` with an explicit allow-list covering Go stdlib, `github.com/moontechs/files-nest/server`, and every module path in `server/go.mod`/`go.sum` (see Solution Overview) — without this it flags all 44 baseline import sites
- [ ] add settings blocks for `lll`, `funlen`, `gocognit`, `cyclop`, `nestif`, `maintidx`, `mnd`, `varnamelen` (tune thresholds/exceptions rather than disable, per user decision to keep these enabled)
- [ ] add `linters.exclusions.rules` relaxing exactly `wsl`, `wsl_v5`, `nlreturn`, `noinlineerr`, `varnamelen`, `paralleltest` for `_test.go` files (the user-confirmed style-only exclusion set — every other linter still applies to tests)
- [ ] add `issues.max-issues-per-linter: 0` and `issues.max-same-issues: 0` so no run is silently truncated
- [ ] run `golangci-lint run ./...` from `server/`, save full output to scratchpad to size up remaining fix work; do NOT fix anything yet
- [ ] write no Go tests this task (config-only); confirm `go build ./...` still succeeds unaffected
- [ ] run `golangci-lint run ./...` - record final remaining violation count and per-linter breakdown before Task 2 (baseline measured during planning: ~490 non-test + ~2226 test-file issues before exclusions; expect a large drop in test-file count after the exclusions above, non-test count unchanged since no non-test linters were disabled)

### Task 2: Apply formatters, then auto-fixable linters

**Files:**
- Modify: all `server/**/*.go` files touched by autofix

- [ ] run `golangci-lint fmt ./...` from `server/` first (formatter-only pass: gofmt/gofumpt/goimports-equivalent under the `formatters:` block) — commit this as its own step before the next one
- [ ] run `golangci-lint run --fix ./...` to apply remaining auto-fixable linters (`staticcheck`, `gocritic`, `modernize`, `usestdlibvars`, `intrange`, `wsl_v5`, `nlreturn`, `perfsprint`, `copyloopvar`, `misspell`, `ineffassign`, `errorlint`, etc.)
- [ ] diff-review the auto-fixed changes for correctness (auto-fixes can occasionally change semantics, e.g. `copyloopvar`, `errorlint`)
- [ ] run `go test ./...` - must pass unchanged behavior before Task 3
- [ ] run `golangci-lint run ./...` - record remaining (manual-fix-only) violation count and per-linter breakdown before Task 3

### Task 3: Fix defect-relevant violations (errcheck, errorlint, gosec, bodyclose, sqlclosecheck, contextcheck, nilnil, nilerr, noctx, wrapcheck, exhaustive, errchkjson)

**Files:**
- Modify: `server/internal/**/*.go` and `server/internal/**/*_test.go`, `server/main.go` as needed

- [ ] fix all violations for this cluster across the whole module (including test files — none of these are in the test exclusion set)
- [ ] **treat every `gosec` finding as a real security review, not a mechanical fix** — pay particular attention to `internal/filestore/mover.go`, `internal/api/handlers.go`, `internal/api/router.go`, `internal/uploadbackend/tushandler.go`, `internal/api/recovery.go` (file-path and auth-handling code in a backup server); if a finding is a genuine false positive, suppress with `//nolint:gosec // specific reason`, never blanket-suppress
- [ ] add/update unit tests for any fix that changes an error path or behavior (e.g. an `errcheck` fix that now surfaces a previously-swallowed error, a `gosec` fix that changes file-permission or path-handling logic)
- [ ] write tests for edge cases uncovered while fixing (nil handling from `nilnil`/`nilerr`, unclosed body from `bodyclose`, missing context from `noctx`)
- [ ] run tests - must pass before task 4
- [ ] run `golangci-lint run ./...` - record remaining violation count

### Task 4: Fix style-correctness violations (noinlineerr, varnamelen, err113, exhaustruct, tagliatelle, godoclint, lll, mnd, revive, goconst, gosmopolitan, dogsled, nonamedreturns, unused, unparam, thelper, testpackage, embeddedstructfieldcheck, prealloc)

**Files:**
- Modify: `server/internal/**/*.go`, `server/main.go` as needed (non-test files only — these linters are excluded for `_test.go` where listed in the Task 1 exclusion set; remaining non-excluded ones like `err113`/`exhaustruct`/`tagliatelle`/`revive`/`goconst` still apply to tests too)

- [ ] fix all violations for this cluster; these are mechanical/pattern-based (e.g. `noinlineerr` extracts `if err := f(); err != nil {` into a separate assignment, `varnamelen` renames short identifiers per the tuned exceptions from Task 1) but numerous — work file-by-file, committing incrementally if the diff grows large enough to hurt reviewability
- [ ] no new tests required for pure rename/style fixes; add a regression test only if a fix changes observable behavior (rare for this cluster)
- [ ] run tests - must pass before task 5
- [ ] run `golangci-lint run ./...` - record remaining violation count

### Task 5: Fix complexity violations (cyclop, gocognit, nestif, maintidx, funlen)

**Files:**
- Modify: `server/internal/**/*.go`, `server/main.go` as needed

- [ ] for each remaining complexity outlier, decide per-function: refactor (extract a helper) if the complexity is accidental, or raise the threshold in `.golangci.yml` with an inline rationale comment if the complexity is inherent to the domain (e.g. a large protocol state machine) — do not force an artificial split just to satisfy the linter
- [ ] write tests for any behavior touched during refactors — extractions must not change external behavior; cover with existing or new tests before/after comparison
- [ ] run tests - must pass before task 6
- [ ] run `golangci-lint run ./...` (full repo) - zero violations anywhere

### Task 6: Add `make lint` / `make lint-fix` targets

**Files:**
- Modify: `server/Makefile`

- [ ] add `lint` target running `golangci-lint run ./...`, following existing `## comment` doc-style used by other targets
- [ ] add `lint-fix` target running `golangci-lint fmt ./... && golangci-lint run --fix ./...`
- [ ] add both to `.PHONY` and to the `make help` output block
- [ ] no Go tests needed (Makefile-only); manually run `make lint` and confirm it exits 0
- [ ] run `make test` - must still pass before task 7

### Task 7: Add pre-commit hook script

**Files:**
- Create: `.githooks/pre-commit`
- Create: `.githooks/pre-commit_test.sh`

- [ ] create `.githooks/pre-commit` (bash, executable) that: first checks `command -v golangci-lint` and fails with a clear install hint if missing (never silently skip the gate), then reads `git diff --cached --name-only --diff-filter=ACM`, skips (exit 0) if no path starts with `server/`, otherwise runs `(cd server && golangci-lint run ./...)` and exits non-zero on failure with a clear message
- [ ] `chmod +x .githooks/pre-commit`
- [ ] write `.githooks/pre-commit_test.sh`: a self-contained bash test with three scenarios — (1) stage a `server/`-path file with a deliberate lint violation, assert commit is blocked; (2) stage a non-`server/` file only (e.g. `README.md`), assert commit succeeds without invoking golangci-lint; (3) simulate `golangci-lint` missing from PATH (e.g. empty `PATH` override) with a staged `server/` file, assert the hook fails closed with the install-hint message
- [ ] run `.githooks/pre-commit_test.sh` - must pass (all three scenarios) before task 8
- [ ] run `golangci-lint run ./...` from `server/` - still zero violations (hook script is bash, not linted by golangci-lint)

### Task 8: Document lint command and hook installation

**Files:**
- Modify: `server/README.md`

- [ ] add a "Linting" section documenting `make lint` (and `make lint-fix`) as the single canonical manual command, noting golangci-lint v2+ is required
- [ ] add a "Pre-commit hook" section documenting one-time setup: `git config core.hooksPath .githooks` (run from repo root), what it does, that it only triggers on staged `server/` changes, and that it lints the current working-tree content of the module (not a snapshot of only staged hunks — relevant for partial `git add -p` stages)
- [ ] no tests needed (docs-only)
- [ ] run `make test` - must still pass before task 9

### Task 9: Verify acceptance criteria
- [ ] verify `server/.golangci.yml` exists with `default: all` and only `wsl`/`gomodguard` disabled (both deprecated), plus the test-file style exclusions and `depguard` allow-list documented
- [ ] verify `golangci-lint run ./...` from `server/` reports zero violations
- [ ] verify `make lint` is the single documented manual command and works
- [ ] verify hook runs and blocks on a staged `server/` violation (manual smoke test: `git config core.hooksPath .githooks`, stage a deliberate violation, attempt commit, confirm block, then discard)
- [ ] verify hook is skipped when only non-`server/` files are staged (manual smoke test)
- [ ] verify hook fails closed when `golangci-lint` is missing from PATH
- [ ] verify hook logic keys off staged files (`git diff --cached`), not working-tree state, for the trigger decision
- [ ] run full test suite: `cd server && make test`
- [ ] run e2e tests: `cd server && make e2e` if Docker is available locally (confirm Tasks 3-5 lint fixes didn't break server behavior — especially the `gosec` security fixes from Task 3); if Docker isn't available, note that in the plan and skip rather than treat as a blocker
- [ ] re-read issue #19 acceptance criteria list end-to-end and confirm each is met or explicitly deferred (CI item — deferred, no CI exists)

### Task 10: [Final] Update documentation
- [ ] update `server/README.md` if any additional gaps found during Task 9 verification
- [ ] update root `CLAUDE.md` (or `server/CLAUDE.md` if it exists) with the new lint/hook workflow if such a file exists in this repo
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- Each developer must run `git config core.hooksPath .githooks` locally once (it's a per-clone git config, not something committed config can force) — call this out clearly in the PR description too.
- Optional: developers using GoLand/VS Code may want to point their IDE's golangci-lint integration at `server/.golangci.yml` for inline feedback; not required for the hook to work.

**External system updates**:
- If/when CI is introduced for this repo in a future initiative, wire `golangci-lint-action` against `server/.golangci.yml` so CI and local hook share the exact same config (issue's conditional CI criterion becomes satisfiable then).
