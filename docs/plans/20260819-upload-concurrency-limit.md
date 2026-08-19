# Upload Concurrency Limit

## Overview

Add server-side support for concurrent uploads to the FilesNest Go backup server: a bounded concurrency limit on active `PATCH /uploads/{id}/data` requests, an env var to configure it, and a `GET /config` endpoint so clients can discover the limit. Scope is server-only (`server/` module) — macOS client parallelization is a separate ticket. Breaking changes are allowed; nothing consumes this backend yet.

Design decisions were settled via an interview process before planning; see `CONTEXT.md` (term: "Concurrent Upload") and `docs/adr/0003-reject-over-limit-uploads-instead-of-queuing.md` / `docs/adr/0004-synchronous-finalize-move.md` at the repo root for full rationale.

## Context (from discovery)

- Server: plain `net/http` + Go 1.22 `http.ServeMux`, module root `server/`.
- Router: `server/internal/api/router.go` — `NewRouter(handler *Handler, authCfg AuthConfig) http.Handler`, composes routes with an `auth` middleware wrapper; `recoveryMiddleware`/`requestLogMiddleware` wrap the whole mux. `/health` is the one unauthenticated, inline-closure route (pattern to follow for `/config`).
- Handlers: `server/internal/api/handlers.go` — `Handler` struct holds `store`, `backend`, `locks` (`*UploadLocker`, per-upload-ID), `storagePath`, `mover`. `HandlePatchUploadData` at `handlers.go:639` is the route to gate.
- Env var pattern: `server/main.go` has `getEnv(key, fallback string) string` (`main.go:182`) already used for `STORAGE_PATH`, `PORT`, `BACKUP_USER`, `BACKUP_PASS`.
- No existing worker pool / semaphore / rate-limiting code anywhere in `server/`.
- Existing concurrency safety already in place and unaffected by this change: `api.UploadLocker` (per-upload-ID mutex) and `filestore.Mover.moveMu` (global mutex on finalize/move, left unchanged per ADR-0003... (see ADR-0004 for the sync-finalize decision)).
- Tests: `server/internal/api/router_test.go` has one router-construction helper (`newRouterForTest`, line 17-32) used by all router/handler tests — single call site to update. `server/e2e/*_test.go` are black-box tests (build tag `e2e`) run against a live Docker Compose stack (`server/docker-compose.e2e.yml`) via `SERVER_URL`; they don't construct `NewRouter` directly, so no wiring change needed there — the deployed server picks up `MAX_CONCURRENT_UPLOADS` from its own environment (unset in `docker-compose.e2e.yml`, so it exercises the default of 4).

## Development Approach

- **Testing approach**: Regular (code first, then tests, matching the existing codebase's style).
- Complete each task fully, with passing tests, before moving to the next.
- Every task that changes behavior includes new/updated tests as separate checklist items.
- Update this plan file if scope changes during implementation.

## Solution Overview

A `ConcurrencyLimiter` (buffered-channel semaphore) wraps only the `PATCH /uploads/{id}/data` route as HTTP middleware. Requests over the configured cap are rejected immediately with `503` + `Retry-After` (no server-side queuing — ADR-0003). The cap is configured via `MAX_CONCURRENT_UPLOADS` (default 4) and exposed read-only via a new `GET /config` endpoint. No other route, and no non-streaming upload operation (create, status, list, delete), is gated — those are cheap metadata operations. `filestore.Mover.moveMu` and the synchronous finalize step are explicitly left unchanged (ADR-0003, ADR-0004).

## Technical Details

- `ConcurrencyLimiter.slots` is a `chan struct{}` of capacity `max`; acquiring is a non-blocking `select` (`case l.slots <- struct{}{}: ... default: reject`) — this is what makes rejection immediate rather than queued.
- Rejection response: `503 Service Unavailable`, `Retry-After: 1`, `Content-Type: application/json`, body `{"error":"too many concurrent uploads"}`.
- `GET /config` response: `{"maxConcurrentUploads": <int>}`, sourced from `limiter.Cap()` (`cap(l.slots)`) so there's one source of truth instead of threading the raw int separately into the handler.
- `NewRouter` gains a third parameter (`limiter *ConcurrencyLimiter`) — a breaking signature change, intentional.
- Middleware order on the gated route: `auth(limiter.Middleware(handler))` — auth runs first so unauthenticated requests don't consume a concurrency slot.
- **Known limitation, accepted as-is**: a slot is held for as long as `next.ServeHTTP` runs, and `main.go` deliberately has no `ReadTimeout`/`WriteTimeout` on the `http.Server` (see the comment above `ReadHeaderTimeout` in `main.go` — a fixed body timeout would abort legitimate large/slow uploads). This means a slow or stalled client can occupy a slot for the life of its connection, reducing effective capacity below the configured cap. This is a pre-existing tradeoff of the no-body-timeout decision, not something this plan introduces or fixes; not in scope here.
- On rejection, log the event server-side (e.g. `log.Printf("rejected upload: over concurrency limit (cap=%d)", limiter.Cap())`) so an operator can distinguish "cap too low for real traffic" from other error sources without relying on client-reported errors.

## What Goes Where

- **Implementation Steps**: limiter code, router/main wiring, config endpoint, unit tests, e2e tests.
- **Post-Completion**: none — this is a self-contained server change with no external/manual verification needed beyond the test suite (no consumers to coordinate with).

## Implementation Steps

### Task 1: Add `ConcurrencyLimiter`

**Files:**
- Create: `server/internal/api/limiter.go`
- Create: `server/internal/api/limiter_test.go`

- [x] create `ConcurrencyLimiter` struct wrapping `slots chan struct{}`
- [x] implement `NewConcurrencyLimiter(max int) *ConcurrencyLimiter`
- [x] implement `Cap() int` returning `cap(l.slots)`
- [x] implement `Middleware(next http.Handler) http.Handler`: non-blocking `select` acquire; on success `defer release()` then call `next.ServeHTTP`; on failure log the rejection (include the configured cap) then write `503` + `Retry-After: 1` + `Content-Type: application/json` + JSON error body, do not call `next`
- [x] write test: single request through an otherwise-idle limiter succeeds (calls `next`)
- [x] write test: with capacity N, N concurrent requests held open (via goroutines + a channel to control release) all reach `next`; the N+1th concurrent request gets `503` with `Retry-After` header set
- [x] write test: after a held request releases its slot, a subsequent request succeeds
- [x] run tests - must pass before task 2

### Task 2: Wire `MAX_CONCURRENT_UPLOADS` env var and limiter into `main.go` / `router.go`

**Files:**
- Modify: `server/main.go`
- Modify: `server/internal/api/router.go`
- Modify: `server/internal/api/router_test.go`

- [x] in `main.go`, read `MAX_CONCURRENT_UPLOADS` via `getEnv("MAX_CONCURRENT_UPLOADS", "4")`, parse with `strconv.Atoi`, fall back to `4` (log a warning) on parse error *or* on a parsed value `<= 0` — a zero/negative cap would make `NewConcurrencyLimiter`'s channel zero/invalid capacity and reject every upload with no distinguishing signal — construct `limiter := api.NewConcurrencyLimiter(max)`
- [x] change `NewRouter` signature to `func NewRouter(handler *Handler, authCfg AuthConfig, limiter *ConcurrencyLimiter) http.Handler`; pass `limiter` from `main.go`
- [x] wrap only the `PATCH /uploads/{id}/data` route: `auth(limiter.Middleware(http.HandlerFunc(handler.HandlePatchUploadData)))`
- [x] update `newRouterForTest` (`router_test.go:31`) to construct and pass a large-capacity limiter (e.g. `api.NewConcurrencyLimiter(1000)`) so existing router/handler tests aren't incidentally rate-limited
- [x] write test: `PATCH .../data` requests beyond the configured cap return `503` when routed through the real `NewRouter` (not just the limiter in isolation) — confirms wiring, not just the middleware itself
- [x] run tests - must pass before task 3

### Task 3: Add `GET /config` endpoint

**Files:**
- Modify: `server/internal/api/router.go`

- [ ] register `GET /config` in `NewRouter`, authenticated (`auth(...)`, same as all routes except `/health`), as an inline closure matching the existing `/health` closure style
- [ ] handler encodes `{"maxConcurrentUploads": limiter.Cap()}` as JSON with `Content-Type: application/json`
- [ ] write test: `GET /config` (authenticated) returns `200` with the configured `maxConcurrentUploads` value matching the limiter passed to `NewRouter`
- [ ] write test: `GET /config` without credentials returns `401` (consistent with other authenticated routes) when auth is configured
- [ ] run tests - must pass before task 4

### Task 4: Run mutation testing on new/changed code and strengthen unit tests

**Files:**
- Modify: `server/internal/api/limiter_test.go`
- Modify: `server/internal/api/router_test.go`

- [ ] run `cd server && make mutation-test` (gremlins, `./internal/...`) — same tool/target used previously for the codebase (issue #22)
- [ ] review surviving mutants in `limiter.go` (the new file) and any mutated lines touched in `router.go`/`main.go` by this change
- [ ] for each surviving mutant that represents a real behavioral gap (e.g. an off-by-one on the semaphore capacity, an inverted `select`/`default` branch, a swapped status code or missing `Retry-After` header), add or adapt a unit test in `limiter_test.go`/`router_test.go` to kill it
- [ ] re-run `make mutation-test` to confirm the previously-surviving mutants are now killed
- [ ] run full unit test suite - must pass before task 5

### Task 5: Audit existing e2e tests for false-positive `503`s under the new default cap

**Files:**
- Modify: `server/e2e/logspam_test.go` (if audit finds concurrent request bursts)
- Modify: other `server/e2e/*_test.go` files (if audit finds concurrent request bursts)

- [ ] confirm via grep (`t.Parallel()`, goroutine-based PATCH bursts) that no current e2e test fires >4 concurrent `PATCH .../data` requests — expected to be a quick no-op confirmation, since all existing e2e tests issue PATCH calls sequentially today; the real risk this guards against is a *future* test adding concurrency without accounting for the new default cap
- [ ] fix any test found firing >4 concurrent `PATCH .../data` requests unintentionally (either reduce concurrency in the test, or have it assert on `503`/`Retry-After` if that's actually what's being exercised)
- [ ] run existing e2e suite (`go test -tags=e2e ./e2e/` against the e2e Docker Compose stack) - must pass before task 6

### Task 6: Add e2e concurrency limit test

**Files:**
- Create: `server/e2e/concurrency_test.go`

- [ ] write e2e test: 1 concurrent `PATCH .../data` upload succeeds (baseline)
- [ ] give the concurrency e2e tests a way to force real overlap: goroutines launched near-simultaneously do not guarantee the server sees them concurrently over real HTTP (unlike the in-process Task 1 tests, which hold requests open via a channel). Use a slow/streamed request body (e.g. an `io.Reader` that trickles bytes with a small delay, or a large-enough payload with a throttled writer) so each PATCH stays in-flight long enough for the others to arrive and be admitted/rejected before any of them complete. Treat this as a real design step, not a one-line detail — get it right before trusting the pass/fail of the tests below.
- [ ] write e2e test: exactly 4 concurrent uploads (the default cap), fired via real goroutines against 4 distinct freshly-created upload IDs (not sequential calls, and not the same ID — distinct IDs isolate the concurrency limiter from the unrelated per-upload-ID `UploadLocker`), using the overlap mechanism above, all succeed
- [ ] write e2e test: more than 4 concurrent uploads (5-6 at once) fired via real goroutines against distinct upload IDs, using the overlap mechanism above — requests beyond the cap receive `503` with a `Retry-After` header, the rest succeed
- [ ] write e2e test: `GET /config` returns `{"maxConcurrentUploads": 4}` against the e2e stack's default configuration
- [ ] run e2e suite - must pass before task 7

### Task 7: Verify acceptance criteria

- [ ] verify all requirements from Overview are implemented
- [ ] verify edge cases are handled (exactly-at-cap succeeds, one-over-cap rejected, slot release allows subsequent request through)
- [ ] run full test suite: `cd server && go test ./...`
- [ ] run e2e tests: `cd server && go test -tags=e2e ./e2e/` (against the e2e Docker Compose stack per `docker-compose.e2e.yml`)
- [ ] re-run `cd server && make mutation-test` one final time to confirm the fixes from Task 4 still hold (note: Tasks 5-6 only touch `server/e2e/*_test.go`, which is outside `./internal/...`, so this run isn't expected to surface anything new — it's a final confirmation of Task 4's state, not a check on Tasks 5-6)

### Task 8: Update documentation

- [ ] add `MAX_CONCURRENT_UPLOADS` to the env var table in `server/README.md` (~line 118-121, alongside `BACKUP_USER`/`BACKUP_PASS`/`STORAGE_PATH`/`PORT`), and document the `GET /config` endpoint; note that production and e2e Docker Compose files are deliberately left without an explicit `MAX_CONCURRENT_UPLOADS` entry so both pick up the default of 4
- [ ] update `CLAUDE.md` if new patterns discovered worth recording
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

None — self-contained server change, no external consumers or manual verification steps required.
