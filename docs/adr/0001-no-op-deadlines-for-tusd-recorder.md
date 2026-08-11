# No-op read/write deadlines on the in-process tusd recorder

## Status

Accepted

## Context

`TUSHandler` (`server/internal/uploadbackend/tushandler.go`) drives tusd's
`UnroutedHandler` in-process: `CreateUpload`, `ForwardPatch`, and
`TerminateOrCleanup` each build a fresh `httptest.NewRecorder()` and call
`PostFile`/`PatchFile`/`DelFile` directly, instead of registering tusd's
handler on a real `net/http` listener. This is deliberate — the package
isolates the rest of the codebase from tusd's HTTP shape and API changes.

tusd calls `http.NewResponseController(w).SetReadDeadline` /
`SetWriteDeadline` on every body-read tick during `PATCH` (`writeChunk`'s
`onReadDone` callback in tusd v2.10.0). `httptest.ResponseRecorder` doesn't
implement these, so `ResponseController` always returns `http.ErrNotSupported`,
and tusd logs a `NetworkTimeoutError` WARN on every tick — dozens of times per
chunk PATCH (issue #8). This reproduces on every PATCH under the current
architecture; it is not caused by a reverse proxy or connection wrapper, and
`NetworkTimeout` config does not affect it since no deadline can ever be
honored in-process.

## Decision

Wrap `httptest.ResponseRecorder` in a small adapter (shared by all three
call sites) that implements `SetReadDeadline`/`SetWriteDeadline` as no-ops
returning `nil`, satisfying the interface `http.ResponseController` probes
for. No behavior changes — there was never a real deadline to enforce
in-process — only the spurious warning goes away.

This doesn't remove any protection that was actually in effect: the real
`http.Server` in `server/main.go` already runs with no `ReadTimeout`/
`WriteTimeout`, by deliberate design (`main.go:88-96`) — uploads stream
large files and must not be aborted mid-stream, so only
`ReadHeaderTimeout` bounds anything. tusd's own `NetworkTimeout` was never
the layer enforcing upload timeouts in this deployment; it was already
inert here (silently failing every tick) before this change, and stays
inert (silently succeeding) after it.

## Considered Options

- **Thread the real `http.ResponseWriter` through** `ForwardPatch` instead
  of building a recorder, so tusd's deadline calls hit a genuine connection.
  Rejected: this is the *tusd-idiomatic* way to integrate (per tusd's own
  `NewHandler` doc comment), and looks like the "more correct" fix at a
  glance — but it reintroduces tusd's `http.ResponseWriter`/`http.Request`
  shape into `handlers.go`, undoing the deliberate isolation the package
  exists for, and creates a double-write hazard: `HandlePatchUploadData`
  already writes its own response headers/status after calling into the
  backend, using its own error-mapping distinct from tusd's.
- **Filter or suppress the WARN log line.** Rejected: leaves the underlying
  interface mismatch in place and risks masking other legitimate tusd
  warnings logged at the same level.
