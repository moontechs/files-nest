# Idempotent final-chunk PATCH + honest tusd 4xx classification

## Overview

Fixes [issue #45](https://github.com/moontechs/files-nest/issues/45): uploads
of the final chunk can fail with a masked `500 failed to write upload data`
when the real cause is tusd rejecting a resent `Upload-Length` header (`400
ERR_INVALID_UPLOAD_LENGTH`).

**Root cause** (confirmed 5-Why in the issue, verified against vendored
`tusd/v2@v2.10.0` source): tusd's declare-then-write sequence for the final
chunk is non-atomic. `unrouted_handler.go` persists `info.SizeIsDeferred =
false` **before** calling `writeChunk`. If `writeChunk` then fails with 0
bytes written (e.g. the request body errors mid-read from a dropped
connection), the length declaration is permanently persisted but `Offset`
never advances. The client's transport-error retry logic
(`ServerClient.sendPatch` in `apple/FilesNestCore`) then resends an
identical `Upload-Length` header on retry — tusd unconditionally rejects
*any* `Upload-Length` header once `SizeIsDeferred` is already `false`, even
a byte-for-byte match. This surfaces to the client as `unexpectedStatus(500,
"failed to write upload data")`, masking the real 400.

Also confirmed: tusd's own early-completion short-circuit
(`unrouted_handler.go`, `PatchFile`) returns success *before even reading*
the `Upload-Length` header whenever `!info.SizeIsDeferred && info.Offset ==
info.Size` (i.e. the upload is already fully complete). The bug only
manifests in the narrower state `SizeIsDeferred == false && Offset < Size` —
declared but not fully written. Any fix/test must target that exact state,
not "upload already fully complete."

**Fix** (server-only, no client changes): make `ForwardPatch` idempotent by
having it check tusd's own current state before deciding whether to forward
`Upload-Length`, and stop masking tusd's genuine 4xx responses as 500s so
any remaining edge case surfaces the real status instead.

## Context (from discovery)

- `server/internal/api/handlers.go` `forwardUploadData` (~line 1085):
  fetches the backend's current offset via `h.backend.GetOffset` before
  forwarding the PATCH; on `ForwardPatch` error, only recognizes
  `uploadbackend.ErrNotFound` and `uploadbackend.ErrInvalidOffset` before
  falling into a generic `500`. **This function is not modified by this
  plan** — see Solution Overview for why.
- `server/internal/uploadbackend/tushandler.go`:
  - `ForwardPatch` (~line 202) sets the `Upload-Length` header on the
    outgoing tusd request whenever the caller passes a non-empty
    `uploadLength`, with no awareness of whether tusd already considers the
    size declared.
  - `extractTusdError` (~line 434) has a `case` per known tusd status
    (404/409/423/501/412) mapping to a dedicated sentinel error; anything
    else falls into `errTusdGeneric`, which `forwardUploadData` always turns
    into a masked `500`.
- Existing test patterns to follow:
  - `server/internal/uploadbackend/tushandler_internal_test.go` —
    white-box tests in the same package, real tusd handler + filestore (no
    mocks), e.g. `TestExtractTusdError`, `TestForwardPatchNoNetworkTimeoutErrorWarning`.
  - `server/internal/api/handlers_test.go` — full-`Handler`-level tests via
    `tusPatchRequest`/`tusHeadRequest` against a real backend, e.g.
    `TestHandlePatchUploadData_DeferredLengthFinalization`,
    `TestHandlePatchUploadData_MultiChunkWithFinalization`.
  - `server/e2e/resume_test.go` — black-box e2e tests over real HTTP via
    `PatchUploadData`/`HeadUploadData` (`server/e2e/client_test.go`), focused
    on offset/PATCH-retry behavior — the natural home for the new e2e test.
- Logging convention: plain `log.Printf("ERROR ...")` / `log.Printf("WARN
  ...")` string-prefixed lines (stdlib `log`, not a structured logger) —
  see `server/internal/filestore/mover.go` and `server/internal/api/recovery.go`
  for existing `"WARN ..."` usage.
- **Client retry-safety check (verified, not assumed)**: read
  `apple/FilesNestCore/Sources/FilesNestCore/ServerClientError.swift` —
  `isRetryable` returns `true` only for `.transport` and
  `.serviceUnavailable`. A masked `unexpectedStatus(500, ...)` (today) and an
  honestly-classified `badRequest(...)`/`unexpectedStatus(4xx, ...)` (after
  this fix) are **both already non-retryable** on the client. Reclassifying
  a masked 500 as an honest 4xx does not change client retry behavior for
  any case this plan touches — confirms Change 2 is safe defense-in-depth,
  not a regression for the client.

## Development Approach

- **Testing approach**: Regular (implement each change, then its tests, in
  the same task).
- Complete each task fully — including its tests passing — before moving to
  the next.
- **CRITICAL: every task with code changes MUST include new/updated tests**
  in that same task.
- **CRITICAL: all tests must pass before starting the next task** — no
  exceptions. (Note: this plan was revised specifically to guarantee this —
  see "Why no signature change" below. The original draft threaded a new
  parameter through `ForwardPatch`'s public signature, which broke
  compilation of ~21 existing call sites in `tushandler_test.go` and
  `recovery_test.go` outside the task that changed it. Fixed by having
  `ForwardPatch` look up what it needs internally instead.)
- Do not touch the five existing `extractTusdError` sentinel cases
  (404/409/423/501/412) or their status-code mappings — they are correct
  as-is.
- Do not touch anything client-side (`apple/`) or the companion "Show
  readable error text" UI ticket — both explicitly out of scope.
- Do not touch the vendored tusd handler itself — the fix works entirely
  from FilesNest's own adapter/handler code.

## Testing Strategy

- **Unit tests** (`server/internal/uploadbackend`, white-box, real tusd
  handler): `extractTusdError` 400 classification; `ForwardPatch` idempotency
  and mismatch-detection, including a same-package reproduction of the real
  bug using a `Read`-erroring `io.Reader` (no HTTP layer needed — `ForwardPatch`
  takes an `io.Reader` directly).
- **Handler-level tests** (`server/internal/api`, full `Handler` via
  `tusPatchRequest`): a genuine `SizeIsDeferred=false && Offset<Size` state
  reproduced through the full `Handler` (not "already fully complete" — see
  Root Cause note above, that state doesn't reach the buggy code path at
  all); `ClientError` passed through as its real status/body instead of a
  masked 500.
- **e2e test** (`server/e2e/resume_test.go`, real HTTP against the running
  server): reproduces the production failure end-to-end via a
  fault-injecting request body that fails on its very first `Read` call
  (guaranteeing zero bytes persisted — see Task 3), then proves the retried
  final-chunk PATCH now succeeds.

## Progress Tracking

- Mark completed items with `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix.
- Document issues/blockers with ⚠️ prefix.

## Solution Overview

Two changes, both self-contained inside
`server/internal/uploadbackend/tushandler.go` (plus their tests) — no
changes to `server/internal/api/handlers.go` are needed at all:

1. **Idempotent, mismatch-checked `ForwardPatch`**: when a caller passes a
   non-empty `uploadLength`, `ForwardPatch` calls `h.GetInfo` on itself
   *first* to check the upload's current `SizeIsDeferred`/`Size` — no
   caller-supplied parameter, no signature change. If the size is already
   declared:
   - and the resent value **matches** `info.Size` → drop the header
     (idempotent no-op) and forward the PATCH normally. This is the actual
     bug fix.
   - and the resent value **does not match** `info.Size` → return a
     `*ClientError` (409) *without* forwarding to tusd at all. This closes a
     gap in the naive "always drop the header" approach: tusd today rejects
     *any* re-declaration unconditionally (never checks equality), so
     silently dropping the header for a *mismatched* resend would hide a
     real client bug (wrong length) instead of catching it. Validating
     equality here preserves that error-detection property while fixing the
     idempotency case.
   - If the size is **not yet** declared, behavior is unchanged — header is
     set and forwarded as today.
2. **Honest 4xx classification**: `extractTusdError` gains a
   `*ClientError{Status, Body}` catch-all for any 4xx tusd status not
   already covered by an existing sentinel (reusing the same type Change 1
   introduces for the mismatch case). This is orthogonal defense-in-depth:
   it ensures any *remaining* edge case surfaces correctly instead of lying
   about the fault being server-side. Confirmed safe for the client (see
   Context's retry-safety note).

**Why no `forwardUploadData` change, and no signature change**: the extra
`GetInfo` lookup inside `ForwardPatch` only fires on the one PATCH per
upload that actually carries `Upload-Length` (the final chunk) — a local
BadgerDB/tusd-info read, not a network round trip. Accepting that one
redundant lookup (`forwardUploadData`'s pre-existing `GetOffset` call
already does an equivalent lookup for its own unrelated offset-mismatch
check) is a smaller, safer diff than threading a new parameter through a
public method signature and every one of its ~21 existing call sites across
two test files. `forwardUploadData` needs no changes: its existing
`ErrNotFound`/`ErrInvalidOffset`/generic-fallback error handling already
gets a `ClientError`-passthrough branch (Task 1), and that's the only piece
it needed.

**Note on the read-then-decide window**: `ForwardPatch`'s `GetInfo` read and
its subsequent `PatchFile` call are not atomic with each other, but tusd's
own `memorylocker` still serializes the actual writes per upload ID. A race
here degrades, at worst, to the same non-atomic-declare symptom this plan
already fixes (a stale read followed by a rejected/mismatched forward) — it
does not introduce a new failure mode.

## Technical Details

### `ForwardPatch` — internal idempotency + mismatch check (no signature change)

```go
// ForwardPatch streams data from body to the tusd upload at the given offset.
// If uploadLength is non-empty, it declares the final upload length (used
// for deferred-length uploads to finalize the size) — unless tusd has
// already recorded the size as declared, in which case a byte-identical
// resend is treated as a no-op retry (dropping the header) and a mismatched
// resend is rejected as a ClientError, without either case reaching tusd.
func (h *TUSHandler) ForwardPatch(
    ctx context.Context, backendID string, body io.Reader, offset int64, uploadLength string,
) (int64, error) {
    if uploadLength != "" {
        info, err := h.GetInfo(ctx, backendID)
        if err != nil {
            return 0, err
        }
        if !info.SizeIsDeferred {
            declared, parseErr := strconv.ParseInt(uploadLength, 10, 64)
            if parseErr != nil {
                return 0, fmt.Errorf("%w: invalid Upload-Length %q", errTusdGeneric, uploadLength)
            }
            if declared != info.Size {
                return 0, &ClientError{
                    Status: http.StatusConflict,
                    Body:   fmt.Sprintf("Upload-Length mismatch: declared %d, resent %d", info.Size, declared),
                }
            }
            uploadLength = "" // matches already-declared size: safe no-op, drop the header
        }
    }

    rec := newTusdRecorder()
    req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "/"+backendID, body)
    req.Header.Set("Tus-Resumable", "1.0.0")
    req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
    req.Header.Set("Content-Type", "application/offset+octet-stream")

    if uploadLength != "" {
        req.Header.Set("Upload-Length", uploadLength)
    }
    // ... unchanged from here (h.handler.PatchFile, response handling)
}
```

Signature, callers, and everything below the header-decision block are
**unchanged** — this is the whole point of the design.

### New `ClientError` type

```go
// ClientError represents a client-caused failure that should reach the
// caller as its real status/body instead of being masked as a 500 — either
// a tusd 4xx response not covered by a dedicated sentinel, or a condition
// ForwardPatch detects itself (e.g. a mismatched resent Upload-Length).
type ClientError struct {
    Status int
    Body   string
}

func (e *ClientError) Error() string {
    return fmt.Sprintf("tusd client error %d: %s", e.Status, e.Body)
}
```

Add in `extractTusdError`, after the existing `switch` (whose five cases
already `return` early, so this only reaches unhandled statuses):

```go
if rec.Code >= http.StatusBadRequest && rec.Code < http.StatusInternalServerError {
    return &ClientError{Status: rec.Code, Body: body}
}
```

Everything else (5xx, or anything outside that range) keeps today's exact
behavior — falls to `errTusdGeneric`/`errTusdHTTP`.

### `forwardUploadData` — one new branch, nothing else changes

Add a branch after the existing `ErrNotFound`/`ErrInvalidOffset` checks
(~line 1114-1132) and before the generic fallback:

```go
var ce *uploadbackend.ClientError
if errors.As(err, &ce) {
    log.Printf("WARN ForwardPatch client error for backend %s: %v", upload.BackendID, err)
    writeError(w, ce.Status, ce.Body)
    return
}
```

The existing generic fallback (`log.Printf("ERROR ForwardPatch failed...")`
+ `writeError(w, 500, ...)`) stays as the last resort, unchanged. No other
part of `forwardUploadData` changes — it keeps calling `GetOffset` exactly
as today.

### Note on `ClientError.Body` content

`Body` is tusd's own response text, passed through verbatim to the client's
JSON error body. Checked: tusd's 4xx bodies are short static error codes
(e.g. `"ERR_INVALID_UPLOAD_LENGTH: ..."`), not user data or file paths — no
sanitization needed.

## What Goes Where

- **Implementation Steps** (checkboxes below): all code and test changes —
  fully achievable within this repo.
- **Post-Completion**: none required — this is a pure server bug fix with no
  external/manual verification step beyond normal CI.

## Implementation Steps

### Task 1: Idempotent, mismatch-checked `ForwardPatch` + `ClientError` type + `forwardUploadData` passthrough branch

**Files:**
- Modify: `server/internal/uploadbackend/tushandler.go`
- Modify: `server/internal/uploadbackend/tushandler_internal_test.go`
- Modify: `server/internal/api/handlers.go`

This task is one atomic unit (not split further) because the `ClientError`
type, `ForwardPatch`'s use of it, and `forwardUploadData`'s handling of it
are mutually dependent — splitting them would leave an intermediate task
where a real error case (the mismatch path) silently falls through to a
masked 500, which is exactly the bug being fixed.

- [x] add the exported `ClientError{Status int; Body string}` type with an
      `Error() string` method (near the existing sentinel vars, ~line 28-39)
- [x] change `ForwardPatch` (~line 202) as shown in Technical Details: when
      `uploadLength != ""`, call `h.GetInfo` first; if `!info.SizeIsDeferred`,
      compare the resent value to `info.Size` — match drops the header
      (idempotent no-op), mismatch returns `*ClientError{Status: 409, ...}`
      without calling tusd. Signature is unchanged.
- [x] in `forwardUploadData` (~line 1114-1132), add the `ClientError`
      passthrough branch shown in Technical Details, after the existing
      `ErrNotFound`/`ErrInvalidOffset` checks and before the generic
      fallback: `"WARN ..."` log + `writeError(w, ce.Status, ce.Body)`
- [x] write a `ForwardPatch`-level test (uploadbackend package) reproducing
      the real bug: create an upload, `ForwardPatch` a chunk using a
      `Read`-erroring reader (fails on its first `Read` call — zero bytes
      persisted, matching Task 3's design) with a non-empty `uploadLength` —
      assert the call errors and, via `GetInfo`, that `SizeIsDeferred` is
      now `false` while `Offset` is still at the pre-chunk value. Retry:
      `ForwardPatch` the same bytes with a normal reader, same
      `uploadLength` — assert it now succeeds (204-equivalent: no error,
      offset advances to the declared length)
- [x] write a `ForwardPatch`-level test for the mismatch path: same
      declared-but-not-written setup, but retry with a *different*
      `uploadLength` value than originally declared — assert a
      `*ClientError{Status: 409, ...}` is returned and `GetInfo` shows the
      offset did **not** advance (mismatch is rejected before reaching tusd)
- [x] write a regression test: a fresh upload's *first* declare of
      `Upload-Length` (size not yet deferred-false) still sets the header
      and succeeds normally — confirms the not-yet-declared path is
      untouched
- [x] write a handler-level test (`server/internal/api/handlers_test.go`,
      pattern: `TestHandlePatchUploadData_MultiChunkWithFinalization`)
      reproducing `SizeIsDeferred=false && Offset<Size` through the full
      `Handler` (NOT "PATCH again after the upload is already fully
      complete" — tusd's own early-completion short-circuit returns success
      before even checking `Upload-Length` in that state, so it wouldn't
      exercise this fix at all; use the same fault-injecting-body technique
      as the `ForwardPatch`-level test, one layer up through
      `h.HandlePatchUploadData`): retry with a matching `Upload-Length` →
      `204`; retry with a mismatched `Upload-Length` → the real `409` status
      reaches the response, not a masked `500`
- [x] run `make test` (from `server/`) — must pass before task 2

### Task 2: Honest 4xx classification in `extractTusdError`

**Files:**
- Modify: `server/internal/uploadbackend/tushandler.go`
- Modify: `server/internal/uploadbackend/tushandler_internal_test.go`

- [x] in `extractTusdError` (~line 434), after the existing `switch` (whose
      five cases already return), add: any `rec.Code` in `[400, 499]`
      returns `&ClientError{Status: rec.Code, Body: body}`; leave everything
      else (>=500 or otherwise) falling through to the existing
      `errTusdGeneric`/`errTusdHTTP` behavior, unchanged
- [x] write tests in `TestExtractTusdError` (or alongside it) covering: a
      400 with a body returns `*ClientError{Status:400, Body:<body>}`; a 400
      with no body still returns `*ClientError{Status:400, Body:""}`; the
      five existing sentinel cases (404/409/423/501/412) are unaffected
      (regression coverage); a 500 still returns the generic
      `errTusdGeneric`/`errTusdHTTP` behavior (regression coverage — this
      must NOT change)
- [x] run `make test` (blocked - Go toolchain unavailable in the environment)

### Task 3: e2e test reproducing the full production failure over real HTTP

**Files:**
- Modify: `server/e2e/resume_test.go`

- [ ] add an unexported `faultInjectingReader` type in this file: fails on
      its **very first** `Read` call (returns `(0, syntheticErr)` — not "N
      bytes then error"), so **zero bytes** are ever handed to the server,
      guaranteeing `Offset` cannot have advanced regardless of internal
      buffering/copy granularity. (This is a deliberate correction from an
      earlier draft that risked a flaky/wrong assertion by allowing partial
      bytes through before erroring.)
- [ ] new test `TestResume_FinalChunkRetryAfterPartialWriteFailure`:
  1. create an upload (existing `CreateTestUpload` helper pattern in this
     file), PATCH one non-final chunk normally (no `Upload-Length`)
  2. PATCH the "final" chunk via `PatchUploadData` using
     `faultInjectingReader` (fails immediately), with `Upload-Length` set to
     the true final size — assert `PatchUploadData` returns a non-nil
     (transport-level) error
  3. `HeadUploadData` — assert `SizeIsDeferred` is now `false` while
     `UploadOffset` is still at the pre-final-chunk value (proves the
     non-atomic state was reproduced through the real HTTP stack)
  4. retry: `PatchUploadData` with a normal `bytes.Reader` of the same
     final-chunk bytes, same offset, same `Upload-Length` string — assert
     `204` and `UploadOffset` equals the final total size
- [ ] run `make e2e` (from `server/`) — must pass before task 4

### Task 4: Verify acceptance criteria

- [ ] verify all five existing `extractTusdError` sentinel cases
      (404/409/423/501/412) are untouched and still pass
- [ ] verify a genuine 5xx from tusd still masks as `500` (unchanged
      behavior, not a regression)
- [ ] verify `ForwardPatch`'s signature and all ~21 existing call sites in
      `tushandler_test.go`/`recovery_test.go` are untouched (confirms the
      "no signature change" design held)
- [ ] verify no `apple/` files were touched
- [ ] run full test suite: `make test` (from `server/`)
- [ ] run e2e tests: `make e2e` (from `server/`)
- [ ] run `make lint` (from `server/`) — zero-tolerance, must be clean

### Task 5: [Final] Update documentation

- [ ] check whether `server/CLAUDE.md` needs a mention of the
      `ClientError` pattern for future tusd-error classification work (only
      add if it represents a reusable convention worth documenting — likely
      not needed, this is a narrow fix)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

None — this is a self-contained server bug fix verified entirely by the
project's own unit, handler-level, and e2e test suites.
