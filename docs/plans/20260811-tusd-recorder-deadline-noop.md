# tusd recorder: no-op read/write deadlines

## Overview

`TUSHandler` (`server/internal/uploadbackend/tushandler.go`) drives tusd's
`UnroutedHandler` in-process, via `httptest.NewRecorder()`, instead of a real
`net/http` listener. tusd calls `http.NewResponseController(w).SetReadDeadline`
/ `SetWriteDeadline` on every body-read tick during `PATCH`. `httptest.ResponseRecorder`
doesn't implement these, so every call returns `http.ErrNotSupported`, and tusd
logs a `NetworkTimeoutError` WARN on every tick — dozens of times per chunk
PATCH ([issue #8](https://github.com/moontechs/files-nest/issues/8)).

This fixes it by wrapping the recorder in a small adapter that answers
`SetReadDeadline`/`SetWriteDeadline` as no-ops, satisfying the interface
`http.ResponseController` probes for. No behavior change — there was never a
real deadline to enforce in-process — only the spurious warning goes away.

Full rationale and rejected alternatives: `docs/adr/0001-no-op-deadlines-for-tusd-recorder.md`.

## Context (from discovery)

- **File involved**: `server/internal/uploadbackend/tushandler.go` — all three
  tusd call sites build a fresh `httptest.NewRecorder()`: `CreateUpload` (line
  102), `ForwardPatch` (line 154), `TerminateOrCleanup` (line 263).
- **Root cause confirmed against tusd v2.10.0 source**
  (`$(go env GOPATH)/pkg/mod/github.com/tus/tusd/v2@v2.10.0/pkg/handler/`):
  `unrouted_handler.go`'s `writeChunk` sets `c.body.onReadDone` to call
  `c.resC.SetReadDeadline`/`SetWriteDeadline` on every successful body read
  (`unrouted_handler.go:915-921`); `context.go:53` constructs `resC` via
  `http.NewResponseController(w)`.
- **Scope confirmed PATCH-only in practice**: `writeChunk` is shared by
  `PatchFile` (always) and `PostFile` (only for creation-with-upload, i.e. a
  non-empty body on `POST`). This project's `CreateUpload` always sends a nil
  body, so the warning is only observed on `ForwardPatch` today — but the
  fix is applied at all three recorder sites for consistency, per ADR.
- **Existing test file**: `server/internal/uploadbackend/tushandler_test.go`,
  uses a `setupTUSHandler(t)` helper (line 23) to construct a `*TUSHandler`
  backed by a temp dir.
- **Test command**: `cd server && go test ./...` (per `server/Makefile`'s
  `test` target).

## Development Approach

- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes
- Every task with code changes includes tests for that code
- All tests must pass before starting the next task
- Update this plan file if scope changes during implementation

## Solution Overview

Add one unexported type in `tushandler.go`: a struct embedding
`*httptest.ResponseRecorder` that adds `SetReadDeadline(time.Time) error` and
`SetWriteDeadline(time.Time) error` methods returning `nil`. A constructor
function builds the recorder and wraps it in one step, so all three call
sites use it instead of calling `httptest.NewRecorder()` directly. Embedding
(not a named field) is used because the type must promote the recorder's
full `http.ResponseWriter` (and the `.Code`/`.Header()`/`.Body` access used
the file) — the outer type "is a" recorder plus deadline support, not a
wrapper hiding it.

## Technical Details

```go
// tusdRecorder wraps httptest.ResponseRecorder to satisfy the deadline-setting
// interface http.ResponseController probes for. tusd calls SetReadDeadline/
// SetWriteDeadline on every body-read tick; ResponseRecorder doesn't implement
// them, so tusd logs a NetworkTimeoutError WARN on every tick. There is no real
// deadline to enforce for an in-process call, so these are no-ops.
type tusdRecorder struct {
	*httptest.ResponseRecorder
}

func newTusdRecorder() *tusdRecorder {
	return &tusdRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *tusdRecorder) SetReadDeadline(time.Time) error  { return nil }
func (r *tusdRecorder) SetWriteDeadline(time.Time) error { return nil }
```

Each of the three `rec := httptest.NewRecorder()` call sites becomes
`rec := newTusdRecorder()`. `*tusdRecorder` still satisfies every existing
usage of `rec` (`.Code`, `.Header()`, `.Body`) because these are promoted
from the embedded `*httptest.ResponseRecorder`.

## What Goes Where

- **Implementation Steps**: adapter type, wiring, tests — all within this repo.
- **Post-Completion**: manual verification against a real upload (per the
  ticket's repro steps) to confirm the WARN no longer appears in server logs.

## Implementation Steps

### Task 1: Add `tusdRecorder` adapter and wire it into all three call sites

**Files:**
- Modify: `server/internal/uploadbackend/tushandler.go`

- [x] add the `tusdRecorder` type and `newTusdRecorder()` constructor (see
      Technical Details) near the top of the file, after the type
      declarations and before `New()`
- [x] replace `rec := httptest.NewRecorder()` in `CreateUpload` with
      `rec := newTusdRecorder()`
- [x] replace `rec := httptest.NewRecorder()` in `ForwardPatch` with
      `rec := newTusdRecorder()`
- [x] replace `rec := httptest.NewRecorder()` in `TerminateOrCleanup` with
      `rec := newTusdRecorder()`
- [x] keep `extractTusdError(rec *httptest.ResponseRecorder)` unchanged; at
      its three call sites (in `CreateUpload`, `ForwardPatch`,
      `TerminateOrCleanup`) pass `rec.ResponseRecorder` (the embedded field)
      instead of `rec`
- [x] create `server/internal/uploadbackend/tushandler_internal_test.go` with
      `package uploadbackend` (an internal test file — `tushandler_test.go`
      and `tusd_api_test.go` are both `package uploadbackend_test` and can't
      reach the unexported `newTusdRecorder()`)
- [x] in that file, write one test asserting
      `http.NewResponseController(newTusdRecorder()).SetReadDeadline(time.Now())`
      and `.SetWriteDeadline(time.Now())` both return `nil` — this is the
      exact behavior tusd depends on; no separate "error case" exists since
      neither method can fail
- [x] write one end-to-end test that actually pins the bug: construct a
      `*TUSHandler` with `handler.Config.Logger` pointed at a `slog.Logger`
      wrapping a custom `slog.Handler` (or `slog.NewTextHandler` writing to a
      `bytes.Buffer`) that captures emitted records, drive a real
      `CreateUpload` + `ForwardPatch` with a non-trivial body through it, and
      assert the captured output contains no `NetworkTimeoutError` record.
      This is the only test that verifies issue #8 is actually fixed — the
      interface-satisfaction test above only proves the adapter is wired,
      not that tusd stops logging when driven through it. Note:
      `New()` (tushandler.go) currently doesn't expose a way to override
      `handler.Config.Logger` — extend it minimally (e.g. an unexported test
      constructor in the same internal test file that builds the
      `handler.UnroutedHandler` with a custom `Config.Logger`, reusing `New`'s
      other config) rather than adding a public option nobody else needs
- [x] run `cd server && go test ./internal/uploadbackend/...` — must pass
      before task 2

### Task 2: Verify acceptance criteria

- [x] verify all three call sites (`CreateUpload`, `ForwardPatch`,
      `TerminateOrCleanup`) use `newTusdRecorder()` — `grep -n
      "httptest.NewRecorder" server/internal/uploadbackend/tushandler.go`
      returns no matches outside the new constructor itself
- [x] verify no other file in the repo constructs a tusd-facing recorder
      directly — `grep -rn "handler.PostFile\|handler.PatchFile\|handler.DelFile"
      server/` to confirm `tushandler.go` is the only caller
- [x] run full test suite: `cd server && go test ./...`
- [x] run `cd server && go vet ./...`

### Task 3: [Final] Move plan

No doc changes needed — internal adapter, no public API or behavior change.

- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

**Manual verification** (from the ticket's own repro steps):
- Run the server (`cd server && go run .`, `PORT=8080`) and perform a real
  upload via the macOS client (Sync Now). Confirm `NetworkTimeoutError` no
  longer appears in server logs during `PATCH /uploads/<id>/data`, while
  `ChunkWriteComplete` → `UploadFinished` → `204` still occur and the file
  still lands in `organized/`.
