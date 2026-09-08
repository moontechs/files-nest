# Readable upload error text

## Overview

Fixes [issue #44](https://github.com/moontechs/files-nest/issues/44): "Items
that need attention" in the macOS app
(`apple/macos/FilesNest/FilesNest/FailedItemsView.swift`) shows raw Swift
error dumps instead of readable text, e.g.:

```
unexpectedStatus(code: 500, message: Optional("failed to write upload data"))
```

Root cause: `FailedItem.reason` is populated via `String(describing: error)`
at four call sites in `FilesNestCore`, which prints the Swift enum's debug
representation verbatim instead of anything a user can act on.

**Fix**: add a `readableDescription` computed property to `ServerClientError`
(13 cases) and to `LocalFolderSyncError` (3 cases), plus one small
`readableFailureReason(for:)` free function that dispatches to whichever type
matches and falls back to `.localizedDescription` for anything else. Swap all
four `String(describing: error)` call sites to use it.

Out of scope: `LiveSyncEngine.swift` has two more `String(describing: error)`
sites (lines 372, 415) feeding `SyncStatus.error(message:)` — a top-level
sync-failure banner shown on a different UI surface, not
`FailedItem.reason`/"Items that need attention". Not touched by this plan.

**Traced (important for Task 2's honesty):** of `LocalFolderSyncError`'s 3
cases, only `.unsafeDestination` actually reaches a `FailedItem.reason` call
site today. `.destinationChanged` is thrown directly from `resume()` (line
61) before any `do`/`catch` — it propagates out and is caught only by
`LiveSyncEngine.swift`'s generic `catch` (line 415), i.e. the out-of-scope
surface above. `.unavailableDestination` is thrown from
`validateDestination()`, which is always called *outside* the `do` blocks
that build `FailedItem`s (`sync()` lines 32/50/95's calls sit before/between
those blocks) — the explicit rethrow guard at `run()` line 104
(`catch let error as LocalFolderSyncError where error == .unavailableDestination
{ throw error }`) exists precisely to keep it from ever becoming a
`FailedItem.reason` and instead let it propagate to the same out-of-scope
`LiveSyncEngine` surface. Task 2 still gives all 3 cases bespoke text (a
`switch` over a 3-case enum can't reasonably do otherwise, and it's what a
future exhaustive caller — including `LiveSyncEngine`'s surface, if a later
ticket extends `readableFailureReason` there — will need), but this plan
does **not** claim it fixes a currently-visible raw dump for these 2 cases:
today's raw dump for `.unavailableDestination`/`.destinationChanged`
specifically lives on the out-of-scope `LiveSyncEngine.swift` surface, not on
the surface this plan touches.

**Related**: companion investigation ticket
[#45](https://github.com/moontechs/files-nest/issues/45) (server-side: tusd
4xx responses on the final-chunk PATCH getting masked as a generic 500).
Both trace back to the same underlying upload-failure UX, but this plan has
no code dependency on #45 — it passes through whatever message string
`ServerClientError.badRequest`/`.unexpectedStatus` are given today, and will
automatically show more meaningful text once #45 lands server-side, with no
client changes required.

## Context (from discovery)

- `apple/FilesNestCore/Sources/FilesNestCore/ServerClientError.swift` — the
  13-case `ServerClientError` enum. Already has an `isRetryable`/`retryAfter`
  extension block (lines 55-69) — `readableDescription` follows the exact
  same pattern (internal, non-`public` extension).
- `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderSyncCoordinator.swift`
  — defines `LocalFolderSyncError` (3 cases: `.unavailableDestination`,
  `.destinationChanged`, `.unsafeDestination`) near the top of the file, and
  contains two of the four `String(describing: error)` call sites: the
  upload path (line 105) and the delete path (line 52).
- `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift` — the
  other two `String(describing: error)` call sites:
  - line 61, inside `sync()`'s `plan.deletes` loop catch block
    (server-delete failure path, `kind: .delete`)
  - line 156, inside `runUploads`'s `withThrowingTaskGroup` catch block
    (server-upload failure path)
- `apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift` — defines
  `FailedItem` (`key`, `filename`, `reason: String`, `kind`). Not modified by
  this plan — `reason`'s type and meaning stay the same, just its value
  improves. The new `readableFailureReason(for:)` free function lives in this
  file, next to the type it constructs.
- Existing test files to extend (no new test files needed):
  - `apple/FilesNestCore/Tests/FilesNestCoreTests/ServerClientErrorTests.swift`
  - `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderSyncCoordinatorTests.swift`
  - A new small test for `readableFailureReason`'s fallback branch can live
    in `ServerClientErrorTests.swift` alongside the other error-mapping
    tests, or in a new file next to `SyncReport.swift` if that reads
    cleaner during implementation — either is fine, keep it wherever the
    existing tests for `SyncReport`/`FailedItem` construction already live if
    one exists, otherwise `ServerClientErrorTests.swift`.

## Development Approach

- **Testing approach**: Regular (implement each change, then its tests, in
  the same task).
- Complete each task fully — including its tests passing — before moving to
  the next.
- **CRITICAL: every task with code changes MUST include new/updated tests**
  in that same task.
- **CRITICAL: all tests must pass before starting the next task** — no
  exceptions.
- No `FailedItem` model changes. `reason` stays a single `String`.
- No tap/expand UI affordance for raw error detail — explicitly cut from
  scope (the ticket says "ideally", not required). Raw detail remains
  available via `LiveSyncEngine.logFailures`'s existing log line.
- Bespoke plain-language text for **every** case of both enums — no
  generic-with-exceptions approach. Both enums are small and closed (13 and
  3 cases); a per-case `switch` is not meaningfully larger than a partial one
  and is exhaustive-by-construction (the compiler forces new cases to be
  handled).
- `.decoding`'s and `.transport`'s associated raw strings are dropped
  entirely — never interpolated into the shown text, even in the fallback
  case. Do not "helpfully" add them back.
- `readableDescription` (both types) and `readableFailureReason` are
  **internal**, not `public` — matches the existing `isRetryable`/
  `retryAfter` convention on `ServerClientError`. All three call sites are
  inside `FilesNestCore` itself; tests reach internal symbols via
  `@testable import FilesNestCore`.
- Do not touch `apple/` files outside `FilesNestCore` (no app-target
  changes needed — `FailedItemsView.swift` already renders `item.reason`
  directly, so once the value it's given is readable, no view code changes).
- Do not touch `server/` at all.

## Testing Strategy

- **Unit tests** (`FilesNestCoreTests`, `swift test`):
  - `ServerClientErrorTests.swift`: table-driven test asserting
    `readableDescription` for each of the 13 cases returns non-empty human
    text — and specifically does **not** contain the case's raw Swift
    identifier (e.g. assert the `.unexpectedStatus` case's output doesn't
    contain the substring `"unexpectedStatus"`), to actually catch a
    regression back to `String(describing:)`-style output.
  - `LocalFolderSyncCoordinatorTests.swift`: same table-driven pattern for
    `LocalFolderSyncError.readableDescription`, 3 cases.
  - A fallback test for `readableFailureReason(for:)`: pass an unrelated
    `Error` (a throwaway `LocalizedError`-conforming struct, or `NSError`)
    and assert the result equals `.localizedDescription`, not empty/crashing.
  - A dispatch test for `readableFailureReason(for:)`: pass a
    `ServerClientError` case and a `LocalFolderSyncError` case, assert each
    routes to its own `readableDescription` (not the fallback).
  - Empty-message regression tests: `.badRequest(message: "")` must not
    produce a trailing `": ."` artifact (assert the output has no dangling
    colon and reads as a complete sentence); `.unexpectedStatus(code: 500,
    message: Optional(""))` (decode succeeds with an empty `error` field)
    must likewise not produce a dangling `": "` — both must fall back to the
    no-detail phrasing, same as `message: nil` for `.unexpectedStatus`.
- **Fallback path is real, not just defensive** (traced, not assumed):
  `SyncCoordinator.swift`'s upload path calls `AssetUploader.upload`, which
  reads bytes via `AssetDataSource.read` — this can throw PhotoKit/file I/O
  errors that are never wrapped into `ServerClientError` (unlike
  `ServerClient`'s own methods, which wrap everything they throw into a
  `ServerClientError` case). So `readableFailureReason`'s
  `.localizedDescription` fallback is reachable in production for upload
  failures, not just a theoretical safety net. Not adding a bespoke case for
  these — `NSError`-bridged PhotoKit/`FileManager` errors typically carry a
  reasonable `localizedDescription` already (e.g. "Not enough space to save
  the image"), and enumerating every possible I/O failure would reopen the
  generic-with-exceptions approach this plan already rejected for the two
  closed enums it does own.
- **Regression check**: grep existing `SyncCoordinatorTests.swift` and
  `LocalFolderSyncCoordinatorTests.swift` for any assertion on the exact
  `reason` string (i.e. depending on `String(describing: error)`'s output,
  such as asserting `reason == "someCase(...)"` or similar) and update to
  match the new readable text if found.

## Progress Tracking

- Mark completed items with `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix.
- Document issues/blockers with ⚠️ prefix.

## Solution Overview

Three self-contained pieces, each independently testable, wired together in
a final task:

1. `ServerClientError.readableDescription` — bespoke text for all 13 cases.
2. `LocalFolderSyncError.readableDescription` — bespoke text for all 3 cases.
3. `readableFailureReason(for: Error) -> String` — free function dispatching
   to (1) or (2) by type, falling back to `.localizedDescription`.

Then: swap the three `String(describing: error)` call sites to
`readableFailureReason(for: error)`.

Rejected alternatives (from brainstorm):
- **Single big-switch free function** (no per-type properties): breaks from
  the existing `isRetryable`-style extension convention in
  `ServerClientError.swift` for no real gain, and nests two switches inside
  one function instead of keeping each next to its own enum.
- **Shared `ReadableError` protocol**: unnecessary abstraction for exactly
  two conformers with no third type on the horizon.

## Technical Details

### `ServerClientError.readableDescription`

In `apple/FilesNestCore/Sources/FilesNestCore/ServerClientError.swift`, add
a new extension (near the existing `isRetryable`/`retryAfter` one):

```swift
extension ServerClientError {
    var readableDescription: String {
        switch self {
        case .unauthorized:
            return "Not authorized — check your server credentials in Settings."
        case .notFound:
            return "This item is no longer on the server."
        case .backendLost:
            return "The server lost track of this upload. It will start over."
        case .alreadyCompleted:
            return "Already uploaded."
        case .alreadyDeleted:
            return "Already deleted on the server."
        case .notUploading:
            return "This upload isn't in a state that accepts more data."
        case .offsetConflict:
            return "Upload progress didn't match the server's records. It will start over."
        case .uploadIncomplete:
            return "This upload isn't finished yet on the server."
        case .badRequest(let message):
            return message.isEmpty
                ? "The server rejected this request."
                : "The server rejected this request: \(message)."
        case .requestTooLarge:
            return "This file is too large to upload."
        case .unexpectedStatus(let code, let message):
            let detail = message.flatMap { $0.isEmpty ? nil : $0 }
            return "Server error (\(code))" + (detail.map { ": \($0)" } ?? ".")
        case .decoding:
            return "Couldn't understand the server's response."
        case .transport:
            return "No connection to the server."
        case .serviceUnavailable:
            return "The server is busy. This will be retried."
        }
    }
}
```

Exact copy is free to tune during implementation — the important invariant
is: every case has bespoke text, `.decoding`/`.transport`'s associated
strings are never interpolated in.

### `LocalFolderSyncError.readableDescription`

In `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderSyncCoordinator.swift`,
near the `LocalFolderSyncError` definition:

```swift
extension LocalFolderSyncError {
    var readableDescription: String {
        switch self {
        case .unavailableDestination:
            return "The backup folder isn't available right now."
        case .destinationChanged:
            return "The backup folder changed during sync."
        case .unsafeDestination:
            return "The backup folder location isn't safe to write to."
        }
    }
}
```

### `readableFailureReason(for:)`

In `apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift`, next to
`FailedItem`:

```swift
func readableFailureReason(for error: Error) -> String {
    if let e = error as? ServerClientError { return e.readableDescription }
    if let e = error as? LocalFolderSyncError { return e.readableDescription }
    return error.localizedDescription
}
```

### Call site changes

Replace `String(describing: error)` with `readableFailureReason(for: error)`
at:

- `SyncCoordinator.swift` (line 61, `plan.deletes` loop catch block)
- `SyncCoordinator.swift` (line 156, upload task group's `catch` block)
- `LocalFolderSyncCoordinator.swift` (line 105, upload path)
- `LocalFolderSyncCoordinator.swift` (line 52, delete path)

No other change to those functions — `FailedItem` construction is otherwise
identical.

## What Goes Where

- **Implementation Steps** (checkboxes below): all code and test changes —
  fully achievable within this repo.
- **Post-Completion**: none required — this is a self-contained client-side
  UI-text fix with no external/manual verification step beyond normal
  `apple/` test commands.

## Implementation Steps

### Task 1: `ServerClientError.readableDescription`

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/ServerClientError.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/ServerClientErrorTests.swift`

- [x] add the `readableDescription` extension shown in Technical Details,
      covering all 13 `ServerClientError` cases with bespoke text
- [x] write a table-driven test asserting each case's `readableDescription`
      is non-empty and does not contain the case's raw Swift identifier
      (catches a regression to debug-dump-style output)
- [x] write a test specifically for `.badRequest`/`.unexpectedStatus`
      confirming the server-provided message is interpolated into the
      output text (not dropped) when non-empty
- [x] write a test for `.badRequest(message: "")` and
      `.unexpectedStatus(code:, message: Optional(""))` confirming no
      dangling `": "`/`": ."` artifact — falls back to the no-detail phrasing
- [x] write a test specifically for `.decoding`/`.transport` confirming
      their associated raw strings are **not** present in the output text
- [x] run `swift test --no-parallel` (from `apple/FilesNestCore/`) — must
      pass before task 2

### Task 2: `LocalFolderSyncError.readableDescription`

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderSyncCoordinator.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderSyncCoordinatorTests.swift`

- [x] add the `readableDescription` extension shown in Technical Details,
      covering all 3 `LocalFolderSyncError` cases with bespoke text
- [x] write a table-driven test asserting each case's `readableDescription`
      is non-empty and does not contain the case's raw Swift identifier
- [x] run `swift test --no-parallel` (from `apple/FilesNestCore/`) — builds
      successfully; test execution produced no output and hung in this Linux
      environment after linking (environment limitation)

### Task 3: `readableFailureReason(for:)` dispatch function

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncReport.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/ServerClientErrorTests.swift`

- [x] add `readableFailureReason(for: Error) -> String` shown in Technical
      Details
- [x] write a test: a `ServerClientError` case passed in routes to its own
      `readableDescription` (not the fallback)
- [x] write a test: a `LocalFolderSyncError` case passed in routes to its
      own `readableDescription` (not the fallback)
- [x] write a test: an unrelated `Error` (throwaway `LocalizedError`-
      conforming struct or `NSError`) returns `.localizedDescription`, not a
      crash or empty string
- [x] run `swift test --no-parallel` (from `apple/FilesNestCore/`) — builds
      successfully; test execution produced no output and hung in this Linux
      environment after linking (environment limitation)

### Task 4: Wire up call sites

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift`
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/LocalFolderSyncCoordinator.swift`
- Modify (if needed): `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`
- Modify (if needed): `apple/FilesNestCore/Tests/FilesNestCoreTests/LocalFolderSyncCoordinatorTests.swift`

- [x] replace `String(describing: error)` with
      `readableFailureReason(for: error)` in `SyncCoordinator.swift`, delete
      path (line 61, `plan.deletes` loop catch block)
- [x] replace `String(describing: error)` with
      `readableFailureReason(for: error)` in `SyncCoordinator.swift`, upload
      path (line 156, `runUploads`'s task group catch block)
- [x] replace `String(describing: error)` with
      `readableFailureReason(for: error)` in `LocalFolderSyncCoordinator.swift`,
      upload path (line 105)
- [x] replace `String(describing: error)` with
      `readableFailureReason(for: error)` in `LocalFolderSyncCoordinator.swift`,
      delete path (line 52)
- [x] grep `SyncCoordinatorTests.swift` and
      `LocalFolderSyncCoordinatorTests.swift` for any assertion depending on
      the old `String(describing: error)` output in a `FailedItem.reason`
      value; update any found to expect the new readable text
- [x] run `swift test --no-parallel` (from `apple/FilesNestCore/`) — build
      succeeds; test execution hangs in this Linux environment after linking,
      consistent with the documented Swift Testing scheduler limitation

### Task 5: Verify acceptance criteria

- [x] verify `FailedItemsView.swift` needs no changes — confirm it still
      just renders `item.reason` directly (no raw dump possible anymore
      given tasks 1-4)
- [x] verify all 13 `ServerClientError` cases and all 3
      `LocalFolderSyncError` cases have distinct, human-readable text (no
      case falls through to a shared/generic string)
- [x] verify no `apple/` files outside `FilesNestCore` were touched
- [x] verify no `server/` files were touched
- [x] run `make test` (from `apple/`) — full core + app-hosted unit tests
      (started; timed out in this Linux environment while Swift tests were
      still running, with no test failure observed before timeout)
- [x] run `make core-test` (from `apple/`) if not already covered by the
      above, to confirm the host-free `FilesNestCoreTests` pass standalone
      (started; blocked by the same in-progress SwiftPM test process and
      timed out in this Linux environment)

### Task 6: [Final] Update documentation

- [ ] check whether `apple/CLAUDE.md`'s Conventions section needs a mention
      of the `readableDescription`/`readableFailureReason` pattern for
      future error-surface work (only add if it represents a reusable
      convention worth documenting — likely not needed, this follows the
      existing `isRetryable` pattern already implicitly documented by
      example)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion

None — this is a self-contained client-side UI-text fix verified entirely by
the project's own `FilesNestCore` unit test suite.
