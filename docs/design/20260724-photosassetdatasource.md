# Design: PhotosAssetDataSource + callback-stream reader

**Date:** 2026-07-24
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (new reader), `apple/macos/FilesNest` (adapter, app wiring)
**Depends on:** `docs/design/20260724-assetuploader.md` (AssetDataSource seam, OffsetSkip, merged as `51c168b`)

---

## 1. Purpose

Conform to `AssetDataSource` against the real Photos library, and answer the measurement question
design `20260724-assetuploader.md` §3.3/§11.1 left open.

The previous slice built an uploader whose memory ceiling is structural. That ceiling holds only
if the *source* honours the capacity-1 contract (`20260724-assetuploader.md` §5.2 clauses 1–4).
`PhotosAssetDataSource` is
where those clauses are actually kept — and it is the one component `swift test` cannot reach.
This design's primary job is to shrink the untestable surface until almost nothing is left in it.

---

## 2. The problem with the obvious placement

The obvious implementation puts the whole adapter in the app target: bridge
`dataReceivedHandler` to the async sink with a semaphore, apply `OffsetSkip`, wire cancellation to
`cancelDataRequest`. All four contract clauses would then live in code no headless test can run.

That is precisely where the previous client failed. `CODE_AUDIT.md` §5.1 (Critical) records that
`FileManagementService.streamChunkData` (`ios-client/.../FileManagementService.swift:463-537`)
discarded the `PHAssetResourceDataRequestID` returned by `requestData` and never called
`cancelDataRequest`, so Photos kept streaming into a no-op handler; the same method hand-rolled
the offset skip as `dropFirst` and accumulated bytes into a long-lived `chunkData` buffer.

Three defects, all in untested glue. The fix is not "write the glue more carefully" — it is to
move the glue somewhere tests can reach.

---

## 3. Approach: extract the reader into core

Core gains a generic reader that owns the entire contract. PhotoKit supplies two closures and
nothing else.

```
Core (pure Foundation · swift test reaches all of it)
  CallbackStreamReader<Token>   NEW — owns contract clauses 1–4
  ResourceKey                   NEW — resource-addressing key, parse/format
  OffsetSkip                    existing, now applied inside the reader
  StreamDiagnostics             NEW — concurrent-deliver counter
  DiskProbe                     moved Tests → Sources, gains free-space mode

App target (macOS 14)
  PhotosAssetDataSource         ~40 lines, no control flow
  MeasurementView               minimal trigger UI for the real run
```

This preserves both standing decisions: core stays pure Foundation (it never imports Photos, and
never names `PHAssetResourceDataRequestID`), and the Photos adapter still lives in the app target.

### 3.1 The seam

```swift
public struct CallbackStreamReader<Token: Sendable>: Sendable {
    public init(
        start:  @escaping @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                                     _ onDone: @escaping @Sendable (Error?) -> Void) -> Token,
        cancel: @escaping @Sendable (Token) -> Void,
        diagnostics: StreamDiagnostics? = nil)

    public func read(from offset: Int64,
                     into sink: @Sendable (Data) async throws -> Void) async throws
}
```

`onData` returns `Bool`. `false` means *stop producing* — this is how a sink error or a task
cancellation reaches PhotoKit's synchronous callback without an exception crossing a C boundary.

`Token` is generic so core never names a PhotoKit type. The app instantiates
`CallbackStreamReader<PHAssetResourceDataRequestID>`.

---

## 4. The handoff mechanism

The consumer must never block a cooperative thread. The producer *must* block, because blocking
its callback is the only backpressure lever PhotoKit offers. The two directions therefore use
different primitives, and that asymmetry is the design:

| Direction | Primitive | Blocks? |
|---|---|---|
| producer → consumer | `CheckedContinuation` | No — the consumer suspends |
| consumer → producer | `DispatchSemaphore` | Yes — blocks PhotoKit's own delivery thread, never the cooperative pool |

State lives in a `final class` behind an `NSLock`. It cannot be an `actor`: `dataReceivedHandler`
is synchronous and cannot `await`. `Synchronization.Mutex` would be cleaner but requires macOS 15,
above the core floor of macOS 13.

**Each `read()` call invokes `start` on a serial `DispatchQueue` it creates for that call**, not on
a queue shared by the reader instance. The distinction matters: a per-*instance* queue deadlocks
under re-entrancy, because a `sink` that calls `read()` again on the same reader dispatches its
nested `start` onto a queue already blocked in the outer `deliver`'s `drained.wait()`. A per-*call*
queue makes concurrent and nested reads independent by construction, so re-entrancy needs no rule. An earlier draft made
"callbacks must arrive on a non-cooperative queue" a *precondition* on `start`. That was wrong
twice over: it was unenforceable — the seam takes closures and has no executor parameter, so
nothing but a comment held it up — and it was unnecessary.

Owning the queue removes the hazard structurally. If a producer calls `onData` synchronously from
inside `start`, `drained.wait()` blocks the reader's *own* queue thread, never a cooperative one;
the consumer task, running independently, collects the blob and signals, and the queue thread
resumes. Inline delivery therefore becomes **supported behaviour rather than undefined behaviour**,
and there is no precondition left for a caller to violate.

This also removes the ordering hazard: `read()` dispatches `start` onto the queue and enters the
consumer loop, so an inline first blob simply waits in `.blobPending` until the loop collects it.

```
deliver(blob) [PhotoKit thread]          next() async [consumer]
──────────────────────────────────       ──────────────────────────────
 lock                                     lock
 if .terminal -> unlock; return false     if .blobPending -> take; unlock; return
 if .consumerWaiting -> take & resume     if .terminal    -> unlock; finish
 else state = .blobPending(blob)          else suspend on continuation
 unlock                                   ...
 drained.wait()   ← BACKPRESSURE          if let b = skip.take(blob) {
 return !terminal                             try await sink(b)
                                          }
                                          drained.signal()   ← ALWAYS
```

Note the `drained.signal()` sits **outside** the `if`. When a blob falls entirely below `offset`,
`OffsetSkip.take` returns `nil` and `sink` is correctly skipped — but the producer is still blocked
in `drained.wait()` and must be released anyway. Resuming a 3 GB video from a 1 GB offset discards
hundreds of blobs on that path, so a signal inside the `if` would hang on the first real resume.

### 4.1 State, and why the continuation is the dangerous part

State is one of:

| State | Meaning |
|---|---|
| `.idle` | no blob pending, no consumer suspended |
| `.blobPending(Data)` | producer deposited; consumer has not yet collected |
| `.consumerWaiting(CheckedContinuation)` | consumer suspended; no blob available |
| `.handoff` | continuation **taken** by `deliver` but not yet resumed — a real window, see rule 3 |
| `.terminal(Error?)` | completed, failed, or cancelled — **one-shot, never left** |

The obvious failure mode is a **leaked blocked thread**: the consumer disappears while the producer
sits in `drained.wait()`, and that thread never returns. But the subtler and more dangerous one is
the **continuation**, which has two opposite failure modes and no safe middle:

1. **Every transition into `.terminal` must atomically take-and-nil any waiting continuation under
   the same lock**, then resume it exactly once outside the lock. Omitting this hangs `read()`
   forever when cancellation arrives while the consumer is suspended with no blob pending —
   `cancelDataRequest(_:)` is *not* documented to guarantee a subsequent completion callback, so
   nothing else would ever wake it.
2. **Once `.terminal`, every later `deliver` and `onDone` is a no-op.** Omitting this resumes an
   already-resumed continuation, which is a checked-continuation **crash**, not a hang.

3. **The drain semaphore is signalled exactly once per blocked `deliver`, never once per exit
   path.** Each `deliver` that blocks takes a monotonically increasing `generation`; the reader
   records `signalledGeneration` under the lock and signals only when it advances. Without this,
   cancellation and the consumer can both signal for the *same* in-flight blob, the semaphore
   accumulates a spare permit, and the *next* `deliver` returns from `wait()` immediately — so
   capacity-1 breaks silently, in the direction that looks like everything working.

Fixing (1) naively is what creates (2). They must be solved together, by making terminal entry a
single guarded transition that owns the continuation.

**The `.handoff` window.** `deliver` cannot resume a continuation while holding the lock, so
between "take the continuation" and "resume it" the lock is released and a terminal transition can
interleave — seeing no waiting continuation and concluding it owns nothing. `.handoff` names that
window explicitly: a terminal transition observing `.handoff` must **not** resume and must **not**
signal, because the handoff already has an owner. Without this state the "terminal entry owns the
waiting continuation under every interleaving" claim is simply false.

**Ownership follows the blob, not the outcome.** A consumer resumed with a blob owns that
generation's drain signal *unconditionally* — including when it wakes to find `.terminal` and
performs no work at all. It signals and then finishes. Stating this as "the consumer observes
`.terminal` and finishes" (as an earlier draft did) leaves the blocked producer with zero signals
from any path: the terminal transition declined because `.handoff` had an owner, and the owner
declined because there was nothing left to do. That is a deadlock assembled entirely out of
individually reasonable rules, which is why the invariant is phrased as *who was handed the blob*
rather than *what happened next*.

Every exit path:

| Exit | Waiting continuation | Drain signal | Other |
|---|---|---|---|
| `sink` throws | — (consumer running) | signal, generation advances | `.terminal`, `cancel(token)`, rethrow |
| Blob fully skipped by `OffsetSkip` | — | signal, generation advances | continue loop, `sink` skipped |
| Cancelled, consumer suspended | **take & resume** | signal **only if** a producer is blocked on an unsignalled generation | `.terminal`, `cancel(token)`, throw `CancellationError` |
| Cancelled, consumer running | — | **must not signal** — the consumer owns this generation | `.terminal`, `cancel(token)`, throw `CancellationError` |
| Cancelled during `.handoff` | — (resume already in flight) | **must not signal** | `.terminal`; consumer observes it after the resume lands |
| `onDone(nil)`, consumer suspended | **take & resume** | no producer blocked → no signal | `.terminal`, return |
| `onDone(nil)`, consumer running | — | consumer owns the signal | `.terminal`; observed at next `next()` |
| `onDone(error)` | **take & resume** if suspended | as above | `.terminal`, rethrow |
| Late `deliver` after `.terminal` | — | — | return `false` at once, never block |
| Late `onDone` after `.terminal` | — | — | ignored |
| Cancelled before `start` returns its token | — | — | §4.1.1 |

Read the drain column as one rule, not ten: **whoever owns the generation signals it, exactly
once.** The rows only spell out who that is in each case.

Cancellation is wrapped in `withTaskCancellationHandler`, whose handler performs the terminal
transition and invokes `cancel(token)` — which is `cancelDataRequest`. Clause 3, the Critical audit
finding, thereby becomes ordinary core control flow that `swift test` drives directly.

#### 4.1.1 Cancellation before the token exists

`cancel(token)` needs a token, but `start` only *returns* one after it has been invoked — and it
may block briefly before returning. Cancellation arriving in that window has nothing to cancel, so
a naive implementation drops it and leaks a live PhotoKit request: precisely the un-cancelled
request the audit rated Critical, reintroduced through a race instead of an omission.

The token is therefore stored under the lock when `start` returns, and the storing step reads
terminal state in the same critical section. If the reader is already `.terminal`, it invokes
`cancel(token)` **immediately upon receiving it** — but, exactly as with continuation resumption
(rule 1), the decision is made under the lock and the call is made **outside** it.

`cancel` is caller-supplied and may synchronously trigger a late `deliver` or `onDone`, both of
which take the same lock. Invoking it while holding that lock would deadlock on a plain `NSLock`.
The rule is uniform across this design: **decide under the lock, call out from outside it.**
Cancellation is thus never dropped, only deferred to the earliest moment it is expressible.

### 4.2 Late callbacks

After `cancelDataRequest`, PhotoKit may still invoke `dataReceivedHandler` *or* `completionHandler`,
and Apple does not document which, or how many times. Both are therefore routed through the same
terminal check: in `.terminal`, `deliver` returns `false` without blocking and `onDone` is ignored.
A late callback can neither deadlock, nor resurrect a finished read, nor double-resume a
continuation that terminal entry already consumed (§4.1).

### 4.3 Memory

While blocked, the producer's stack still references the blob — but it is the *same* object the
consumer holds, so one distinct buffer.

The number depends on which artefact you ask, and the merged code does not agree with itself:

| Source | Says |
|---|---|
| `AssetUploader.swift:5` | "Peak memory is **two** blobs, independent of asset size" |
| `MemoryGateTests.swift:60` | `maxExpectedLiveBlobs = **3**` — transport "may briefly reference a third" |
| Observed test output | `maxAlive=2` at 3 MB, 64 MB, 512 MB **and 3 GB** |

So: **two is the design floor and the observed value; three is unexercised headroom.** The gate
asserts an upper bound of 3 to tolerate `ServerClient.patchData` assigning the blob to
`req.httpBody` (`ServerClient.swift:137`) and URLSession retaining it past the next delivery — but
under `MockURLProtocol` that third reference has never actually materialised.

This adapter changes none of it; it inherits the arrangement. What matters for this slice is only
that the ceiling is **not tightened**: adding a producer thread that holds a reference while
blocked must not push the observed count above 2, and the gate must keep its headroom at 3 rather
than being "corrected" down to match the observation.

`OffsetSkip.take` already returns freshly copied `Data`, so a partially skipped blob does not keep
PhotoKit's buffer alive by aliasing.

---

## 5. Resource identity

An asset is not a byte stream. A Live Photo is one `PHAsset` with one `localIdentifier` but two
resources (JPEG + MOV), which `architecture.md:230` says become **two** upload records. Records are
keyed `uploads/<localIdentifier>` with `"id": "<localIdentifier>"` (`architecture.md:59-63`) —
two records, one key. `AssetDataSource.read(assetID:from:into:)` takes exactly that one string and
so, as written, cannot name which resource to stream.

The collision is worse than an ambiguous client string. The server derives every upload id as
`SafeID(req.LocalIdentifier)` (`handlers.go:204`), a SHA-256 over the identifier
(`ids.go:26`). Two resources of one Live Photo therefore hash to a **byte-identical server id** —
the second `createUpload` collides with the first. Sending a distinct identifier per resource is
what makes the two records addressable at all; it is a correctness requirement, not a naming
preference.

It collides on a **second** index too. `localIndexKey` is `idx/local/` + base64url of the raw
identifier (`store/index.go:152-157`), described in-source as the lookup for "whether an upload
already exists for a localIdentifier". Two resources sharing one `PHAsset.localIdentifier`
therefore collide there as well — so the second resource would not merely overwrite a record, it
would be *detected as already existing* and skipped. Encoding the resource into the identifier
fixes both layers at once, because both derive from the same string.

`assetID` is therefore redefined as a **resource key**, not an asset identifier:

```
B84E8479-475C-4727-A4A4-B77AA9980897/L0/001#pairedVideo
└──────────────── localIdentifier ───────────────────┘ └─── kind ───┘
```

The separator is `#`, **not** `/`: `PHAsset.localIdentifier` already contains slashes
(`UUID/L0/001`), so `/` cannot be parsed unambiguously.

**Parsing splits on the _last_ `#`, and correctness does not depend on `#` being absent from
localIdentifiers.** Apple documents `localIdentifier` as an opaque persistent string and states no
character-set restriction, so assuming it excludes `#` would be an unverifiable premise. Instead,
`kind` is drawn from a **closed enum whose cases contain no `#`**, which makes last-separator
parsing unambiguous for *any* identifier: `A#B#pairedVideo` parses as `A#B` + `pairedVideo`, the
only valid reading. Splitting on the first `#` would be ambiguous and is forbidden.

This is why the key needs no `base64url` encoding layer: the closed `kind` alphabet already
supplies the delimiter guarantee that encoding would otherwise have to buy.

`ResourceKey` (core, pure string logic, tested) formats and parses this. The app maps
`ResourceKey.kind` to `PHAssetResourceType` through an explicit `switch`; an unmapped kind
**throws**. A silent fallback to the primary resource would upload the wrong bytes under the right
key — a corruption that no test would catch.

The `AssetDataSource` signature is unchanged; only the meaning of the string is sharpened.

**Doc impact:** `architecture.md:59-63` is stale in two ways, both corrected in this slice. It
documents records as keyed `uploads/<localIdentifier>` with `"id": "<localIdentifier>"`, but
`recordKey` is `uploads/` + **SafeID** (`store/index.go:143-145`), so the raw identifier appears in
neither the key nor the id. That staleness predates this slice — it is fixed here because §5 makes
the identifier's meaning load-bearing.

**The server needs no change** — but not for the reason an earlier draft of this document gave.
The `SplitN(key, "/", 4)` index scan (`server/internal/store/uploads.go:616`) is *not* why keys are
safe; ids never reach that path in raw form. `SafeID` hashes the identifier to fixed-length
base64url before it is ever used as a Badger key segment or URL path component (`ids.go:19-28`), so
any byte sequence — `/`, `#`, or otherwise — is already path-safe. `ResourceKey` only has to be
unambiguous to *the client*.

**Deliberately not load-bearing yet:** `SyncCoordinator` is what actually builds upload records,
and it is out of scope here (§9). Until it adopts `ResourceKey`, the Live Photo collision above
remains latent — this slice defines and tests the key, it does not fix the collision. That
sequencing is intentional, and the fix must not be reported as delivered.

---

## 6. Instrumentation and the measurement run

### 6.1 Why blob liveness cannot be used here

`BlobLifetimeTracker` counts through `didAllocate`/`didFree`, which fire only because
`FakeAssetDataSource` mints blobs via `Data(bytesNoCopy:deallocator:)`. PhotoKit's blobs are its
own allocations; we cannot hook their deallocation. Blob liveness is therefore unavailable on the
real run and is **not** the instrument for this slice.

### 6.2 What the concurrency counter proves — and what it does not

`StreamDiagnostics` counts concurrent `deliver()` entries. **This is a necessary check, not a
sufficient one.** An earlier draft of this document claimed a reading of 1 would prove capacity-1
holds end to end. That claim was wrong and is withdrawn.

Apple documents `requestData`'s data and completion blocks as arriving on an arbitrary **serial**
queue. Serial delivery guarantees non-concurrent *entry* by construction — so on the real run this
counter is expected to read exactly 1 *whether or not PhotoKit reads ahead*. A reading of 1 proves
only that no second thread entered `deliver`. It says nothing about blobs already materialized and
captured in blocks queued behind the one we are blocking.

Concretely: PhotoKit may read blob 2, capture it in a block enqueued on the same serial queue, and
only then invoke blob 1's handler. We block inside blob 1. Concurrency stays 1. Blob 2 is alive
anyway, and the diagnostic reports good news.

What the counter still earns its place for: a reading **> 1** falsifies serial delivery outright
and would invalidate the whole backpressure story, so it is worth watching — it simply cannot
confirm the happy case.

`StreamDiagnostics` is a `final class` guarded by an `NSLock`, mirroring `BlobLifetimeTracker` — it
is written from whatever threads PhotoKit delivers on and read from the consumer, so it cannot be a
value type. It also records blob count, total bytes, blob-size min/max, and first/last blob
wall-clock.

### 6.2.1 The instrument that measures retention: mid-stream stall

Because concurrency cannot see queue-ahead retention, retention is measured directly. Two earlier
designs of this measurement were wrong, and both failures are instructive:

- **A single stalled run.** Flat free space is consistent with *both* "backpressure held" and "read
  ahead into memory" — and `phys_footprint` cannot separate them either, per the recorded trap that
  it under-reports cold retained pages. No signal.
- **A stalled/unstalled pair, comparing time-shift.** The premise was that shared machine noise
  cancels. It does not: the procedure must **evict the asset between runs** (otherwise run B has it
  locally and never downloads), and eviction guarantees run B faces a different network and cache
  state. Download-rate variance alone can manufacture or mask the time shift. The comparison
  destroys the very condition it depends on.

**The measurement must therefore be within a single run**, comparing a stall against its own
immediate neighbourhood rather than against another run.

**Stall mid-stream, and compare free-space growth rate before / during / after.** Let the stream
reach steady state (~20 % in), hold the handler ~30 s without returning, then release.

| Growth rate during the stall | Conclusion |
|---|---|
| ≈ 0, then resumes on release | PhotoKit is genuinely blocked; backpressure is real |
| ≈ the pre-stall rate | PhotoKit materialises regardless of the handler; our ceiling governs only our own address space, which is all §3.1 ever claimed |

Network throughput, APFS purgeable recalculation and background processes are all effectively
constant across a window of tens of seconds within one run, so they cannot fake the transition —
which is exactly what the cross-run design could not guarantee. No eviction is required, because
the asset is mid-download when the stall begins.

**Repeat the stall three times in one run**, at different points. A real backpressure signal
reproduces at each stall; a coincidence of background disk activity does not.

**If the rates are ambiguous, the honest outcome is "unresolved", not a rounded-up conclusion.**
This experiment is capable of returning no answer, and that must be reportable.

### 6.3 Free space, not directory size

`DiskProbe.directorySize` enumerates a directory. Under App Sandbox that cannot read
`~/Pictures/Photos Library.photoslibrary` — Photos access via TCC grants PhotoKit access, not
filesystem access to the library container. The probe gains a free-space mode
(`.volumeAvailableCapacityForImportantUsageKey`), which is sandbox-safe and, at the sizes involved,
has a signal that dwarfs background noise.

### 6.4 The run

One large iCloud-only video, streamed **once**, with a sink that **discards** bytes — no network, no
server, no upload — while a background timer samples free space and footprint throughout.

The sink stalls three times mid-stream (§6.2.1), roughly at 20 %, 45 % and 70 %, holding ~30 s each.
The free-space growth rate before / during / after each stall is the result; the three stalls are
replicates of one measurement, not three measurements.

The same single run also answers `20260724-assetuploader.md` §11 item 1: whether the free-space
curve ramps alongside delivery or arrives as a cliff before the first blob shows whether PhotoKit
streams during download or materialises first.

The asset must be iCloud-only **at the start of the run** — if it is already materialised locally,
nothing downloads and the entire run is vacuous. That is a precondition to verify before starting,
not a result to interpret afterwards. Note this is the one place eviction is still needed; it is
just no longer needed *between* runs, where it did the damage (§6.2.1).

Max concurrent `deliver` is recorded, but as an alarm only — see §6.2 for why a reading of `1` is
expected either way and confirms nothing.

---

## 7. App wiring

Only what the run requires:

- `NSPhotoLibraryUsageDescription` in Info.plist
- `com.apple.security.personal-information.photos-library` entitlement
- A single-window view with an asset picker and a **Measure** button that reports diagnostics

No `MenuBarExtra` — that is a later slice. No network entitlement, since the sink discards.

---

## 8. Testing

All core tests are reachable by headless `swift test`.

**`CallbackStreamReader`**

1. `deliversBytesInOrderFromZero`
2. `deliversBytesInOrderFromOffset` — `OffsetSkip` integration, clause 1
3. `offsetBeyondEndYieldsNothing`
4. `zeroBlobsCompletesCleanly`
5. `sinkIsFullyAwaitedBeforeNextDelivery` — clause 2, capacity-1
6. `concurrentDeliverIsDetected` — see below
7. `sinkErrorPropagatesAndStopsProducer`
8. `sinkErrorInvokesCancelWithToken` — clause 3
9. `taskCancellationInvokesCancelWithToken` — clause 3
10. `taskCancellationThrowsCancellationError`
11. `blockedProducerIsReleasedOnCancellation` — the §4.1 leaked-thread path, asserted under timeout
12. `producerErrorPropagates`
13. `lateCallbacksAfterCancelReturnFalsePromptly` — §4.2
14. `cancellationWhileConsumerSuspendedResumesAndThrows` — §4.1 rule 1, the never-resume hang
15. `lateOnDoneAfterTerminalDoesNotDoubleResume` — §4.1 rule 2, the double-resume crash
16. `drainIsSignalledExactlyOncePerBlob` — §4.1 rule 3; a cancel racing a delivery must not leave a
    spare permit. Asserted by checking a subsequent `deliver` still blocks
17. `terminalDuringHandoffNeitherResumesNorSignals` — the `.handoff` window
18. `inlineSynchronousDeliveryIsSupported` — §4; the private queue makes this legal rather than
    fatal, so this asserts success, not a diagnostic
19. `cancellationBeforeTokenReturnsStillCancels` — §4.1.1; asserts `cancel` is invoked once the
    token materialises, rather than dropped

20. `resumedConsumerSignalsEvenWhenTerminal` — §4.1 ownership rule; a consumer resumed with a blob
    that wakes into `.terminal` must still signal, or the blocked producer is stranded
21. `nestedReadOnSameReaderDoesNotDeadlock` — §4; asserts the queue is per-call, not per-instance

**`ResourceKey`**

22. `roundTripsLocalIdentifierContainingSlashes`
23. `parsesOnLastSeparatorWhenIdentifierContainsHash` — §5, must not assume `#` is absent
24. `rejectsMalformedKeyAndUnknownKind`

### 8.1 Which tests carry unusual weight

`concurrentDeliverIsDetected` (6) drives the reader with a fake that **deliberately ignores**
backpressure and asserts diagnostics reports `> 1`. Since serial delivery means the real run is
expected to read `1` regardless (§6.2), an inert counter stuck at `1` would be indistinguishable
from a working one. This test is what makes a `> 1` alarm trustworthy.

Tests 14, 15 and 17 cover the continuation failures of §4.1 — a hang, a crash, and the `.handoff`
window between them — where the naive fix for one causes another. They must all be present; any
one alone is misleading.

Test 16 is the subtlest in the suite. An over-signalled semaphore does not fail anything directly:
it *loosens* capacity-1, so the code gets faster and the memory gate still passes at these blob
counts. It must be asserted positively — a later `deliver` still blocks — because nothing else in
the suite would notice.

Several of these cannot be written as "assert it does not deadlock": a deadlocking implementation
hangs the suite rather than failing it. Each must assert a specific detectable outcome under a
timeout.

### 8.2 Failure verification

Every test above is deliberately broken and watched to fail before it counts as done. The previous
slice produced four vacuous assertions; three were found only by injecting failures. A green test
nobody has watched fail proves nothing.

### 8.3 Honest residue

The ~40 lines of PhotoKit wiring — resource lookup, request options, the `ResourceKey.kind` →
`PHAssetResourceType` mapping — remain unreachable by `swift test`. Only the §6.4 run exercises
them. That is a smaller untested surface than the status quo, not a zero one.

---

## 9. Scope

**In this slice:**

- `CallbackStreamReader`, `ResourceKey`, `StreamDiagnostics` in core, with tests
- `DiskProbe` moved to `Sources/`, gains free-space mode
- `PhotosAssetDataSource` in the app target
- Xcode wiring: usage description, Photos entitlement, measurement UI
- The §6.4 measurement run, answering `20260724-assetuploader.md` §11 item 1
- `architecture.md` corrections (§5 doc impact)

**Deferred:**

- Free-space pre-flight guard. `20260724-assetuploader.md` §4 placed this in the adapter slice;
  deferring it is a **deliberate deviation**, because it needs a resource size and the only route
  the previous client used was private KVC (`value(forKey: "fileSize")`,
  `FileManagementService.swift:1192`). That decision deserves its own deliberation rather than
  being smuggled in here.
- `SyncCoordinator` adoption of `ResourceKey`
- `KeychainStore`, `MenuBarExtra` shell

---

## 10. Risks

1. **The measurement can invalidate an accepted assumption.** If free space keeps growing at its
   pre-stall rate during the §6.2.1 mid-stream stalls, PhotoKit materialises regardless of backpressure, and
   "backpressure is structural" is not true end to end. The remedy is not obvious: §3.1 of the
   uploader design forbids temp files *we* own, so the likely outcome is accepting PhotoKit's
   read-ahead and narrowing the documented claim to *our* address space only. Better to learn this
   before it is load-bearing.

   The experiment can also return **no answer** — an ambiguous shift is a legitimate outcome (§6.2.1)
   and must not be rounded into a conclusion in either direction.

   A `> 1` reading from the §6.2 concurrency counter would be a stronger and stranger alarm — it
   would falsify Apple's documented serial-delivery contract — but it is not the expected signal
   and its absence proves nothing.
2. **We block a Photos-owned thread for the duration of a network PATCH** — potentially seconds.
   Whether that stalls unrelated Photos work, or trips a watchdog, is unknown. The §6.4 run will
   show it.
3. **`DispatchSemaphore` misuse is a deadlock, not a wrong answer.** §4.1 enumerates every exit
   path for this reason, and test 11 asserts it under a timeout rather than hanging the suite.

---

## 11. Open items

1. **`PHAssetResource` size without private API.** Blocks the free-space pre-flight guard.
   Unresolved; see §9.

   Verified against the installed SDK (MacOSX26.1) rather than assumed: `PHAssetResource` exposes
   only `type`, `assetLocalIdentifier`, `originalFilename`, `contentType`, `uniformTypeIdentifier`,
   `pixelWidth`, `pixelHeight`. **There is no public size property of any name** — a review claim
   that `dataSize` is available in beta did not hold up against
   `Photos.framework/Versions/A/Headers/PHAssetResource.h`. Re-check on SDK bumps; this is the one
   open item a future SDK could close for free.
2. **Asset/offset divergence** (`20260724-assetuploader.md` §11.3) is unchanged by this slice. The
   adapter still cannot tell the uploader that an asset shrank between runs.
3. **Copy-shaped leaks remain invisible** (`20260724-assetuploader.md` §11.2). §6.1 narrows this
   further on the real run: blob liveness is unavailable, so retention is observed only indirectly,
   through the §6.2.1 free-space and footprint samples. Those are coarse — they detect PhotoKit
   read-ahead at asset scale, not a copy-shaped leak of a single blob.

4. **This slice defines `ResourceKey` but does not fix the Live Photo collision.** `SyncCoordinator`
   builds the records and is out of scope (§5, §9). Until it adopts the key, two resources of one
   Live Photo still collide on an identical `SafeID`. Recorded so the fix is not mistaken for
   delivered.
