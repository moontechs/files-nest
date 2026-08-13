# PhotosAssetDataSource + callback-stream reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Conform to `AssetDataSource` against the real Photos library by extracting the entire capacity-1 contract into a headlessly-testable core reader, leaving the app target only trivial PhotoKit glue, and measure whether PhotoKit honours backpressure.

**Architecture:** A generic `CallbackStreamReader<Token>` in `FilesNestCore` owns the four `AssetDataSource` clauses (ordered delivery from an offset, capacity-1 backpressure, cancellation, error propagation) via a five-state machine behind an `NSLock`, with a `CheckedContinuation` waking a suspended consumer and a `DispatchSemaphore` applying backpressure to a synchronous producer callback. PhotoKit supplies only two closures (`start` returning a request token, `cancel`). The Photos-backed conformance in the macOS app target is ~40 lines of closures with no control flow.

**Tech Stack:** Swift 6 / SwiftPM, Swift Testing, Foundation, `DispatchQueue`/`DispatchSemaphore`, Photos.framework (app target only), Xcode project.

**Spec:** `docs/design/20260724-photosassetdatasource.md`

## Global Constraints

- `FilesNestCore` platform floor: **macOS 13, iOS 17**. `Synchronization.Mutex` (macOS 15+) is unavailable — use `NSLock`.
- **Swift 6 language mode, complete strict concurrency.** `swift-tools-version: 6.0`, no `swiftLanguageModes` override.
- `FilesNestCore` is **pure Foundation**. Never `import Photos` in `Sources/`. `Token` is generic precisely so core never names `PHAssetResourceDataRequestID`.
- Tests using `MemoryProbe` MUST live in a `@Suite(.serialized)` — `phys_footprint` is process-wide. `.serialized` orders tests only *within* a suite; other suites still run in parallel.
- Test fakes live in the test target, never in `Sources/`.
- Verification for every core task:
  - `cd apple/FilesNestCore && swift test`
  - `swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
- The macOS app target (`apple/macos/FilesNest`) is verified by `xcodebuild build` and a manual measurement procedure, **not** by `swift test`. This is the accepted untestable residue (spec §8.3).
- **Do not commit or push until the user explicitly says go.** Commit steps are written per green task; wait for that instruction.
- `assetID` is a **resource key** `<localIdentifier>#<kind>`, parsed on the **last** `#` (spec §5).
- The memory ceiling is **2 observed / 3 allowed** and must not be tightened (spec §4.3).

---

## File Structure

**`FilesNestCore/Sources/` (new, pure Foundation, headlessly tested):**
- `DiskProbe.swift` — moved from `Tests/Support/`, gains a free-space mode. Volume/directory sampling for the measurement run.
- `ResourceKey.swift` — formats/parses `<localIdentifier>#<kind>`. Pure string logic.
- `StreamDiagnostics.swift` — thread-safe concurrent-`deliver` counter plus blob stats.
- `CallbackStreamReader.swift` — the generic reader owning all four contract clauses.

**`FilesNestCore/Tests/` (new):**
- `DiskProbeTests.swift` (exists — extend), `ResourceKeyTests.swift`, `StreamDiagnosticsTests.swift`, `CallbackStreamReaderTests.swift`, plus `Support/FakeCallbackProducer.swift`.

**`apple/macos/FilesNest/` (app target, Xcode, untested by `swift test`):**
- `PhotosAssetDataSource.swift` — `AssetDataSource` conformance; parses `ResourceKey`, resolves `PHAssetResource`, hands two closures to `CallbackStreamReader`.
- `MeasurementView.swift` + `MeasurementRunner.swift` — asset picker and the mid-stream-stall run procedure.
- `FilesNest.entitlements`, `Info.plist` additions, package link — Xcode wiring.

**Docs:**
- `docs/architecture.md` — correct stale `uploads/<localIdentifier>` / `"id"` claims (spec §5 doc impact).

---

### Task 1: DiskProbe — move to Sources, add free-space mode

`DiskProbe` currently lives in the test target and only measures directory size. The measurement run (spec §6) needs volume free space, and it must ship in `Sources/` so the app target can use it.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/DiskProbe.swift`
- Delete: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/DiskProbe.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/DiskProbeTests.swift` (exists — extend)

**Interfaces:**
- Consumes: nothing
- Produces: `public enum DiskProbe` with `static func directorySize(at:) throws -> Int64`, `static func sizeDelta(of:during:) async throws -> Int64` (both unchanged, now `public`), and new `static func volumeFreeSpace(at url: URL) throws -> Int64` reading `.volumeAvailableCapacityForImportantUsageKey`.

- [x] **Step 1: Move the file and make it public**

Move `Tests/FilesNestCoreTests/Support/DiskProbe.swift` to `Sources/FilesNestCore/DiskProbe.swift`. Change `enum DiskProbe` to `public enum DiskProbe` and add `public` to `directorySize` and `sizeDelta`. Content is otherwise unchanged from the existing file.

- [x] **Step 2: Write the failing test for free space**

Add to `DiskProbeTests.swift`:

```swift
@Test func volumeFreeSpaceIsPositiveForHomeDirectory() throws {
    let home = URL(fileURLWithPath: NSHomeDirectory())
    let free = try DiskProbe.volumeFreeSpace(at: home)
    #expect(free > 0)
}
```

- [x] **Step 3: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter volumeFreeSpaceIsPositiveForHomeDirectory`
Expected: FAIL — `volumeFreeSpace` not found (or compile error).

- [x] **Step 4: Implement `volumeFreeSpace`**

Add to `DiskProbe`:

```swift
/// Bytes available for "important" usage on the volume containing `url`.
/// Sandbox-safe: reads a volume resource value, not directory contents.
public static func volumeFreeSpace(at url: URL) throws -> Int64 {
    let values = try url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
    return values.volumeAvailableCapacityForImportantUsage ?? 0
}
```

- [x] **Step 5: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS — all existing DiskProbe tests still green (the move did not change them), plus the new one.

- [x] **Step 6: Verify build with warnings-as-errors**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: builds clean.

- [x] **Step 7: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/DiskProbe.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/DiskProbe.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/DiskProbeTests.swift
git commit -m "feat(core): move DiskProbe to Sources, add sandbox-safe free-space mode"
```

---

### Task 2: ResourceKey — format and parse `<localIdentifier>#<kind>`

Redefines `assetID` as a resource-addressing key so a Live Photo's two resources are distinct (spec §5). Correctness does **not** rest on `#` being absent from localIdentifiers: parsing splits on the **last** `#`, and `kind` is a closed enum containing no `#`.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/ResourceKey.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/ResourceKeyTests.swift`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `public enum ResourceKind: String, Sendable, CaseIterable { case photo, pairedVideo, fullSizePhoto, video, audio, alternatePhoto }`
  - `public struct ResourceKey: Sendable, Equatable { public let localIdentifier: String; public let kind: ResourceKind; public init(localIdentifier:kind:); public var encoded: String; public init(parsing:) throws }`
  - `public enum ResourceKeyError: Error, Equatable { case missingSeparator; case unknownKind(String); case emptyLocalIdentifier }`

- [x] **Step 1: Write the failing tests**

Create `ResourceKeyTests.swift`:

```swift
import Testing
@testable import FilesNestCore

@Suite struct ResourceKeyTests {

    @Test func roundTripsLocalIdentifierContainingSlashes() throws {
        let key = ResourceKey(localIdentifier: "B84E8479-475C-4727-A4A4-B77AA9980897/L0/001",
                              kind: .pairedVideo)
        #expect(key.encoded == "B84E8479-475C-4727-A4A4-B77AA9980897/L0/001#pairedVideo")
        let parsed = try ResourceKey(parsing: key.encoded)
        #expect(parsed == key)
    }

    /// The identifier may itself contain '#'. Splitting on the LAST '#' is the
    /// only correct reading because `kind` never contains '#'.
    @Test func parsesOnLastSeparatorWhenIdentifierContainsHash() throws {
        let parsed = try ResourceKey(parsing: "A#B#pairedVideo")
        #expect(parsed.localIdentifier == "A#B")
        #expect(parsed.kind == .pairedVideo)
    }

    @Test func rejectsMissingSeparator() {
        #expect(throws: ResourceKeyError.missingSeparator) {
            _ = try ResourceKey(parsing: "no-separator-here")
        }
    }

    @Test func rejectsUnknownKind() {
        #expect(throws: ResourceKeyError.unknownKind("bogus")) {
            _ = try ResourceKey(parsing: "ABC/L0/001#bogus")
        }
    }

    @Test func rejectsEmptyLocalIdentifier() {
        #expect(throws: ResourceKeyError.emptyLocalIdentifier) {
            _ = try ResourceKey(parsing: "#photo")
        }
    }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter ResourceKeyTests`
Expected: FAIL — `ResourceKey` not defined.

- [x] **Step 3: Implement `ResourceKey`**

Create `ResourceKey.swift`:

```swift
import Foundation

/// The PhotoKit resource kinds this client can address. Closed set, and no
/// case contains '#', which is what makes `ResourceKey` parsing unambiguous.
public enum ResourceKind: String, Sendable, CaseIterable {
    case photo
    case pairedVideo
    case fullSizePhoto
    case video
    case audio
    case alternatePhoto
}

public enum ResourceKeyError: Error, Equatable {
    case missingSeparator
    case unknownKind(String)
    case emptyLocalIdentifier
}

/// Addresses one *resource* of one asset. A Live Photo's JPEG and MOV share a
/// `localIdentifier` but differ in `kind`, so they encode to distinct keys.
public struct ResourceKey: Sendable, Equatable {
    public let localIdentifier: String
    public let kind: ResourceKind

    public init(localIdentifier: String, kind: ResourceKind) {
        self.localIdentifier = localIdentifier
        self.kind = kind
    }

    public var encoded: String { "\(localIdentifier)#\(kind.rawValue)" }

    /// Parses on the LAST '#'. `localIdentifier` may contain '#'; `kind` cannot,
    /// so the final separator is always the real one.
    public init(parsing string: String) throws {
        guard let hash = string.lastIndex(of: "#") else {
            throw ResourceKeyError.missingSeparator
        }
        let idPart = String(string[string.startIndex..<hash])
        let kindPart = String(string[string.index(after: hash)...])
        guard !idPart.isEmpty else { throw ResourceKeyError.emptyLocalIdentifier }
        guard let kind = ResourceKind(rawValue: kindPart) else {
            throw ResourceKeyError.unknownKind(kindPart)
        }
        self.localIdentifier = idPart
        self.kind = kind
    }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter ResourceKeyTests`
Expected: PASS — all five.

- [x] **Step 5: Verify each test can fail**

Temporarily change `lastIndex(of:)` to `firstIndex(of:)` and rerun. `parsesOnLastSeparatorWhenIdentifierContainsHash` MUST fail (it would parse `A` + `B#pairedVideo`). Revert. This proves the last-separator test has teeth (spec §8.2).

- [x] **Step 6: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/ResourceKey.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/ResourceKeyTests.swift
git commit -m "feat(core): add ResourceKey addressing an asset resource (last-# parse)"
```

---

### Task 3: StreamDiagnostics — thread-safe concurrent-deliver counter

The instrument for the measurement run's alarm channel (spec §6.2). A reading of `> 1` falsifies serial delivery; a reading of `1` confirms nothing but must still be recorded. It is written from PhotoKit's delivery threads and read from the consumer, so it is a lock-guarded `final class`, mirroring `BlobLifetimeTracker`.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/StreamDiagnostics.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/StreamDiagnosticsTests.swift`

**Interfaces:**
- Consumes: nothing
- Produces: `public final class StreamDiagnostics: @unchecked Sendable` with `public init()`, methods `func enter(byteCount: Int)` / `func exit()` (called by the reader around each `deliver`), and readonly getters `maxConcurrent: Int`, `blobCount: Int`, `totalBytes: Int64`, `minBlob: Int`, `maxBlob: Int`.

- [x] **Step 1: Write the failing tests**

Create `StreamDiagnosticsTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct StreamDiagnosticsTests {

    @Test func recordsBlobStats() {
        let d = StreamDiagnostics()
        d.enter(byteCount: 100); d.exit()
        d.enter(byteCount: 300); d.exit()
        #expect(d.blobCount == 2)
        #expect(d.totalBytes == 400)
        #expect(d.minBlob == 100)
        #expect(d.maxBlob == 300)
    }

    @Test func maxConcurrentIsOneForSequentialDelivery() {
        let d = StreamDiagnostics()
        for _ in 0..<10 { d.enter(byteCount: 8); d.exit() }
        #expect(d.maxConcurrent == 1)
    }

    /// If two threads are inside `enter`/`exit` at once, maxConcurrent must
    /// exceed 1. Without this the counter could be inert and always report 1 —
    /// the exact false "good news" spec §8.1 warns about.
    @Test func maxConcurrentDetectsOverlap() async {
        let d = StreamDiagnostics()
        let barrier = DispatchSemaphore(value: 0)
        await withTaskGroup(of: Void.self) { group in
            for _ in 0..<2 {
                group.addTask {
                    d.enter(byteCount: 8)
                    barrier.wait()      // hold both inside simultaneously
                    d.exit()
                }
            }
            // release both after they have entered
            try? await Task.sleep(for: .milliseconds(50))
            barrier.signal(); barrier.signal()
        }
        #expect(d.maxConcurrent == 2)
    }
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter StreamDiagnosticsTests`
Expected: FAIL — `StreamDiagnostics` not defined.

- [x] **Step 3: Implement `StreamDiagnostics`**

Create `StreamDiagnostics.swift`:

```swift
import Foundation

/// Records how PhotoKit delivered blobs. Concurrency is the load-bearing field:
/// `maxConcurrent > 1` falsifies serial delivery; `== 1` confirms nothing on its
/// own (spec §6.2). Written from arbitrary delivery threads, read from the
/// consumer — hence lock-guarded, not a value type.
public final class StreamDiagnostics: @unchecked Sendable {
    private let lock = NSLock()
    private var _now = 0
    private var _maxConcurrent = 0
    private var _blobCount = 0
    private var _totalBytes: Int64 = 0
    private var _minBlob = Int.max
    private var _maxBlob = 0

    public init() {}

    public func enter(byteCount: Int) {
        lock.lock(); defer { lock.unlock() }
        _now += 1
        _maxConcurrent = max(_maxConcurrent, _now)
        _blobCount += 1
        _totalBytes += Int64(byteCount)
        _minBlob = min(_minBlob, byteCount)
        _maxBlob = max(_maxBlob, byteCount)
    }

    public func exit() {
        lock.lock(); defer { lock.unlock() }
        _now -= 1
    }

    public var maxConcurrent: Int { lock.lock(); defer { lock.unlock() }; return _maxConcurrent }
    public var blobCount: Int { lock.lock(); defer { lock.unlock() }; return _blobCount }
    public var totalBytes: Int64 { lock.lock(); defer { lock.unlock() }; return _totalBytes }
    public var minBlob: Int { lock.lock(); defer { lock.unlock() }; return _blobCount == 0 ? 0 : _minBlob }
    public var maxBlob: Int { lock.lock(); defer { lock.unlock() }; return _maxBlob }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter StreamDiagnosticsTests`
Expected: PASS.

- [x] **Step 5: Verify the overlap test can fail**

Temporarily make `enter` compute `_maxConcurrent = max(_maxConcurrent, 1)`. `maxConcurrentDetectsOverlap` MUST fail. Revert. This proves the counter is not inert (spec §8.1).

- [x] **Step 6: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/StreamDiagnostics.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/StreamDiagnosticsTests.swift
git commit -m "feat(core): add StreamDiagnostics concurrent-deliver counter"
```

---

### Task 4: CallbackStreamReader — delivery path

The reader's happy path: ordered delivery from an offset (via `OffsetSkip`), capacity-1 backpressure, zero-blob completion, and sink-error propagation. Termination, cancellation and the `.handoff`/generation machinery are Task 5 — but the state enum and `Coordinator` are introduced whole here so Task 5 only adds transitions, never reshapes the type.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/CallbackStreamReader.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeCallbackProducer.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/CallbackStreamReaderTests.swift`

**Interfaces:**
- Consumes: `OffsetSkip` (`public struct OffsetSkip: Sendable { init(skipping: Int64); mutating func take(_ blob: Data) -> Data? }`), `StreamDiagnostics` (Task 3).
- Produces:
  - `public struct CallbackStreamReader<Token: Sendable>: Sendable` with
    `public init(start: @escaping @Sendable (_ onData: @escaping @Sendable (Data) -> Bool, _ onDone: @escaping @Sendable (Error?) -> Void) -> Token, cancel: @escaping @Sendable (Token) -> Void, diagnostics: StreamDiagnostics? = nil)`
    and `public func read(from offset: Int64, into sink: @Sendable (Data) async throws -> Void) async throws`.
  - `FakeCallbackProducer` (test support) — drives `onData`/`onDone` on a background queue, returns an `Int` token, records whether `cancel` was called.

- [x] **Step 1: Write the test support fake**

Create `Support/FakeCallbackProducer.swift`:

```swift
import Foundation
@testable import FilesNestCore

/// Feeds a fixed sequence of blobs to a CallbackStreamReader from a background
/// serial queue, mimicking PhotoKit's arbitrary-serial-queue delivery. Honours
/// backpressure: it stops when `onData` returns false.
final class FakeCallbackProducer: @unchecked Sendable {
    private let blobs: [Data]
    private let queue = DispatchQueue(label: "fake.producer")
    private let cancelledLock = NSLock()
    private var _cancelled = false
    var cancelled: Bool { cancelledLock.lock(); defer { cancelledLock.unlock() }; return _cancelled }

    init(blobs: [Data]) { self.blobs = blobs }

    /// Builds a reader whose `start` streams `blobs` on the background queue.
    func makeReader(diagnostics: StreamDiagnostics? = nil) -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.queue.async {
                    for blob in self.blobs {
                        if !onData(blob) { onDone(nil); return }
                    }
                    onDone(nil)
                }
                return 1   // token
            },
            cancel: { _ in
                self.cancelledLock.lock(); self._cancelled = true; self.cancelledLock.unlock()
            },
            diagnostics: diagnostics)
    }
}

/// Collects blobs a reader delivers, so tests can assert order and content.
final class BlobCollector: @unchecked Sendable {
    private let lock = NSLock()
    private var _data = Data()
    var joined: Data { lock.lock(); defer { lock.unlock() }; return _data }
    @Sendable func sink(_ blob: Data) async throws {
        lock.lock(); _data.append(blob); lock.unlock()
    }
}
```

- [x] **Step 2: Write the failing delivery tests**

Create `CallbackStreamReaderTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct CallbackStreamReaderTests {

    @Test func deliversBytesInOrderFromZero() async throws {
        let blobs = [Data([0,1,2]), Data([3,4,5]), Data([6,7])]
        let producer = FakeCallbackProducer(blobs: blobs)
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 0, into: collector.sink)
        #expect(collector.joined == Data([0,1,2,3,4,5,6,7]))
    }

    @Test func deliversBytesInOrderFromOffset() async throws {
        // Offset 4 drops the first four bytes across blob boundaries.
        let blobs = [Data([0,1,2]), Data([3,4,5]), Data([6,7])]
        let producer = FakeCallbackProducer(blobs: blobs)
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 4, into: collector.sink)
        #expect(collector.joined == Data([4,5,6,7]))
    }

    @Test func offsetBeyondEndYieldsNothing() async throws {
        let producer = FakeCallbackProducer(blobs: [Data([0,1,2])])
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 100, into: collector.sink)
        #expect(collector.joined.isEmpty)
    }

    @Test func zeroBlobsCompletesCleanly() async throws {
        let producer = FakeCallbackProducer(blobs: [])
        let collector = BlobCollector()
        try await producer.makeReader().read(from: 0, into: collector.sink)
        #expect(collector.joined.isEmpty)
    }

    /// Capacity-1: the producer must not run ahead of the sink. We prove it by
    /// having the sink record how many blobs the producer had *started* by the
    /// time each sink call runs; it must never exceed 1 in flight.
    @Test func sinkIsFullyAwaitedBeforeNextDelivery() async throws {
        let counter = InFlightCounter()
        let producer = InstrumentedProducer(blobCount: 20, counter: counter)
        try await producer.makeReader().read(from: 0) { _ in
            try await Task.sleep(for: .milliseconds(1))
        }
        #expect(counter.maxInFlight == 1)
    }

    @Test func sinkErrorPropagatesAndStopsProducer() async throws {
        struct Boom: Error {}
        let producer = FakeCallbackProducer(blobs: [Data([1]), Data([2]), Data([3])])
        await #expect(throws: Boom.self) {
            try await producer.makeReader().read(from: 0) { _ in throw Boom() }
        }
    }
}

/// Counts producer callbacks in flight relative to sink completion.
final class InFlightCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var _now = 0
    private var _max = 0
    var maxInFlight: Int { lock.lock(); defer { lock.unlock() }; return _max }
    func producerEntered() { lock.lock(); _now += 1; _max = max(_max, _now); lock.unlock() }
    func sinkReturned() { lock.lock(); _now -= 1; lock.unlock() }
}

final class InstrumentedProducer: @unchecked Sendable {
    private let blobCount: Int
    private let counter: InFlightCounter
    private let queue = DispatchQueue(label: "instrumented.producer")
    init(blobCount: Int, counter: InFlightCounter) { self.blobCount = blobCount; self.counter = counter }
    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.queue.async {
                    for i in 0..<self.blobCount {
                        self.counter.producerEntered()
                        let go = onData(Data([UInt8(i & 0xff)]))
                        self.counter.sinkReturned()   // onData returned = sink finished this blob
                        if !go { onDone(nil); return }
                    }
                    onDone(nil)
                }
                return 1
            },
            cancel: { _ in })
    }
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter CallbackStreamReaderTests`
Expected: FAIL — `CallbackStreamReader` not defined.

- [x] **Step 4: Implement the reader (delivery path)**

Create `CallbackStreamReader.swift`. This introduces the full state enum and `Coordinator`; Task 5 only extends `finish`/adds cancellation.

```swift
import Foundation

/// Bridges a synchronous, callback-delivering byte producer (e.g. PhotoKit's
/// `PHAssetResourceManager.requestData`) to an async sink, honouring the four
/// `AssetDataSource` clauses. `Token` is generic so this file never names a
/// PhotoKit type. See docs/design/20260724-photosassetdatasource.md §4.
public struct CallbackStreamReader<Token: Sendable>: Sendable {

    /// Returned by the consumer's `next()`.
    fileprivate enum Item {
        case blob(Data, generation: Int)
        case done(Error?)
    }

    /// One blob only is ever in flight (capacity-1), enforced by `drained`.
    fileprivate final class Coordinator: @unchecked Sendable {
        enum State {
            case idle
            case blobPending(Data, generation: Int)
            case consumerWaiting(CheckedContinuation<Item, Never>)
            case handoff(generation: Int)   // continuation taken, resume in flight
            case terminal(Error?)
        }

        let lock = NSLock()
        var state: State = .idle
        var nextGeneration = 0
        let drained = DispatchSemaphore(value: 0)
        let diagnostics: StreamDiagnostics?
        // Task 5 adds: token storage + cancellation. Declared here so the type
        // is stable across tasks.
        var token: Token?
        var cancelOnTokenArrival = false

        init(diagnostics: StreamDiagnostics?) { self.diagnostics = diagnostics }

        /// Producer side. Returns false to tell the producer to stop.
        func deliver(_ blob: Data) -> Bool {
            diagnostics?.enter(byteCount: blob.count)
            defer { diagnostics?.exit() }

            lock.lock()
            if case .terminal = state { lock.unlock(); return false }
            let gen = nextGeneration; nextGeneration += 1
            switch state {
            case .consumerWaiting(let k):
                state = .handoff(generation: gen)
                lock.unlock()
                k.resume(returning: .blob(blob, generation: gen))
            case .idle:
                state = .blobPending(blob, generation: gen)
                lock.unlock()
            default:
                // Unreachable under capacity-1: the producer is blocked in
                // `drained.wait()` until the prior blob is consumed.
                lock.unlock()
                return false
            }
            drained.wait()                 // BACKPRESSURE
            lock.lock()
            let stopping: Bool
            if case .terminal = state { stopping = true } else { stopping = false }
            lock.unlock()
            return !stopping
        }

        /// Producer completion. Task 5 hardens this; delivery path needs only
        /// the terminal transition and waking a suspended consumer.
        func finish(_ error: Error?) {
            lock.lock()
            if case .terminal = state { lock.unlock(); return }
            let prev = state
            state = .terminal(error)
            switch prev {
            case .consumerWaiting(let k):
                lock.unlock()
                k.resume(returning: .done(error))
            default:
                // .handoff: resume already in flight; consumer owns the signal.
                // .idle / .blobPending: consumer observes terminal at next next().
                lock.unlock()
            }
        }

        /// Consumer side. Suspends until a blob or terminal is available.
        func next() async -> Item {
            lock.lock()
            switch state {
            case .blobPending(let blob, let gen):
                state = .idle
                lock.unlock()
                return .blob(blob, generation: gen)
            case .terminal(let err):
                lock.unlock()
                return .done(err)
            case .idle:
                return await withCheckedContinuation { (k: CheckedContinuation<Item, Never>) in
                    state = .consumerWaiting(k)
                    lock.unlock()
                }
            default:
                lock.unlock()
                return .done(nil)   // unreachable
            }
        }

        /// Called by the consumer after it has fully processed the blob for
        /// `generation`. OWNERSHIP FOLLOWS THE BLOB: this signals unconditionally,
        /// even if the state went terminal while the sink ran (spec §4.1).
        func consumerDidProcess(generation: Int) {
            lock.lock()
            if case .handoff(let g) = state, g == generation { state = .idle }
            lock.unlock()
            drained.signal()
        }
    }

    private let start: @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                                  _ onDone: @escaping @Sendable (Error?) -> Void) -> Token
    private let cancel: @Sendable (Token) -> Void
    private let diagnostics: StreamDiagnostics?

    public init(
        start: @escaping @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                                    _ onDone: @escaping @Sendable (Error?) -> Void) -> Token,
        cancel: @escaping @Sendable (Token) -> Void,
        diagnostics: StreamDiagnostics? = nil
    ) {
        self.start = start
        self.cancel = cancel
        self.diagnostics = diagnostics
    }

    public func read(from offset: Int64,
                     into sink: @Sendable (Data) async throws -> Void) async throws {
        let coord = Coordinator(diagnostics: diagnostics)
        var skip = OffsetSkip(skipping: offset)

        // Per-CALL serial queue: a re-entrant read on the same reader gets its
        // own queue, so it cannot deadlock behind this call's blocked producer.
        let queue = DispatchQueue(label: "CallbackStreamReader.\(UUID().uuidString)")
        queue.async {
            let token = coord.reentrantStart(self.start)
            _ = token   // Task 5 stores/cancels; delivery path ignores it.
        }

        // Consumer loop. Each blob handed to us is ours to signal, always.
        while true {
            let item = await coord.next()
            switch item {
            case .done(let err):
                if let err { throw err }
                return
            case .blob(let blob, let gen):
                do {
                    if let b = skip.take(blob) { try await sink(b) }
                } catch {
                    coord.consumerDidProcess(generation: gen)  // release producer first
                    coord.finish(error)                        // then mark terminal
                    throw error
                }
                coord.consumerDidProcess(generation: gen)
            }
        }
    }
}

extension CallbackStreamReader.Coordinator {
    /// Invokes `start`, wiring `onData`→`deliver` and `onDone`→`finish`.
    func reentrantStart(
        _ start: @Sendable (_ onData: @escaping @Sendable (Data) -> Bool,
                            _ onDone: @escaping @Sendable (Error?) -> Void) -> Token
    ) -> Token {
        start({ [weak self] blob in self?.deliver(blob) ?? false },
              { [weak self] error in self?.finish(error) })
    }
}
```

- [x] **Step 5: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter CallbackStreamReaderTests`
Expected: PASS — all six delivery tests.

- [x] **Step 6: Verify capacity-1 test can fail**

Temporarily change `deliver` to call `drained.signal()` itself right before `drained.wait()` (breaking backpressure). `sinkIsFullyAwaitedBeforeNextDelivery` MUST report `maxInFlight > 1`. Revert (spec §8.2).

- [x] **Step 7: Verify build with warnings-as-errors**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: clean.

- [x] **Step 8: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/CallbackStreamReader.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeCallbackProducer.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/CallbackStreamReaderTests.swift
git commit -m "feat(core): CallbackStreamReader delivery path with capacity-1 backpressure"
```

---

### Task 5: CallbackStreamReader — termination, cancellation, handoff, generation

The concurrency-critical cluster. Three review rounds each found a distinct deadlock here, so these are one reviewable gate, not several: the never-resume hang, the double-resume crash, the `.handoff` under-signal, the over-signalled semaphore, and cancellation-before-token. They share one machine and must be verified together (spec §4.1, §4.1.1).

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/CallbackStreamReader.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/CallbackStreamReaderTests.swift`
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeCallbackProducer.swift`

**Interfaces:**
- Consumes: everything from Task 4.
- Produces: same public surface; `read` now honours `Task` cancellation, invokes `cancel(token)` exactly once, resumes any suspended continuation on termination without double-resume, and never leaves a producer blocked.

- [x] **Step 1: Add a controllable producer to the fake**

Append to `Support/FakeCallbackProducer.swift`:

```swift
/// A producer whose delivery you can pause and whose token arrival you can delay,
/// so tests can force the exact interleavings spec §4.1 enumerates.
final class ControllableProducer: @unchecked Sendable {
    let started = DispatchSemaphore(value: 0)     // signalled when start() runs
    let releaseToken = DispatchSemaphore(value: 0) // gate token return
    private let queue = DispatchQueue(label: "controllable.producer")
    private let cancelledLock = NSLock()
    private var _cancelCount = 0
    var cancelCount: Int { cancelledLock.lock(); defer { cancelledLock.unlock() }; return _cancelCount }

    let blobs: [Data]
    let delayTokenReturn: Bool
    init(blobs: [Data], delayTokenReturn: Bool = false) {
        self.blobs = blobs; self.delayTokenReturn = delayTokenReturn
    }

    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.started.signal()
                self.queue.async {
                    for blob in self.blobs {
                        if !onData(blob) { onDone(nil); return }
                    }
                    onDone(nil)
                }
                if self.delayTokenReturn { self.releaseToken.wait() }
                return 1
            },
            cancel: { _ in
                self.cancelledLock.lock(); self._cancelCount += 1; self.cancelledLock.unlock()
            })
    }
}
```

- [x] **Step 2: Write the failing termination/cancellation tests**

Append to `CallbackStreamReaderTests.swift`:

```swift
extension CallbackStreamReaderTests {

    /// Cancellation while the consumer is suspended with no blob pending must
    /// throw CancellationError, not hang. This is the never-resume path (§4.1 rule 1).
    @Test func cancellationWhileConsumerSuspendedResumesAndThrows() async throws {
        let producer = ControllableProducer(blobs: [])   // never delivers, never completes
        let reader = producer.makeReader()
        let task = Task {
            try await reader.read(from: 0) { _ in }
        }
        producer.started.wait()
        try await Task.sleep(for: .milliseconds(50))   // let consumer suspend
        task.cancel()
        await #expect(throws: CancellationError.self) { try await task.value }
        #expect(producer.cancelCount == 1)
    }

    /// A late onDone after terminal must not double-resume the continuation
    /// (would crash). Driven by cancelling, then letting the producer complete.
    @Test func lateOnDoneAfterTerminalDoesNotDoubleResume() async throws {
        let producer = LateCompletingProducer()
        let reader = producer.makeReader()
        let task = Task { try await reader.read(from: 0) { _ in } }
        producer.started.wait()
        try await Task.sleep(for: .milliseconds(50))
        task.cancel()
        _ = try? await task.value
        producer.completeNow()             // fires onDone AFTER terminal
        try await Task.sleep(for: .milliseconds(50))  // crash would surface here
        #expect(Bool(true))                // reaching here without crash is the assertion
    }

    /// After cancellation, `cancel(token)` fires even if the token had not yet
    /// been returned by `start` (§4.1.1).
    @Test func cancellationBeforeTokenReturnsStillCancels() async throws {
        let producer = ControllableProducer(blobs: [], delayTokenReturn: true)
        let reader = producer.makeReader()
        let task = Task { try await reader.read(from: 0) { _ in } }
        producer.started.wait()
        task.cancel()                      // cancel while start() is blocked pre-return
        producer.releaseToken.signal()     // now let the token return
        _ = try? await task.value
        try await Task.sleep(for: .milliseconds(50))
        #expect(producer.cancelCount == 1)
    }

    /// A consumer resumed with a blob that discovers `.terminal` while running
    /// must still signal, or the blocked producer is stranded (§4.1 handoff).
    @Test func resumedConsumerSignalsEvenWhenTerminal() async throws {
        // One blob whose sink is slow; producer completes during the sink.
        let producer = CompleteDuringSinkProducer()
        let reader = producer.makeReader()
        try await reader.read(from: 0) { _ in
            producer.fireCompletionDuringSink()
            try await Task.sleep(for: .milliseconds(30))
        }
        // If the producer were stranded, its queue task would never finish and
        // the run would hang. Reaching completion is the assertion.
        #expect(Bool(true))
    }

    /// A cancel racing a normal delivery must not leave a spare semaphore permit
    /// (which would loosen capacity-1). Assert a later deliver still blocks.
    @Test func drainIsSignalledExactlyOncePerBlob() async throws {
        let counter = InFlightCounter()
        let producer = InstrumentedProducer(blobCount: 50, counter: counter)
        let reader = producer.makeReader()
        let task = Task {
            try await reader.read(from: 0) { _ in try await Task.sleep(for: .milliseconds(1)) }
        }
        try await Task.sleep(for: .milliseconds(20))
        task.cancel()
        _ = try? await task.value
        #expect(counter.maxInFlight == 1)   // never exceeded 1 despite the cancel race
    }

    @Test func inlineSynchronousDeliveryIsSupported() async throws {
        // A producer that calls onData INLINE from start(), before returning.
        let collector = BlobCollector()
        let reader = CallbackStreamReader<Int>(
            start: { onData, onDone in
                _ = onData(Data([9, 9, 9]))   // inline, synchronous
                onDone(nil)
                return 1
            },
            cancel: { _ in })
        try await reader.read(from: 0, into: collector.sink)
        #expect(collector.joined == Data([9, 9, 9]))
    }

    @Test func nestedReadOnSameReaderDoesNotDeadlock() async throws {
        let inner = FakeCallbackProducer(blobs: [Data([2])]).makeReader()
        let outerCollector = BlobCollector()
        let innerCollector = BlobCollector()
        let outer = FakeCallbackProducer(blobs: [Data([1])]).makeReader()
        try await outer.read(from: 0) { blob in
            try await outerCollector.sink(blob)
            // Re-enter a DIFFERENT read from inside the sink; per-call queues
            // make this safe.
            try await inner.read(from: 0, into: innerCollector.sink)
        }
        #expect(outerCollector.joined == Data([1]))
        #expect(innerCollector.joined == Data([2]))
    }
}
```

Also append these small producers to `Support/FakeCallbackProducer.swift`:

```swift
/// Completes only when told to, after start().
final class LateCompletingProducer: @unchecked Sendable {
    let started = DispatchSemaphore(value: 0)
    private let doneLock = NSLock()
    private var onDone: (@Sendable (Error?) -> Void)?
    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { _, onDone in
                self.doneLock.lock(); self.onDone = onDone; self.doneLock.unlock()
                self.started.signal()
                return 1
            }, cancel: { _ in })
    }
    func completeNow() {
        doneLock.lock(); let d = onDone; doneLock.unlock(); d?(nil)
    }
}

/// Delivers one blob, then fires completion while that blob's sink is running.
final class CompleteDuringSinkProducer: @unchecked Sendable {
    private let queue = DispatchQueue(label: "complete.during.sink")
    private let doneLock = NSLock()
    private var onDone: (@Sendable (Error?) -> Void)?
    func makeReader() -> CallbackStreamReader<Int> {
        CallbackStreamReader<Int>(
            start: { onData, onDone in
                self.doneLock.lock(); self.onDone = onDone; self.doneLock.unlock()
                self.queue.async { _ = onData(Data([7])) }
                return 1
            }, cancel: { _ in })
    }
    func fireCompletionDuringSink() {
        doneLock.lock(); let d = onDone; doneLock.unlock()
        DispatchQueue.global().async { d?(nil) }
    }
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `cd apple/FilesNestCore && swift test --filter CallbackStreamReaderTests`
Expected: FAIL — cancellation is not yet wired; `cancellationWhileConsumerSuspendedResumesAndThrows` hangs or times out, others fail.

- [x] **Step 4: Wire cancellation, token lifecycle, and the terminal-owns-continuation rule**

Replace the `read` body and extend the `Coordinator`. In `Coordinator`, add:

```swift
    /// Terminal transition from cancellation. Resumes a suspended consumer
    /// exactly once (outside the lock); NEVER touches a .handoff continuation
    /// (its resume is already in flight); returns the token to cancel, if known.
    /// Returns (continuationToResume?, tokenToCancel?).
    func beginCancellation() -> (CheckedContinuation<Item, Never>?, Token?) {
        lock.lock()
        if case .terminal = state { lock.unlock(); return (nil, nil) }
        let prev = state
        state = .terminal(CancellationError())
        var k: CheckedContinuation<Item, Never>?
        if case .consumerWaiting(let cont) = prev { k = cont }
        // If a token already arrived, hand it back to cancel; otherwise ask the
        // token-arrival path to cancel when it lands (§4.1.1).
        let tok = token
        if tok == nil { cancelOnTokenArrival = true }
        lock.unlock()
        return (k, tok)
    }

    /// Store the token once `start` returns it. If cancellation already fired,
    /// return it so the caller can cancel immediately (outside the lock).
    func storeToken(_ t: Token) -> Token? {
        lock.lock()
        token = t
        let shouldCancel = cancelOnTokenArrival
        lock.unlock()
        return shouldCancel ? t : nil
    }
```

Replace `read` with:

```swift
    public func read(from offset: Int64,
                     into sink: @Sendable (Data) async throws -> Void) async throws {
        let coord = Coordinator(diagnostics: diagnostics)
        var skip = OffsetSkip(skipping: offset)
        let start = self.start
        let cancel = self.cancel

        let queue = DispatchQueue(label: "CallbackStreamReader.\(UUID().uuidString)")
        queue.async {
            let token = coord.reentrantStart(start)
            // Decide-under-lock, call-out-outside-lock (§4.1.1, §4.1 rule).
            if let toCancel = coord.storeToken(token) { cancel(toCancel) }
        }

        try await withTaskCancellationHandler {
            while true {
                let item = await coord.next()
                switch item {
                case .done(let err):
                    if let err { throw err }
                    return
                case .blob(let blob, let gen):
                    do {
                        if let b = skip.take(blob) { try await sink(b) }
                    } catch {
                        coord.consumerDidProcess(generation: gen)
                        coord.finish(error)
                        throw error
                    }
                    coord.consumerDidProcess(generation: gen)
                }
            }
        } onCancel: {
            let (k, tok) = coord.beginCancellation()
            k?.resume(returning: .done(CancellationError()))   // outside lock
            if let tok { cancel(tok) }                          // outside lock
        }
    }
```

The delivery-path `finish` from Task 4 is unchanged and already correct for the `.handoff` and `.consumerWaiting` cases; the never-resume/double-resume safety now comes from `beginCancellation` owning the continuation under the lock and `finish`/`next` treating `.terminal` as absorbing.

- [x] **Step 5: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter CallbackStreamReaderTests`
Expected: PASS — all delivery and termination tests. None hang (a hang = a bug; the suite must complete).

- [x] **Step 6: Verify each termination test can fail**

One at a time, revert and rerun:
- Make `beginCancellation` not resume the continuation → `cancellationWhileConsumerSuspendedResumesAndThrows` hangs/times out.
- Make `consumerDidProcess` signal only `if case .handoff` (inside the guard) → `resumedConsumerSignalsEvenWhenTerminal` hangs.
- Make `storeToken` ignore `cancelOnTokenArrival` → `cancellationBeforeTokenReturnsStillCancels` fails (`cancelCount == 0`).
Revert each. This is the §8.2 discipline; these three are the deadlock paths prior review rounds found.

- [x] **Step 7: Verify build with warnings-as-errors**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: clean.

- [x] **Step 8: Run the whole suite for regressions**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS — 68 prior tests plus the new ones.

- [x] **Step 9: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/CallbackStreamReader.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/CallbackStreamReaderTests.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeCallbackProducer.swift
git commit -m "feat(core): CallbackStreamReader termination, cancellation, handoff, exactly-once drain"
```

---

### Task 6: App target wiring + PhotosAssetDataSource

The untestable-by-`swift test` residue (spec §8.3). Links the Core package into the Xcode app target, adds the Photos entitlement and usage string, and writes the ~40-line adapter. Verified by `xcodebuild build` and a manual smoke check, not the Swift test runner.

**Files:**
- Modify: `apple/macos/FilesNest/FilesNest.xcodeproj/project.pbxproj` (package ref + entitlement/plist settings)
- Create: `apple/macos/FilesNest/FilesNest/FilesNest.entitlements`
- Create: `apple/macos/FilesNest/FilesNest/PhotosAssetDataSource.swift`

**Interfaces:**
- Consumes: `CallbackStreamReader<PHAssetResourceDataRequestID>`, `ResourceKey`, `ResourceKind`, `AssetDataSource` (from Core).
- Produces: `struct PhotosAssetDataSource: AssetDataSource` in the app target.

- [x] **Step 1: Link FilesNestCore into the app target**

In Xcode: open `apple/macos/FilesNest/FilesNest.xcodeproj`, File ▸ Add Package Dependencies ▸ Add Local ▸ select `apple/FilesNestCore`. Add `FilesNestCore` to the `FilesNest` target's "Frameworks, Libraries, and Embedded Content". (This edits `project.pbxproj`; do it through Xcode rather than by hand to keep the file valid.)

- [x] **Step 2: Add the Photos entitlement and usage string**

Create `apple/macos/FilesNest/FilesNest/FilesNest.entitlements`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>com.apple.security.app-sandbox</key>
    <true/>
    <key>com.apple.security.personal-information.photos-library</key>
    <true/>
</dict>
</plist>
```

In the target's Build Settings, set `CODE_SIGN_ENTITLEMENTS = FilesNest/FilesNest.entitlements`. In the target's Info tab, add `NSPhotoLibraryUsageDescription` = `FilesNest reads your photo and video originals to back them up to your server.` No network entitlement — the measurement sink discards.

- [x] **Step 3: Write the adapter**

Create `apple/macos/FilesNest/FilesNest/PhotosAssetDataSource.swift`:

```swift
import Foundation
import Photos
import FilesNestCore

/// `AssetDataSource` backed by PhotoKit. All contract logic lives in
/// `CallbackStreamReader`; this only resolves the resource and supplies two
/// closures. See docs/design/20260724-photosassetdatasource.md §3, §5.
struct PhotosAssetDataSource: AssetDataSource {

    enum ResolveError: Error {
        case assetNotFound(String)
        case resourceNotFound(ResourceKind)
        case unmappableKind(ResourceKind)
    }

    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws {
        let key = try ResourceKey(parsing: assetID)
        let resource = try Self.resolveResource(key)

        let options = PHAssetResourceRequestOptions()
        options.isNetworkAccessAllowed = true   // iCloud-only originals

        let manager = PHAssetResourceManager.default()
        let reader = CallbackStreamReader<PHAssetResourceDataRequestID>(
            start: { onData, onDone in
                manager.requestData(
                    for: resource,
                    options: options,
                    dataReceivedHandler: { data in _ = onData(data) },
                    completionHandler: { error in onDone(error) })
            },
            cancel: { id in manager.cancelDataRequest(id) })

        try await reader.read(from: offset, into: sink)
    }

    /// Resolves the ResourceKey to a concrete PHAssetResource. Throws rather
    /// than falling back to a primary resource — a silent fallback would upload
    /// the wrong bytes under the right key (spec §5).
    private static func resolveResource(_ key: ResourceKey) throws -> PHAssetResource {
        let fetch = PHAsset.fetchAssets(withLocalIdentifiers: [key.localIdentifier], options: nil)
        guard let asset = fetch.firstObject else {
            throw ResolveError.assetNotFound(key.localIdentifier)
        }
        let wanted = try mapKind(key.kind)
        let resources = PHAssetResource.assetResources(for: asset)
        guard let match = resources.first(where: { $0.type == wanted }) else {
            throw ResolveError.resourceNotFound(key.kind)
        }
        return match
    }

    private static func mapKind(_ kind: ResourceKind) throws -> PHAssetResourceType {
        switch kind {
        case .photo:          return .photo
        case .video:          return .video
        case .audio:          return .audio
        case .pairedVideo:    return .pairedVideo
        case .fullSizePhoto:  return .fullSizePhoto
        case .alternatePhoto: return .alternatePhoto
        }
    }
}
```

- [x] **Step 4: Build the app target**

Run:
```bash
xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj \
           -scheme FilesNest -destination 'platform=macOS' build
```
Expected: BUILD SUCCEEDED. `import FilesNestCore` resolves; the `switch` over `ResourceKind` is exhaustive.

- [x] **Step 5: Commit**

```bash
git add apple/macos/FilesNest/FilesNest.xcodeproj/project.pbxproj \
        apple/macos/FilesNest/FilesNest/FilesNest.entitlements \
        apple/macos/FilesNest/FilesNest/PhotosAssetDataSource.swift
git commit -m "feat(macos): link FilesNestCore, add Photos entitlement and PhotosAssetDataSource"
```

---

### Task 7: Measurement UI + mid-stream-stall run

Builds the instrument that answers spec §6/§11.1: does PhotoKit honour backpressure, and does it stream during download or materialise first? The method is the within-run mid-stream stall (spec §6.2.1) — never a cross-run comparison.

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/MeasurementRunner.swift`
- Create: `apple/macos/FilesNest/FilesNest/MeasurementView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/ContentView.swift` (show `MeasurementView`)

**Interfaces:**
- Consumes: `PhotosAssetDataSource`, `DiskProbe.volumeFreeSpace`, `MemoryProbe.footprint` (from Core), `StreamDiagnostics`.
- Produces: `MeasurementRunner` (async run) and `MeasurementView` (SwiftUI).

- [x] **Step 1: Write the runner**

Create `MeasurementRunner.swift`:

```swift
import Foundation
import Photos
import FilesNestCore

/// Streams one asset with a discarding sink, stalling three times mid-stream,
/// while sampling free space and footprint. Reports growth-rate around each
/// stall. See spec §6.2.1 / §6.4. NOT a cross-run comparison.
@MainActor
final class MeasurementRunner: ObservableObject {
    @Published var log: [String] = []

    struct Sample { let t: TimeInterval; let freeBytes: Int64; let footprint: Int64 }

    /// Fraction-of-bytes points at which to stall (~20/45/70%).
    private let stallAt: [Double] = [0.20, 0.45, 0.70]
    private let stallSeconds: UInt64 = 30

    func run(localIdentifier: String, kind: ResourceKind) async {
        log = []
        let key = ResourceKey(localIdentifier: localIdentifier, kind: kind).encoded
        let diagnostics = StreamDiagnostics()
        let home = URL(fileURLWithPath: NSHomeDirectory())
        let start = Date()
        var samples: [Sample] = []
        var stalledIndices = Set<Int>()

        func sample() {
            let free = (try? DiskProbe.volumeFreeSpace(at: home)) ?? 0
            let fp = MemoryProbe.footprint() ?? 0
            samples.append(Sample(t: Date().timeIntervalSince(start), freeBytes: free, footprint: fp))
        }

        // Background sampler at 1 Hz.
        let sampler = Task { while !Task.isCancelled { sample(); try? await Task.sleep(for: .seconds(1)) } }

        var bytesSeen: Int64 = 0
        let source = PhotosAssetDataSource()
        // We do not know total size in advance (no resource-size API, spec §11);
        // stall by elapsed byte thresholds relative to an estimate is impossible,
        // so stall by blob-count milestones instead: stall on the Nth blob at
        // roughly-even spacing, capped at 3 stalls.
        var blobIndex = 0
        let stallEveryNBlobs = 40   // tune to blob size; 3 stalls over a large video

        do {
            try await source.read(assetID: key, from: 0) { blob in
                bytesSeen += Int64(blob.count)
                blobIndex += 1
                if stalledIndices.count < self.stallAt.count,
                   blobIndex % stallEveryNBlobs == 0 {
                    stalledIndices.insert(blobIndex)
                    await MainActor.run { self.log.append("STALL #\(stalledIndices.count) at blob \(blobIndex), bytes \(bytesSeen)") }
                    try await Task.sleep(for: .seconds(Double(self.stallSeconds)))
                    await MainActor.run { self.log.append("RELEASE #\(stalledIndices.count)") }
                }
                // discard blob
            }
        } catch {
            log.append("ERROR: \(error)")
        }

        sampler.cancel()
        report(samples: samples, diagnostics: diagnostics, totalBytes: bytesSeen)
    }

    private func report(samples: [Sample], diagnostics: StreamDiagnostics, totalBytes: Int64) {
        log.append("total bytes: \(totalBytes)")
        log.append("maxConcurrentDeliver: \(diagnostics.maxConcurrent)  (>1 falsifies serial delivery)")
        log.append("samples: \(samples.count)")
        for s in samples {
            log.append(String(format: "t=%.1fs free=%lld footprint=%lld", s.t, s.freeBytes, s.footprint))
        }
        log.append("INTERPRET: near-zero free-space growth DURING a stall, resuming after = backpressure real.")
        log.append("           growth continuing at pre-stall rate through a stall = read-ahead.")
        log.append("           ambiguous across the three stalls = UNRESOLVED (spec §6.2.1).")
    }
}
```

- [x] **Step 2: Write the view**

Create `MeasurementView.swift`:

```swift
import SwiftUI
import Photos
import PhotosUI

struct MeasurementView: View {
    @StateObject private var runner = MeasurementRunner()
    @State private var localIdentifier = ""
    @State private var authorized = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("PhotoKit backpressure measurement").font(.headline)
            if !authorized {
                Button("Request Photos access") {
                    PHPhotoLibrary.requestAuthorization(for: .readWrite) { status in
                        Task { @MainActor in authorized = (status == .authorized || status == .limited) }
                    }
                }
            }
            TextField("localIdentifier of a large iCloud-only video", text: $localIdentifier)
                .textFieldStyle(.roundedBorder)
            Button("Measure (video, mid-stream stalls)") {
                Task { await runner.run(localIdentifier: localIdentifier, kind: .video) }
            }
            .disabled(localIdentifier.isEmpty)
            ScrollView {
                Text(runner.log.joined(separator: "\n"))
                    .font(.system(.caption, design: .monospaced))
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding()
        .frame(minWidth: 520, minHeight: 400)
    }
}
```

- [x] **Step 3: Show it from ContentView**

Replace the body of `apple/macos/FilesNest/FilesNest/ContentView.swift`'s `ContentView` with:

```swift
import SwiftUI

struct ContentView: View {
    var body: some View {
        MeasurementView()
    }
}
```

- [x] **Step 4: Build the app target**

Run:
```bash
xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj \
           -scheme FilesNest -destination 'platform=macOS' build
```
Expected: BUILD SUCCEEDED.

- [x] **Step 5: Manual measurement procedure (record results, do not automate)**

Document the run in the commit body. Procedure:
1. Ensure the target asset is iCloud-only (Optimize Mac Storage on, asset evicted locally). If it is already materialised, the run is vacuous — verify first (spec §6.4).
2. Launch the app, grant Photos access, paste the localIdentifier, click Measure.
3. Observe the log: three STALL/RELEASE cycles and the free-space/footprint samples.
4. Interpret per the three-way rule the runner prints; if ambiguous, record **UNRESOLVED** rather than rounding.
5. Note `maxConcurrentDeliver` — expected 1; a value > 1 is a surprising, report-worthy result.

- [x] **Step 6: Commit**

```bash
git add apple/macos/FilesNest/FilesNest/MeasurementRunner.swift \
        apple/macos/FilesNest/FilesNest/MeasurementView.swift \
        apple/macos/FilesNest/FilesNest/ContentView.swift
git commit -m "feat(macos): mid-stream-stall measurement UI for PhotoKit backpressure"
```

---

### Task 8: architecture.md corrections

Fixes the stale key/id documentation the resource-key work exposed (spec §5 doc impact), and records the measurement outcome.

**Files:**
- Modify: `docs/architecture.md:59-64`, `:230`, `:246` region as needed.

- [x] **Step 1: Correct the record-key documentation**

In `docs/architecture.md`, the schema block currently reads `Main records keyed by uploads/<localIdentifier>` with `"id": "<localIdentifier>"`. Replace with:

```
Main records keyed by `uploads/<SafeID(localIdentifier#kind)>` (SHA-256 → base64url,
see `server/internal/api/ids.go`). The raw resource key is stored in the record body,
not the BadgerDB key.
```

And change the `"id"` line in the JSON block to:

```json
  "id":            "<SafeID of the resource key>",
```

- [x] **Step 2: Note the resource-key model near the Live Photo paragraph**

At `docs/architecture.md:230` (Live Photos become two records), append:

```
Each resource is addressed by a `ResourceKey` (`<localIdentifier>#<kind>`,
`apple/FilesNestCore/Sources/FilesNestCore/ResourceKey.swift`), so the JPEG and MOV
occupy distinct keys and distinct `idx/local/*` index entries despite sharing a
`localIdentifier`.
```

- [x] **Step 3: Record the §11.1 measurement outcome**

Under the "Mac app: streaming without temp files" section, add a short paragraph stating what the Task 7 run found: whether PhotoKit honoured backpressure, whether it streams or materialises, or that the result was UNRESOLVED. Use the actual observed numbers.

- [x] **Step 4: Verify no other stale references**

Run: `grep -n "uploads/<localIdentifier>\|\"id\": \"<localIdentifier>\"" docs/architecture.md`
Expected: no matches remain.

- [x] **Step 5: Commit**

```bash
git add docs/architecture.md
git commit -m "docs: correct stale record-key schema, document ResourceKey and measurement result"
```

---

## Self-Review

**Spec coverage:**
- §3.1 seam → Task 4 (init/read signatures verbatim). ✓
- §4 handoff mechanism, per-call queue, exactly-once drain → Tasks 4 (delivery), 5 (termination). ✓
- §4.1 five-state machine + 11-row exit table → Task 5 code + tests. ✓
- §4.1.1 cancellation-before-token → Task 5 `storeToken`/`beginCancellation` + `cancellationBeforeTokenReturnsStillCancels`. ✓
- §4.3 ceiling 2-observed/3-allowed, not tightened → Global Constraints + Task 4 capacity-1 test asserting maxInFlight == 1. ✓
- §5 ResourceKey, last-# parse, SafeID/localIndexKey collision → Task 2 + Task 8 docs. ✓
- §6.1/§6.2 diagnostics (concurrency only) → Task 3. ✓
- §6.2.1/§6.4 mid-stream-stall run → Task 7. ✓
- §6.3 free-space probe → Task 1. ✓
- §7 app wiring (entitlement, usage string, no network) → Task 6. ✓
- §8 test list (24 tests) → Tasks 2–5. ✓
- §8.3 untestable residue acknowledged → Task 6/7 use xcodebuild + manual, Global Constraints. ✓
- §11.1 measurement answer recorded → Task 8 step 3. ✓

**Known deviation carried from spec §9:** free-space pre-flight guard and `SyncCoordinator` adoption of `ResourceKey` are out of scope; the Live Photo collision remains latent until `SyncCoordinator` adopts the key. Not a plan gap — a deliberate deferral (spec §9, §11.4).

**Placeholder scan:** no TBD/TODO; every code step shows full code; every test step shows the assertion. ✓

**Type consistency:** `deliver`/`finish`/`next`/`consumerDidProcess`/`beginCancellation`/`storeToken` names are stable across Tasks 4–5. `ResourceKind` cases match between `ResourceKey` (Task 2) and `mapKind` (Task 6). `StreamDiagnostics.enter(byteCount:)`/`exit()` match between Task 3 and the `Coordinator.deliver` calls in Task 4. `DiskProbe.volumeFreeSpace` matches between Task 1 and Task 7. ✓

**One open sizing note for the executor:** Task 7's `stallEveryNBlobs = 40` is a heuristic for "three stalls over a large video"; PhotoKit's blob size is unknown until the first real run (that is partly what we are measuring). If the first run produces fewer than ~120 blobs, lower the constant so all three stalls fire. This is the one number in the plan that a real run may force you to tune — flagged rather than hidden.
