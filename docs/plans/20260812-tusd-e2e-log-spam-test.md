# E2E test: no NetworkTimeoutError log spam during PATCH (issue #8)

## Overview

Issue #8 reported tusd logging `NetworkTimeoutError ... error="feature not
supported"` dozens of times per chunk `PATCH`. The fix (`tusdRecorder`
no-op-deadline adapter, `server/internal/uploadbackend/tushandler.go`) and an
internal Go regression test (`tushandler_internal_test.go`, using a fake
`slog.Logger`) are already implemented and merged on this branch
(`docs/plans/completed/20260811-tusd-recorder-deadline-noop.md`).

That internal test proves the adapter is correct in isolation, but nothing
in the suite exercises the real deployment: a chunked `PATCH` sent over
actual HTTP, through Caddy, into the containerized server, with the real
tusd logger configured by `main.go` — the exact path the original bug
report came from. This plan adds that missing black-box e2e test to
`server/e2e/`, using the existing docker-compose e2e stack, so a future
regression (e.g. someone reverting the adapter, or a tusd upgrade changing
the deadline-probing behavior) is caught at the same layer the bug was
originally observed.

## Context (from discovery)

- **Existing e2e suite**: `server/e2e/` (build tag `e2e`), runs against a
  live `docker-compose.e2e.yml` stack (Go server + Caddy reverse proxy) via
  `make e2e` (up → wait → test → down) or `make e2e-test` (test only,
  against an already-running stack).
- **Client helpers**: `server/e2e/client_test.go` has `PatchUploadData(id,
  data, offset, uploadLength)` and `CreateUpload(...)`;
  `server/e2e/fixtures_test.go` has `MakeLocalIdentifier` and
  `CreateCompleteUpload` helpers already used by `resume_test.go` to drive
  multi-chunk uploads.
- **Docker log/introspection pattern already exists**:
  `server/e2e/storage_test.go` has `requireDockerStorage(t)` /
  `storageAccess`, gated on `COMPOSE_PROJECT_NAME` (or
  `E2E_COMPOSE_PROJECT`) being set and the `docker` CLI being on `PATH`,
  skipping otherwise (so the suite still runs against a remote `SERVER_URL`
  with no local stack). `make e2e`/`make e2e-test` export
  `COMPOSE_PROJECT_NAME` automatically, so this always runs in the normal
  local/CI flow. This plan follows the same skip-if-unavailable pattern for
  reading `docker compose logs`.
- **Fix under test**: `server/internal/uploadbackend/tushandler.go` — all
  three tusd call sites (`CreateUpload`, `ForwardPatch`,
  `TerminateOrCleanup`) already use `newTusdRecorder()` instead of
  `httptest.NewRecorder()` directly, so `SetReadDeadline`/`SetWriteDeadline`
  return `nil` instead of `http.ErrNotSupported`.
- **Test command**: `cd server && go test ./...` for the regular suite;
  `cd server && make e2e` for the full e2e stack run.

## Development Approach

- **Testing approach**: Regular (the test itself IS the deliverable — there
  is no separate production code change in this plan)
- Complete the task fully, run it against the real stack before finishing
- Update this plan file if scope changes during implementation

## Solution Overview

Add one new e2e test file, `server/e2e/logspam_test.go`, with a single test:

1. Perform one `PATCH` upload (create + a single chunk carrying
   `Upload-Length` to complete it) large enough to guarantee tusd's
   `writeChunk`/`onReadDone` deadline-setting path runs multiple times
   within that one `PATCH` — the regression is per-read-tick *within* a
   single PATCH, so a second chunk would add runtime without adding
   coverage.
2. Read the `server` container's logs via `docker compose -p <project> -f
   <composeFile> logs server` (reusing the `storageAccess`-style gating:
   skip if `docker` CLI or `COMPOSE_PROJECT_NAME`/`E2E_COMPOSE_PROJECT`
   unavailable).
3. Assert the captured log output contains a known tusd log line (e.g.
   `ChunkWriteStart`, which tusd logs at Info on every `writeChunk` call) —
   a positive control proving the tusd logger's output actually reaches
   `docker compose logs` for this run, so the negative assertion below
   can't pass vacuously (e.g. on an empty/wrong-stream log capture).
4. Assert the captured log output does not contain `NetworkTimeoutError`.

This directly pins the regression at the black-box layer: if the
`tusdRecorder` adapter is ever reverted or a tusd upgrade reintroduces the
warning, this test fails against the real running server, not just an
in-process construction.

## Technical Details

- **Pre-existing path bug to fix first**: `storageAccess.composeFile`
  defaults to the relative path `docker-compose.e2e.yml`
  (`storage_test.go:60-63`), but `go test ./e2e/...` runs with cwd =
  `server/e2e/`, one directory below where `docker-compose.e2e.yml` and
  `server/Makefile` actually live. `server/Makefile`'s `e2e`/`e2e-test`
  targets never export `E2E_COMPOSE_FILE`, so every `docker compose -f
  docker-compose.e2e.yml ...` call from a test binary fails with "no such
  file or directory" today — this already breaks `storage_test.go`'s
  `TestStorage_*` tests, not just the new test. Fix the default in
  `storage_test.go` to `"../docker-compose.e2e.yml"` (cwd-relative from
  `server/e2e/` to `server/`) so both the existing storage tests and the
  new log test actually run instead of erroring out.
- Reuse `requireDockerStorage(t)` from `storage_test.go` (already exported
  at package scope within `package e2e`) to get a `storageAccess{composeFile,
  project, service}` — no new gating helper needed.
- Add one small unexported helper next to `readFile`/`fileExists` in
  `storage_test.go` (not in the new file, to keep the type and its methods
  together): `func (s storageAccess) logs(t testing.TB) string`, running
  `docker compose -p <project> -f <composeFile> logs <service>` via
  `exec.Command` + `CombinedOutput()`, mirroring the existing
  `exec.Command("docker", "compose", ...)` pattern.
- Log timing: `docker compose logs` (no `--since`) dumps the full container
  log history, which is fine here — the assertion is "never contains
  NetworkTimeoutError anywhere", not scoped to a time window, so no flakiness
  from a missed cutoff.
- Assertion style: don't pass the full log dump to `require.NotContains`
  (its failure message would embed potentially megabytes of log text).
  Instead scan the captured output line by line, collect any line
  containing `NetworkTimeoutError`, and fail with just those lines (plus a
  count) if any are found.
- Chunk sizing: tusd's `onReadDone` fires per `bodyReader.Read` call inside
  `writeChunk` (`unrouted_handler.go`), and reads are driven by
  `io.Copy`'s default 32KB buffer in the filestore backend — so each ~32KB
  of body data is roughly one deadline-setting tick pre-fix. A 256KB
  payload (~8 ticks × 2 deadline calls ≈ ~16 warnings pre-fix) is comfortably
  enough to reproduce the bug on an unfixed adapter while keeping the test
  fast and inside `E2E_TIMEOUT`. Body size is not otherwise constrained —
  `HandlePatchUploadData` has no `MaxBytesReader` and `Caddyfile.e2e` sets
  `max_size 0`.

## What Goes Where

- **Implementation Steps**: the new e2e test file — entirely within this
  repo.
- **Post-Completion**: running the full `make e2e` locally to confirm the
  new test passes against the live stack (already required by the task
  itself, listed here only as a reminder that plan completion requires this
  since it can't be verified by `go test ./...` alone).

## Implementation Steps

### Task 1: Fix compose-file path resolution in storageAccess

**Files:**
- Modify: `server/e2e/storage_test.go`

- [ ] change the default in `requireDockerStorage` from `composeFile =
      "docker-compose.e2e.yml"` to `composeFile =
      "../docker-compose.e2e.yml"` (cwd-relative fix: `go test ./e2e/...`
      runs with cwd `server/e2e/`, one level below the compose file)
- [ ] run `cd server && make e2e` and confirm the existing
      `TestStorage_CompletedFileContentMatchesUploadedBytes` and
      `TestStorage_DeleteRemovesFileFromDisk` tests now actually exercise
      `docker compose cp`/`exec` (previously erroring on "no such file or
      directory") — must pass before task 2

### Task 2: Add e2e test asserting no NetworkTimeoutError log spam

**Files:**
- Create: `server/e2e/logspam_test.go`
- Modify: `server/e2e/storage_test.go`

- [ ] add `func (s storageAccess) logs(t testing.TB) string` next to
      `readFile`/`fileExists` in `storage_test.go`, running `docker compose
      -p <project> -f <composeFile> logs <service>` via `exec.Command` +
      `CombinedOutput()`, following the existing `require.NoError` pattern
- [ ] write `TestLogSpam_NoNetworkTimeoutErrorDuringChunkedPatch` in
      `logspam_test.go`: call `requireDockerStorage(t)` to skip when no
      local stack is available, create an upload via `CreateUpload`, send
      one `PATCH` with a 256KB payload carrying `Upload-Length` to complete
      it (see Technical Details for the 256KB/32KB-tick sizing rationale),
      assert the `PATCH` returned 204
- [ ] fetch logs via the new helper; assert (positive control) the output
      contains `ChunkWriteStart` — proves tusd's logger output actually
      reached `docker compose logs` for this run, so the negative
      assertion below can't pass vacuously
- [ ] assert (negative control) the output contains no line matching
      `NetworkTimeoutError` — scan line by line and fail with only the
      matching lines (plus a count), not the full log dump
- [ ] run `cd server && make e2e` — must pass against the live stack before
      considering this task done (this is the only way to verify the test;
      `go build -tags=e2e ./e2e/...` only confirms it compiles)
- [ ] run `cd server && go vet -tags=e2e ./e2e/...`
- [ ] locally and temporarily revert `newTusdRecorder()` back to
      `httptest.NewRecorder()` in `tushandler.go` (uncommitted), re-run
      `make e2e`, and confirm
      `TestLogSpam_NoNetworkTimeoutErrorDuringChunkedPatch` now fails —
      proves the test actually detects the regression it's meant to catch,
      not just passing vacuously. Revert the temporary change afterward
      (`git checkout -- server/internal/uploadbackend/tushandler.go`)

### Task 3: [Final] Move plan

No production code or doc changes needed — test-only addition.

- [ ] move this plan to `docs/plans/completed/`
