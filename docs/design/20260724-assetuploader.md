# Design: AssetUploader + memory harness

**Date:** 2026-07-24
**Status:** Approved, ready for planning
**Package:** `apple/FilesNestCore`
**Depends on:** `docs/design/20260723-serverclient.md` (ServerClient, merged as `9eb5feb`)

---

## 1. Purpose

Upload a single Photos asset's bytes to the FilesNest server via TUS, with **peak memory
independent of asset size** and **exactly one pass over the asset's data**.

The previous iOS client failed both goals. This design's primary job is to make those two
failures structurally impossible rather than merely avoided by discipline.

---

## 2. Background: why the obvious designs are rejected

Two prior approaches are documented in the Serena memory `ios/memory_management_lessons` and
in `CODE_AUDIT.md`. Both are rejected here, and the reasons drive the whole design.

### 2.1 Unbounded `AsyncThrowingStream` — rejected

`PHAssetResourceManager.requestData`'s `dataReceivedHandler` delivers data as fast as the Photos
framework can read. `AsyncThrowingStream` has **no producer backpressure**. While the consumer
uploads chunk 1, the stream buffers chunks 2..N internally. For a 7 GB file that is 7 GB resident
and an OOM crash.

`AsyncThrowingStream`'s `bufferingPolicy` does not solve this: `.bufferingNewest(1)` and
`.bufferingOldest(1)` **drop** elements rather than throttling the producer. For file bytes,
dropping is silent data corruption, not throttling. There is no buffering policy that means
"block the producer."

### 2.2 Stream + semaphore, with a shared accumulation buffer — rejected

A `ChunkStream` using `DispatchSemaphore.wait()` to block `dataReceivedHandler` *did* serialize
uploads correctly. Memory still grew linearly, because `Data.append` combined with
`prefix`/`dropFirst` slicing on one long-lived buffer kept copy-on-write backing storage alive.
`autoreleasepool` cannot wrap an `await`, so there was no way to force cleanup between chunks.

### 2.3 Per-chunk `requestData` — rejected

The shipped workaround (`ios-client/FilesNest/Services/FileManagementService.swift:464-538`)
issued a **separate `requestData` call per chunk**, skipping `startOffset` bytes each time. Memory
was flat, because each call's `chunkData` was the sole reference to its bytes and ARC freed it per
loop iteration.

The cost was quadratic. `CODE_AUDIT.md:114` rates it Critical:

> each chunk forces a full iCloud materialization → an N-chunk file transfers ~N full copies
> (O(N²)) … This is the exact redundant download the app exists to avoid.

Compounding it, `FileManagementService.swift:407-462` re-streamed the entire asset a second time
to compute a SHA256 (audit §5.2), and `ContentViewModel.swift:76-86` prefetched every asset over
iCloud into a cache the upload path never read (audit §7.1).

**Note on evidence quality:** the audit's O(N²)-*network* claim is a code-reading conclusion, not
an instrumented measurement. Whether PhotoKit re-downloads per call or caches locally and re-reads
changes the severity — O(N²) network vs O(N²) local reads — but not the conclusion that the design
is unacceptable.

### 2.4 What this design takes from that

- Backpressure must be **structural**, not coordinated by hand.
- There must be **no shared accumulation buffer** to mismanage.
- Each blob must be the **sole reference** to its bytes, released when its upload returns.
- One pass. One materialization per asset.

---

## 3. Correction: the "no temp files" constraint

`architecture.md:19` currently claims:

> A 7GB video never lands on disk on the Mac or as a completed file in the server before it is
> moved to final storage.

**The Mac half of that claim is false and must be corrected.**

When an asset is iCloud-only (Optimize Mac Storage), its bytes are not on the machine. The only
sanctioned way to obtain them is `PHAssetResourceManager.requestData` with
`isNetworkAccessAllowed = true`, and PhotoKit materializes the resource into the Photos library
container in order to serve it. There is **no public PhotoKit API for a ranged iCloud fetch** —
you cannot request "bytes 0–8 MB of this asset" and have only that fetched from iCloud.

This is not a shortcoming of our design; any Mac app reading original asset data pays it. The old
iOS client did not avoid it either — it depended on that materialization and, per audit §5.1,
triggered it repeatedly.

### 3.1 What the constraint actually means

> **No temp files *we* own, and no whole-file buffering in *our* memory.**

The bytes do touch the disk. The copy is **PhotoKit's**: PhotoKit creates it, owns its lifecycle,
and evicts it under disk pressure. We never create, name, or delete a file, and we never hold more
than a bounded window of bytes in our address space.

### 3.2 What we do control

| Lever | Effect |
|---|---|
| Single pass (this design) | **One** materialization per asset instead of N |
| Sequential processing (`architecture.md:190`) | At most one asset materialized at a time |
| Free-space pre-flight (adapter slice) | Skip with a typed error instead of filling the disk |

Peak transient cost is therefore roughly **the largest single asset**, not the library, and macOS
reclaims it under pressure.

### 3.3 Open question, to be answered by measurement

Whether PhotoKit delivers bytes *as they download* or only *after* the download completes is
unverified. It affects perceived latency on large videos, not correctness and not our memory
ceiling. `DiskProbe` (§7) exists to replace this uncertainty with a number, in the adapter slice.

---

## 4. Scope

**In this slice** — all provable by headless `swift test`:

- `MemoryProbe` — resident-footprint sampling
- `DiskProbe` — directory-size delta, plumbing and unit tests only
- `AssetDataSource` — the protocol seam
- `OffsetSkip` — tested skip helper for adapters
- `FakeAssetDataSource` — synthetic source, 3 MB to 3 GB
- `AssetUploader`
- Go e2e test for the zero-byte PATCH edge case (§6.3)
- `architecture.md` corrections (§3, §9)

**Deferred to the adapter slice:**

- `PhotosAssetDataSource` (macOS app target, imports Photos)
- Real iCloud disk-delta measurements
- Free-space pre-flight guard — needs the resource size, which only the real adapter supplies
- Xcode project setup, Photos entitlements, TCC consent

This slice does **not** answer the PhotoKit question in §3.3. It builds the instrument that will.

---

## 5. The seam

`FilesNestCore` stays pure Foundation. Photos is a UI-framework dependency and does not belong in
a package shared by macOS and a future iOS client. The seam mirrors the existing `CredentialStore`
pattern exactly.

```swift
public protocol AssetDataSource: Sendable {
    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws
}
```

### 5.1 Why an async sink rather than a returned stream

The sink **is** the backpressure. The source cannot consume its next callback until the sink's
`await` returns, so "at most one blob in flight" is guaranteed by the signature rather than by
coordination code we have to keep correct. No stream buffer exists, so no stream buffer can leak.

The adapter still needs one semaphore internally to bridge PhotoKit's synchronous
`dataReceivedHandler` to the async sink — but that is contained inside the adapter, and the core
invariant no longer depends on getting it right.

### 5.2 Contract

Conformances must:

1. Deliver bytes **starting at `offset`**, in order, without gaps or overlaps.
2. **Fully await `sink` before producing the next blob.** This is the capacity-1 guarantee.
3. **Honour `Task` cancellation** and stop producing promptly. Audit §5.1 flags the old client's
   un-cancellable Photos request as part of its Critical finding; this clause exists so it cannot
   recur.
4. Throw on failure. Partial delivery is legal — the next run resumes from the TUS offset.

### 5.3 `OffsetSkip`

PhotoKit always restarts at byte 0, so `PhotosAssetDataSource` must discard bytes below `offset`
to satisfy clause 1. That discard is precisely the `dropFirst` shape implicated in §2.2, and it
would live in the one component `swift test` cannot reach.

Core therefore ships a small, fully-tested value type that adapters use instead of hand-rolling
the skip:

```swift
struct OffsetSkip {
    private var remaining: Int64
    init(skipping: Int64)
    /// Returns the portion of `blob` at or beyond the skip point, or nil if fully consumed.
    mutating func take(_ blob: Data) -> Data?
}
```

`take` must return a **fresh** `Data` when it slices, never a slice aliasing the input's storage,
so the input blob's buffer is released when the caller drops it.

---

## 6. AssetUploader

```swift
public struct AssetUploader: Sendable {
    let client: ServerClient
    let source: any AssetDataSource

    public func upload(assetID: String, uploadID: String) async throws
}
```

### 6.1 Flow

1. `client.offset(forUploadID:)` → `startOffset`
2. `source.read(assetID:from: startOffset)` with the look-ahead sink below
3. PATCH the final held blob with `finalLength` — or, if no blob was ever held, take the zero-blob
   path in §6.3
4. `client.markComplete(uploadID:)`

### 6.2 Look-ahead by one

The server declares an upload complete only when a declared `Upload-Length` equals the offset
(`tushandler_test.go:154-189`). But a blob is only known to be the last one when the source's
completion fires — *after* a naive pass-through would already have sent it.

Holding one blob back resolves this without private API:

```swift
var offset = startOffset
var held: Data?

try await source.read(assetID: assetID, from: startOffset) { blob in
    if let previous = held {
        offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                            data: previous, finalLength: nil)
    }
    held = blob                     // at most two blobs alive
}

if let last = held {
    offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                        data: last, finalLength: offset + Int64(last.count))
}
```

Peak is **two blobs**, not one. Bounded and size-independent, which is what the gate requires —
but the harness asserts a two-blob floor, not a one-blob floor, and the spec says so rather than
letting a later reader assume otherwise.

**The snippet above is illustrative, not compilable** — it is kept here only to show the algorithm.
Capturing and mutating local `var`s inside a `@Sendable` closure is rejected under Swift 6.
Verified against Swift 6.2.1 in Swift 6 language mode (tools-version 6.0 with no
`swiftLanguageModes` override ⇒ Swift 6 mode, complete strict concurrency):

```
error: mutation of captured var 'offset' in concurrently-executing code [#SendableClosureCaptures]
error: mutation of captured var 'held'   in concurrently-executing code [#SendableClosureCaptures]
```

### 6.2.1 Resolution: state in an actor

The look-ahead state moves into a private `actor`. The sink closure then captures **only a
Sendable actor reference** and mutates nothing locally, so `@Sendable` stays on the seam in §5 —
no adapter-visible signature change:

```swift
private actor LookAhead {
    private var offset: Int64
    private var held: Data?
    private var inFlight = false

    func consume(_ blob: Data) async throws {
        guard !inFlight else { throw AssetUploaderError.concurrentSinkCall }
        inFlight = true
        defer { inFlight = false }

        if let previous = held {
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: previous, finalLength: nil)
        }
        held = blob
    }
    func finish() async throws { /* final blob, or §6.3 zero-blob path */ }
}

// call site — captures a Sendable actor ref, no local mutation
try await source.read(assetID: assetID, from: startOffset) { blob in
    try await state.consume(blob)
}
try await state.finish()
```

This shape typechecks clean under `swiftc -swift-version 6 -typecheck`.

**Correction (found during implementation): the `inFlight` guard shown above was removed.**

The reasoning for it was that an actor serializes *access* but is **reentrant**, so two concurrent
`consume` calls would interleave across the `await` on `patchData` and produce out-of-order
offsets — and that "nothing in the type system enforces" the §5.2 contract.

That last part is false. `sink` is a **non-escaping** parameter of `read`, so a conformance cannot
hand it to a task group or `async let`. The attempt fails to compile:

```
error: escaping closure captures non-escaping parameter 'sink'
```

Concurrent sink invocation is therefore **unrepresentable**, not merely forbidden, and the guard
was unreachable code. It and the `AssetUploaderError` enum that existed only to serve it were
both deleted. The test that was meant to cover the guard could not be written at all — the
compiler rejected the malicious conformance.

`sinkIsInvokedStrictlySequentially` now pins the *consequence* (a gapless ascending offset chain)
instead, so that changing `sink` to `@escaping` — which would silently restore the hazard —
breaks a test. That requirement is also recorded in the source at `LookAhead.consume`.

**Rejected alternatives:**

| Option | Why not |
|---|---|
| `final class` + `Mutex` | `Synchronization.Mutex` is macOS 15+; core's floor is macOS 13. Worse, the state mutation spans the `patchData` await, and holding a lock across a suspension point is invalid regardless. |
| Drop `@Sendable` from the sink | Would weaken the contract for every adapter to work around a problem the actor solves without any signature change. |

Cost of the actor: one `await` hop per blob. Against an ~8 MB HTTP PATCH, that is noise.

Rejected alternative: reading `PHAssetResource`'s size upfront. The old client did this via
`resources.first?.value(forKey: "fileSize") as? Int64 ?? 0`
(`FileManagementService.swift:1190-1192`) — undocumented KVC, silently defaulting to `0`.

### 6.3 Zero-blob edge case

If `read` delivers no blobs, there is no blob to carry `finalLength`, and tusd will not complete an
upload whose size is still deferred. This occurs when a resumed upload's bytes were all sent on a
previous run but the length was never declared.

Resolution: a **zero-byte PATCH declaring `Upload-Length = startOffset`**.

`handlers.go:616-619` forwards the client's `Upload-Length` header into `ForwardPatch`, so the
server needed no change. No existing Go test covered a 0-byte PATCH, so this slice added
`TestTUSZeroByteFinalPatchDeclaresLength` (`server/internal/uploadbackend/tushandler_test.go:883`).
**Confirmed accepted:** tusd logs `ChunkWriteComplete bytesWritten=0` followed by
`UploadFinished size=40`. The test ran before any uploader code depended on it.

### 6.4 Error handling

`AssetUploader` handles nothing and propagates everything.

- `ServerClientError.backendLost` (409) propagates to `SyncCoordinator`, which owns the
  delete-and-re-register recovery (`architecture.md:191`).
- `CancellationError` propagates.
- Transport errors propagate; the next run resumes from the TUS offset.

Keeping recovery out of the uploader is what allows it to remain a stateless `struct`.

---

## 7. Memory harness

Built **first**, before any uploader code. It is the gate the uploader must pass.

### 7.1 What was built first and why it was replaced

The original design specified sampling `phys_footprint` from `task_vm_info` and comparing peak
growth across asset sizes. That was implemented, and then **disproved by injecting a leak**.

Three separate defects each produced a *vacuous pass* — a green gate over broken code:

| Defect | Symptom |
|---|---|
| Baseline contamination | `peakGrowth` is relative, and the allocator does not return freed pages promptly, so whichever case ran second started from an inflated baseline. The 3 GB case reported a **negative** delta against the 384 MB case. |
| Compressible blobs | The fake wrote one byte per 4 KB page, ~99.97% zeros. macOS's memory compressor erased them, so leaked memory did not appear in `phys_footprint`. |
| **Churn-induced eviction** (fatal) | With a leak retaining a verified **200,802,304 bytes**, `phys_footprint` reported **39 MB**. The same 200 MB retained *without* concurrent allocation churn reported **148 MB**. A large upload churns by definition, so the OS compresses and swaps the cold retained pages out of the metric. |

The first two were fixed. The third is not fixable: `phys_footprint` is structurally blind to
retained memory precisely under the workload the gate must measure. **OS memory accounting was
abandoned as a leak detector.**

### 7.2 The gate: exact blob liveness

`FakeAssetDataSource` allocates each blob itself and wraps it in
`Data(bytesNoCopy:count:deallocator:)`. The custom deallocator fires exactly when the last
reference to that buffer dies, so `BlobLifetimeTracker` knows precisely how many blobs are alive at
any instant. No OS metric is involved, nothing can be compressed or swapped away, and the result is
deterministic.

Measured on clean code, 8 MB blobs:

| Case | Blobs | `maxAlive` | `aliveAfter` |
|---|---|---|---|
| 3 MB photo | 1 | 1 | 0 |
| 64 MB | 8 | 2 | 0 |
| 512 MB | 64 | 2 | 0 |
| 3 GB video | 384 | **2** | 0 |

`maxAlive = 2` is exactly the look-ahead floor, and it is identical from 8 blobs to 384. Runtime
~4 s for the whole suite.

Each measurement also asserts bytes actually transferred and blobs actually allocated, so a source
that silently yielded nothing cannot satisfy the liveness assertions trivially.

### 7.3 Verified to have teeth

A passing gate proves nothing unless it can fail. Both leak shapes were injected into `LookAhead`:

| Injected leak | `maxAlive` 8 → 64 blobs | Caught |
|---|---|---|
| `leak.append(previous.dropFirst(1))` — aliasing slice, COW-retains the whole buffer; **the historical bug** | 8 → 64 | **yes** |
| `leak.append(Data(previous.prefix(512 * 1024)))` — copies into fresh storage | 2 → 2 | no |

The aliasing case is the failure mode recorded in `ios/memory_management_lessons`, and it produces
a perfectly linear signal — peak liveness tracks asset size exactly, which is unmistakable.

**Known, unclosed gap:** a leak that *copies* bytes into fresh storage allocates memory the tracker
never handed out, so liveness cannot see it — and `phys_footprint` cannot see it either, for the
churn reason in §7.1. Closing it would need process-external RSS measurement or memory scanning,
both judged disproportionate for this slice. Recorded here rather than left implicit.

### 7.4 Harness validity

`FakeAssetDataSourceTests` asserts the tracker observes every allocation and free, and that a
deliberately retained blob keeps `alive` elevated until released. Without those, every liveness
assertion downstream could pass vacuously.

`MemoryProbe` is retained for the adapter slice, where measuring real iCloud behaviour is the goal,
but its limitations from §7.1 must be respected there too. The footprint-based tests written for
this slice were **deleted** rather than left in place: with liveness tracking they measured nothing
reliable (one recorded a delta of exactly 0 across a 512 MB upload) and would only have produced
flaky failures and false confidence.

### 7.5 DiskProbe

Measures the byte-size delta of a directory across a unit of work. Built and unit-tested here
against a temp directory. It produces **no iCloud numbers in this slice** — that requires the real
adapter, a real library, and TCC consent, all of which land in the adapter slice.

## 8. Testing

- Anything touching `MockURLProtocol` lives in a `@Suite(.serialized)` — Swift Testing parallelises
  by default and the stub handler is shared static state.
- `FakeAssetDataSource` is configurable by total size and blob size, and must honour the §5.2
  contract, including cancellation — so uploader cancellation is testable headlessly.
- Contract tests for `OffsetSkip`: skip 0, skip mid-blob, skip spanning several blobs, skip beyond
  total, and confirmation that returned `Data` does not alias its input.
- `AssetUploader` tests: fresh upload, resume from non-zero offset, zero-blob path, `backendLost`
  propagation, cancellation propagation, and correct `finalLength` on the last PATCH only.
- The memory gate is its own serialized test.
- Go: e2e test for §6.3.

Verification per the project convention:

```
cd apple/FilesNestCore && swift test
swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x
```

---

## 9. Documentation changes

| File | Change |
|---|---|
| `architecture.md:19` | Constraint #1 rewritten per §3.1, **including the why** — PhotoKit owns the copy, no public ranged-fetch API exists |
| `architecture.md:161-167` | `AsyncThrowingStream` + 8 MB buffer prescription replaced with the sink seam and pass-through, **including why the stream shape was rejected** (§2.1, §2.2), so it is not reintroduced |
| `docs/plans/20260724-serverclient.md` | Global Constraints says `.macOS(.v15)`; actual is core 13 / app 14 |
| `docs/design/20260723-serverclient.md` | §10 open items are all resolved; mark them so |

---

## 10. Decisions made during design

| Decision | Rationale |
|---|---|
| Flat memory **and** single pass | Rejects the O(N²) fallback; §2.3 |
| Protocol seam, core stays pure Foundation | Mirrors `CredentialStore`; keeps `swift test` headless |
| Async sink, not a returned stream | Backpressure by construction; §5.1 |
| Pass-through PATCHes, no accumulation buffer | Designs out the COW leak; §2.2 |
| Look-ahead by one blob | Exact `finalLength` without private API; §6.2 |
| Core + fake source this slice | Real iCloud measurement needs the adapter; §4 |
| Accept PhotoKit materialization, bound and measure it | No public alternative exists; §3 |

## 11. Open items

**Still open:**

1. **Does PhotoKit stream during download or materialize first?** (§3.3) Affects perceived latency
   on large videos only — not correctness, and not our memory ceiling. To be answered by `DiskProbe`
   in the adapter slice, against a real library.
2. **Copy-shaped leaks are undetectable.** (§7.3) Blob-liveness tracking cannot see a leak that
   copies bytes into fresh storage, and `phys_footprint` cannot see it either under allocation
   churn. Closing this needs process-external RSS measurement or memory scanning, both judged
   disproportionate here. Accepted, not forgotten.
3. **The uploader cannot detect asset/offset divergence.** (§6.3) It trusts the server's HEAD
   offset unconditionally. If a prior run uploaded more bytes than the asset now contains — the
   asset changed or was re-encoded between runs — the source yields nothing, the zero-blob path
   declares `Upload-Length = startOffset`, and `markComplete` finalizes a file longer than the
   local asset. Raised by adversarial review.

   Not fixable in this slice: with `Upload-Defer-Length` the client has no trustworthy length to
   compare against, and reading `PHAssetResource.fileSize` means private KVC (§6.2). The declared
   length is at least always consistent with what the server actually holds, so the file is never
   truncated or mis-sized relative to its own bytes. Revisit when the adapter can supply a resource
   size through a supported API.

**Resolved during implementation:**

3. ~~Does tusd accept a zero-byte PATCH declaring `Upload-Length`?~~ **Yes.** Proven by
   `TestTUSZeroByteFinalPatchDeclaresLength`; tusd logs `ChunkWriteComplete bytesWritten=0` then
   `UploadFinished size=40`. The §6.3 zero-blob path is sound and needs no server change.
4. ~~How look-ahead state crosses the `@Sendable` boundary.~~ Settled in §6.2.1: a private `actor`.
   The reentrancy guard originally specified was **removed** — `sink` is non-escaping, so concurrent
   invocation is unrepresentable, not merely forbidden. The seam signature in §5 is unchanged.
5. ~~Final gate thresholds.~~ Obsolete. The footprint gate those thresholds belonged to was
   abandoned (§7.1); the liveness gate asserts an exact blob count, not a tuned byte threshold.
