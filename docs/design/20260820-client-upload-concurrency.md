# Concurrent uploads on the client — Apple clients #NN

Status: approved design — ready for implementation plan.
Date: 2026-08-20.

## 1. Goal

Upload multiple assets **in parallel** instead of one at a time, bounded by a
server-advertised concurrency cap. Today `SyncCoordinator.sync` uploads assets in
a strictly serial loop (`for item in plan.uploads { try await execute(item) }`),
so throughput is limited to one in-flight upload regardless of available bandwidth.

This is the **client (FE) half** of a two-part feature. The **server (BE) half** is
`moontechs/files-nest` issue #24 / PR #25, which adds a `ConcurrencyLimiter` gating
`PATCH /uploads/{id}/data`, a `MAX_CONCURRENT_UPLOADS` env var (default 4), and a
`GET /config` endpoint advertising the cap. This spec makes the client discover that
cap, run uploads concurrently up to it, and handle the server's over-limit rejection.

Scope is the **whole client**: `FilesNestCore` (parallel loop, config discovery,
503 handling) plus a minimal `PanelView` change so the concurrency is visible.

Non-goals (explicit): a user-facing Settings control for concurrency (the injection
seam is built, the control is a later slice); a richer multi-thumbnail sync strip
(only an in-flight *count* is surfaced now); parallelizing the **delete** queue
(deletes stay serial — they are cheap, ungated, and keep the delete-after-upload
ordering invariant).

## 2. Server contract (from PR #25, for reference)

- `GET /config` — authenticated. `200`, `Content-Type: application/json`, body
  `{"maxConcurrentUploads": <int>}`. Sourced from the limiter's capacity, so it is
  the single source of truth. Default cap is **4**.
- Over-limit `PATCH /uploads/{id}/data` — `503 Service Unavailable`, headers
  `Retry-After: 1` and `Content-Type: application/json`, body
  `{"error":"too many concurrent uploads"}`. **Only the PATCH-data route is gated**;
  `POST /uploads` (create), `PATCH .../status`, `GET /uploads` (list),
  `DELETE`, and `HEAD .../data` are not.
- No server-side queuing (ADR-0003): the server rejects immediately and pushes
  retry/backoff to the client. A 503 leaves the upload's offset **unchanged**, so
  re-issuing the identical PATCH is safe/idempotent at the TUS layer.

At the time of writing PR #25 is **open, not merged**. The client falls back to a
default cap and treats a missing `/config` as "old server" (see §4), so it works
against both the current `main` server and the post-#25 server.

## 3. Concurrency mechanism — bounded sliding-window task group

Replace the serial upload loop in `SyncCoordinator.sync` with a
`withThrowingTaskGroup` sliding window that keeps exactly `cap` uploads in flight:

- **Prime** up to `cap` tasks from an iterator over `plan.uploads`.
- **Drain-one/add-one**: `for try await outcome in group { ... add next if any ... }`.
- **Per-item failures do not throw out of a task.** Each task runs `execute(item)`
  and catches its own upload error into an outcome value:

  ```
  enum UploadOutcome { case success(ResourceKey); case failed(FailedItem) }
  ```

  A task that threw a per-item error would cancel its siblings — wrong. Only
  `CancellationError` is rethrown, so a pause tears the whole group down (§6).
- **Single-threaded aggregation & progress.** The group-draining coroutine (one
  coroutine) owns `uploaded`/`failed` accumulation and every `onProgress` call, so
  progress stays ordered and race-free even though the uploads run concurrently.
  `onProgress` is not called from inside the concurrent child tasks.
- **Deletes** run serially *after* the group completes — unchanged from today, so
  the existing `deleteIdx > lastUploadIdx` ordering guarantee holds.

Per-item recovery (`uploadWithRecovery` / `recover` for `backend_lost`) is unchanged;
it simply runs inside each child task.

Peak memory ≈ `cap × 2` blobs (`AssetUploader` holds ≤2 blobs per in-flight upload —
one held by `LookAhead`, one being read). At the default cap of 4 that is ~8 blobs.

## 4. Config discovery — server-driven cap

New model:

```
public struct ServerConfig: Decodable, Sendable, Equatable {
    public let maxConcurrentUploads: Int
}
```

The Go handler returns camelCase `maxConcurrentUploads`, which maps to the Swift
property directly (no `CodingKeys` needed).

New client method:

```
/// GET /config — server-advertised limits. Authenticated.
public func config() async throws -> ServerConfig
```

Cap resolution inside `SyncCoordinator.sync`:

```
let cap = max(1, configuredConcurrency
                 ?? (try? await client.config().maxConcurrentUploads)
                 ?? Self.defaultConcurrency)   // defaultConcurrency = 4
```

- `SyncCoordinator` gains `configuredConcurrency: Int? = nil` (a new init parameter).
  - **Tests** inject a fixed value → deterministic, no network. `configuredConcurrency: 1`
    reproduces today's serial behaviour so existing single-item ordering tests stay green.
  - **The real app** leaves it `nil` → the cap is discovered from `/config` each sync,
    falling back to `4` when the endpoint is **absent** (old server returns `404`/
    `notFound`) or **unreachable** (`transport`). The `try?` swallows exactly those —
    a config fetch never fails a sync.
- This `configuredConcurrency` parameter **is** the seam the future Settings-UI slice
  plugs a persisted value into; no re-architecture needed then.

`GET /config` is fetched once per `sync()` call (cheap metadata; keeps the cap fresh
if an operator retunes the server). It is not cached across syncs.

## 5. 503 + Retry-After handling

New error case:

```
case serviceUnavailable(retryAfter: Int?)   // 503; retryAfter is delta-seconds
```

`ServerClient.send` detects `503` where it already holds the `HTTPURLResponse` and
throws `serviceUnavailable(retryAfter:)`, parsing the `Retry-After` header as an
integer number of seconds (fallback `1` when absent/unparseable — matches the
server's `Retry-After: 1`). Only the PATCH-data route is gated, so this only arises
from `patchData`.

`patchData` wraps its send in a **bounded retry loop** (default `maxPatchRetries = 5`):

```
for attempt in 0...maxPatchRetries {
    do { return try await sendPatch(...) }
    catch ServerClientError.serviceUnavailable(let retryAfter) where attempt < maxPatchRetries {
        try await Task.sleep(for: .seconds(retryAfter ?? 1))   // cancellable
        continue
    }
}
```

- Safe because a 503 does not advance the offset — the retried PATCH carries the
  same `Upload-Offset` and body.
- `Task.sleep` is cancellable, so a pause during backoff unwinds via
  `CancellationError` (§6).
- After exhausting attempts the error propagates and the item becomes an ordinary
  per-item failure (recorded in `failed`, retried on the next sync). The client also
  self-caps concurrency to the server value (§4), so a single-client sync should
  rarely hit 503 at all — the retry loop covers chunk-boundary races, multi-device
  contention, and the server's "slot held for the life of a slow connection"
  limitation (PR #25 known-limitation note).

The retry lives in `patchData` (the sole gated endpoint), so `AssetUploader` and
`LookAhead` stay unaware of concurrency backpressure.

## 6. Progress mapping and the strip (minimal FE change)

`SyncProgress` gains one field:

```
public let inFlight: Int   // uploads currently in flight; init default 0
```

Added with a default (`inFlight: Int = 0`) so existing construction sites and the
`StubSyncEngine` compile unchanged; `Equatable` synthesis picks it up.

- `completed` = successful outcomes so far (monotonic; never credits a failure) —
  unchanged semantics.
- `currentItemName` / `currentItemID` = the most-recently-started item **that is
  still in flight**. The coordinator keeps the in-flight uploads in start order and
  reports the last one still running, so the strip never shows an upload that has
  already finished while an older one is still going (Codex review #4). When nothing
  is in flight (the trailing "all done" tick), current is `nil`.
- `inFlight` = number of child tasks currently running, emitted alongside.

`PanelView.currentItem(_:)` (`PanelView.swift:95-100`) keeps its single hero
`ThumbnailView(id: p.currentItemID)` and item name, and its subtitle changes from
`"Uploading · \(p.completed) of \(p.total)"` to surface the count, e.g.:

```
Text("Uploading \(p.inFlight) · \(p.completed) of \(p.total)")
```

(When `inFlight <= 1` the label reads naturally as a single upload.) No other
`PanelView` layout changes. `LiveSyncEngine` forwards `SyncProgress` unchanged — it
constructs nothing, so it needs no edit.

## 7. Error & cancellation semantics

- **Per-item upload failure** → recorded in `failed`, siblings continue (same as
  today, now concurrent).
- **Pause / cancel** → the parent task cancels → the task group cancels its children
  → in-flight PATCHes and any `Retry-After` sleeps unwind via `CancellationError`,
  which `SyncCoordinator` rethrows (existing contract; `LiveSyncEngine`'s
  generation-gating drops the superseded run).
- **`backend_lost`** recovery is per-item and unchanged (runs inside a child task).

## 8. Seam / files touched

Core (`FilesNestCore`):
- `Models/ServerConfig.swift` — **new** `ServerConfig`.
- `ServerClient.swift` — `config()`; `503` detection + `Retry-After` parse in `send`;
  bounded retry loop in `patchData`.
- `ServerClientError.swift` — `serviceUnavailable(retryAfter:)` case + mapping.
- `SyncCoordinator.swift` — sliding-window task group, `configuredConcurrency` init
  param + cap resolution, `UploadOutcome`, single-threaded aggregation/progress.
- `SyncStatus.swift` — `SyncProgress.inFlight`.

App target (`apple/macos/FilesNest`):
- `PanelView.swift` — one-line subtitle change to show `inFlight`.

Unchanged: `LiveSyncEngine`, `AssetUploader`, `LookAhead`, `SyncPlanner`,
`FilesNestApp` (its `perform` closure builds `SyncCoordinator` with
`configuredConcurrency` left `nil` → discovery via `/config`).

## 9. Testing

Core (swift-testing, `@testable import FilesNestCore`):
- `SyncCoordinatorTests`
  - Bounded window: a fake source/client that records max concurrent in-flight
    uploads never exceeds `cap`.
  - `configuredConcurrency: 1` reproduces deterministic serial order — existing
    single-item / ordering tests stay green.
  - Multi-item results compared as **Sets** (`uploaded`/`failed` order is now
    nondeterministic); the existing multi-failure test already uses `Set(...)`.
  - Cancellation cancels in-flight uploads (no stranded tasks; `CancellationError`
    propagates).
  - Progress reports most-recently-started `currentItemID` and a plausible
    `inFlight`; `completed` stays monotonic and never counts a failure.
  - Cap resolution: `configuredConcurrency` wins; else `/config` value; else default
    4 when `config()` throws `notFound` / `transport`.
- `ServerClientTests`
  - `/config` decodes `{"maxConcurrentUploads": N}`.
  - `503` → `serviceUnavailable(retryAfter:)` with the header parsed (and the
    absent-header fallback).
  - `patchData` retries after `Retry-After` then succeeds (503-then-200 script).
  - `patchData` surfaces the error after exhausting `maxPatchRetries`.
- Support fakes: add a `/config` route and a "503 then 200" response script to
  `FakeServer` / `MockURLProtocol`.
- Note: any test asserting exact `SyncProgress` equality must account for the new
  `inFlight` field (default 0 keeps prior literals valid unless `inFlight` is set).

App (manual): a verification checklist under `docs/plans/` walking a real sync with
cap 4 — the strip shows the in-flight count and a live hero thumbnail; pause during a
concurrent burst cancels cleanly; run against a pre-#25 server (no `/config`) to
confirm the fallback path.

## 9a. Review decisions (Codex, PR #27)

- **Cap has no upper bound (deliberate).** The resolved cap is `max(1, …)` only —
  not clamped to a ceiling. The server is the user's own self-hosted instance and
  its `MAX_CONCURRENT_UPLOADS` is intentional configuration; the client trusts it.
  A misconfigured very-large value would let the client run that many concurrent
  uploads (memory), which is accepted as the operator's own footgun.
- **Priming loop must `break`** on plan exhaustion (a `for … where` only *filters*,
  so a large cap iterated the whole range) — fixed, guarded by
  `hugeCapWithFewUploadsDoesNotHang`.
- **Config fetch preserves cancellation** — a `CancellationError` from `GET /config`
  is rethrown, never swallowed into the default-cap fallback.

## 10. Rollout

Client-only change; safe against both server versions. Ships as one client PR
(Core + minimal UI) titled `Apple clients: Concurrent uploads (#NN)`, following the
Core-tested / UI-manual-verify split within the single PR.
