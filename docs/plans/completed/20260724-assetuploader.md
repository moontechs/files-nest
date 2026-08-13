# AssetUploader + Memory Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upload a single Photos asset's bytes to the FilesNest server via TUS with peak memory independent of asset size and exactly one pass over the data.

**Architecture:** A protocol seam (`AssetDataSource`) keeps `FilesNestCore` pure Foundation; the Photos-backed conformance lands in a later slice. Backpressure is structural — the source awaits an async sink per blob, so at most one blob is in flight. `AssetUploader` PATCHes blobs straight through with no accumulation buffer, holding exactly one blob back so the final PATCH can declare `Upload-Length`. Look-ahead state lives in a private actor because Swift 6 rejects mutable capture in `@Sendable` closures.

**Tech Stack:** Swift 6 / SwiftPM, Swift Testing, Foundation, Mach `task_vm_info`, Go (tusd) for one server-side test.

**Spec:** `docs/design/20260724-assetuploader.md`

## Global Constraints

- `FilesNestCore` platform floor: **macOS 13, iOS 17**. `Synchronization.Mutex` (macOS 15+) is unavailable.
- **Swift 6 language mode, complete strict concurrency.** `swift-tools-version: 6.0` with no `swiftLanguageModes` override.
- `FilesNestCore` is **pure Foundation**. Never `import Photos` in `Sources/`.
- Tests touching `MockURLProtocol` MUST live in a `@Suite(.serialized)` — Swift Testing parallelises by default and `MockURLProtocol.handler` is shared static state.
- Tests using `MemoryProbe` MUST **also** live in a `@Suite(.serialized)`. `phys_footprint` is process-wide, so parallel tests contaminate each other's readings. Confirmed in Task 1: a trivial-work test measured 134 MB of growth because a sibling test allocated 128 MB concurrently. Note `.serialized` orders tests only *within* a suite — other suites still run in parallel, which Task 8 must re-verify once a 3 GB-allocating suite exists.
- Verification for every task:
  - `cd apple/FilesNestCore && swift test`
  - `swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
- Test fakes live in the test target, never in `Sources/`.
- **Do not commit or push until the user explicitly says go.** Commit steps below are written per green task, but wait for that instruction.

---

### Task 1: MemoryProbe

The gate instrument. Built before any streaming code, because a probe that silently returns zero would make every later assertion vacuous.

**Files:**
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/MemoryProbe.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/MemoryProbeTests.swift`

**Interfaces:**
- Consumes: nothing
- Produces: `enum MemoryProbe` with `static func footprint() -> Int64?` and `static func peakGrowth(sampleInterval:during:) async throws -> Int64`

- [x] **Step 1: Write the failing test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/MemoryProbeTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct MemoryProbeTests {

    @Test func footprintReturnsPositiveValue() throws {
        let f = try #require(MemoryProbe.footprint())
        #expect(f > 0)
    }

    /// The probe must actually SEE an allocation. A probe that always reports
    /// ~0 would make every later memory assertion pass vacuously.
    @Test func peakGrowthDetectsALargeAllocation() async throws {
        let growth = try await MemoryProbe.peakGrowth {
            var block = Data(count: 128 * 1024 * 1024)
            block[0] = 1              // force the pages to be touched
            block[block.count - 1] = 1
            _ = block.count
            try await Task.sleep(for: .milliseconds(50))
        }
        #expect(growth > 100 * 1024 * 1024)
    }

    @Test func peakGrowthIsSmallForTrivialWork() async throws {
        let growth = try await MemoryProbe.peakGrowth {
            try await Task.sleep(for: .milliseconds(50))
        }
        #expect(growth < 16 * 1024 * 1024)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter MemoryProbeTests`
Expected: FAIL — `cannot find 'MemoryProbe' in scope`

- [x] **Step 3: Write minimal implementation**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/MemoryProbe.swift`:

```swift
import Foundation
#if canImport(Darwin)
import Darwin
#endif

/// Samples the process's resident physical footprint.
///
/// Limitations, stated so results are not over-read:
/// - `phys_footprint` is PROCESS-WIDE; test-runner allocations count toward it.
/// - Sampling can miss a spike between samples.
/// - This detects LINEAR GROWTH. It is not a leak detector.
enum MemoryProbe {

    /// Current `phys_footprint` in bytes, or nil if the kernel call fails.
    static func footprint() -> Int64? {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(
            MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let kr = withUnsafeMutablePointer(to: &info) { ptr in
            ptr.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { intPtr in
                task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), intPtr, &count)
            }
        }
        guard kr == KERN_SUCCESS else { return nil }
        return Int64(info.phys_footprint)
    }

    private actor PeakRecorder {
        private(set) var peak: Int64 = 0
        func record(_ value: Int64) { peak = max(peak, value) }
    }

    /// Runs `work`, sampling footprint throughout, and returns peak minus baseline.
    static func peakGrowth(sampleInterval: Duration = .milliseconds(10),
                           during work: () async throws -> Void) async throws -> Int64 {
        let baseline = footprint() ?? 0
        let recorder = PeakRecorder()
        await recorder.record(baseline)

        let sampler = Task {
            while !Task.isCancelled {
                if let f = footprint() { await recorder.record(f) }
                try? await Task.sleep(for: sampleInterval)
            }
        }
        defer { sampler.cancel() }

        try await work()
        if let f = footprint() { await recorder.record(f) }
        return await recorder.peak - baseline
    }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter MemoryProbeTests`
Expected: PASS — 3 tests

- [x] **Step 5: Verify strict build**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: exit 0, no output

- [x] **Step 6: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/Support/MemoryProbe.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/MemoryProbeTests.swift
git commit -m "test: add MemoryProbe resident-footprint harness"
```

---

### Task 2: DiskProbe

Plumbing only. It produces no iCloud numbers until the adapter slice — do not claim otherwise.

**Files:**
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/DiskProbe.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/DiskProbeTests.swift`

**Interfaces:**
- Consumes: nothing
- Produces: `enum DiskProbe` with `static func directorySize(at: URL) throws -> Int64` and `static func sizeDelta(of:during:) async throws -> Int64`

- [x] **Step 1: Write the failing test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/DiskProbeTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct DiskProbeTests {

    private func makeTempDir() throws -> URL {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("diskprobe-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    @Test func emptyDirectoryHasZeroSize() throws {
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        #expect(try DiskProbe.directorySize(at: dir) == 0)
    }

    @Test func sizeDeltaSeesAFileWrittenDuringWork() async throws {
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(at: dir) }

        let oneMB = 1024 * 1024
        let delta = try await DiskProbe.sizeDelta(of: dir) {
            let payload = Data(count: oneMB)
            try payload.write(to: dir.appendingPathComponent("blob.bin"))
        }
        #expect(delta >= Int64(oneMB))
    }

    @Test func sizeDeltaIsZeroWhenNothingIsWritten() async throws {
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let delta = try await DiskProbe.sizeDelta(of: dir) {}
        #expect(delta == 0)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter DiskProbeTests`
Expected: FAIL — `cannot find 'DiskProbe' in scope`

- [x] **Step 3: Write minimal implementation**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/DiskProbe.swift`:

```swift
import Foundation

/// Measures the byte-size delta of a directory across a unit of work.
///
/// In this slice it is exercised only against temp directories. Pointing it at
/// the Photos library container to answer "does PhotoKit stream or materialize
/// first?" belongs to the adapter slice, which needs a real library and TCC consent.
enum DiskProbe {

    /// Total allocated size of all regular files under `url`, recursively.
    static func directorySize(at url: URL) throws -> Int64 {
        let fm = FileManager.default
        guard let e = fm.enumerator(at: url,
                                    includingPropertiesForKeys: [.isRegularFileKey,
                                                                 .totalFileAllocatedSizeKey,
                                                                 .fileSizeKey],
                                    options: [.skipsHiddenFiles]) else { return 0 }
        var total: Int64 = 0
        for case let fileURL as URL in e {
            let values = try fileURL.resourceValues(forKeys: [.isRegularFileKey,
                                                              .totalFileAllocatedSizeKey,
                                                              .fileSizeKey])
            guard values.isRegularFile == true else { continue }
            total += Int64(values.totalFileAllocatedSize ?? values.fileSize ?? 0)
        }
        return total
    }

    /// Runs `work` and returns the directory's size change in bytes.
    static func sizeDelta(of url: URL,
                          during work: () async throws -> Void) async throws -> Int64 {
        let before = try directorySize(at: url)
        try await work()
        return try directorySize(at: url) - before
    }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter DiskProbeTests`
Expected: PASS — 3 tests

- [x] **Step 5: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/Support/DiskProbe.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/DiskProbeTests.swift
git commit -m "test: add DiskProbe directory-size delta harness"
```

---

### Task 3: Go e2e — zero-byte PATCH declaring Upload-Length

Placed before the Swift uploader deliberately. Spec §6.3's zero-blob path depends on tusd accepting this, and no existing Go test covers it. If it fails, the spec needs revisiting — better to learn now than at integration.

**Files:**
- Modify: `server/internal/uploadbackend/tushandler_test.go` (append a test function)

**Interfaces:**
- Consumes: `TUSHandler.CreateUpload`, `TUSHandler.ForwardPatch`, `TUSHandler.IsComplete`, `TUSHandler.GetInfo`, `setupTUSHandler` — all existing in that file
- Produces: confirmation that a 0-byte PATCH carrying `Upload-Length` finalizes a deferred-length upload

- [x] **Step 1: Write the failing test**

Append to `server/internal/uploadbackend/tushandler_test.go`:

```go
// TestTUSZeroByteFinalPatchDeclaresLength verifies that an upload whose bytes
// were all written WITHOUT a declared length can be finalized by a subsequent
// zero-byte PATCH that carries Upload-Length. The Mac client needs this for the
// zero-blob resume path: when a resumed upload has no new bytes to send, there
// is no chunk to attach Upload-Length to.
func TestTUSZeroByteFinalPatchDeclaresLength(t *testing.T) {
	h, _ := setupTUSHandler(t)

	payload := []byte("bytes written without declaring a length")
	id, err := h.CreateUpload("")
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}

	// First PATCH: write all bytes, length still deferred.
	offset, err := h.ForwardPatch(id, bytes.NewReader(payload), 0, "")
	if err != nil {
		t.Fatalf("ForwardPatch (deferred): %v", err)
	}
	if offset != int64(len(payload)) {
		t.Fatalf("offset = %d, want %d", offset, len(payload))
	}

	complete, err := h.IsComplete(id)
	if err != nil {
		t.Fatalf("IsComplete before finalize: %v", err)
	}
	if complete {
		t.Fatal("upload should not be complete while size is deferred")
	}

	// Second PATCH: zero bytes, at the current offset, declaring the length.
	finalOffset, err := h.ForwardPatch(id, bytes.NewReader(nil), offset,
		strconv.FormatInt(offset, 10))
	if err != nil {
		t.Fatalf("zero-byte finalizing ForwardPatch: %v", err)
	}
	if finalOffset != offset {
		t.Errorf("final offset = %d, want %d", finalOffset, offset)
	}

	info, err := h.GetInfo(id)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.SizeIsDeferred {
		t.Error("SizeIsDeferred should be false after declaring length")
	}

	complete, err = h.IsComplete(id)
	if err != nil {
		t.Fatalf("IsComplete after finalize: %v", err)
	}
	if !complete {
		t.Error("upload should be complete after zero-byte PATCH declared the length")
	}
}
```

- [x] **Step 2: Run the test**

Run: `cd server && go test ./internal/uploadbackend/ -run TestTUSZeroByteFinalPatchDeclaresLength -v`

Expected: **PASS.** This test is written to confirm an assumption, not to drive new server code.

**If it FAILS, stop and report.** Do not work around it. Spec §6.3 and open item #1 depend on this behaviour; a failure means the zero-blob path needs redesigning (most likely by having `AssetUploader` skip `read` entirely when `HEAD` shows the offset already equals a known length, which requires a different source of truth for length). Surface it rather than patching around it.

- [x] **Step 3: Run the full server suite for regressions**

Run: `cd server && go test ./... && go vet ./...`
Expected: PASS, vet clean

- [x] **Step 4: Commit** *(wait for the user's go-ahead)*

```bash
git add server/internal/uploadbackend/tushandler_test.go
git commit -m "test(server): cover zero-byte PATCH declaring Upload-Length"
```

---

### Task 4: AssetDataSource seam + FakeAssetDataSource

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/AssetDataSource.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeAssetDataSource.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/FakeAssetDataSourceTests.swift`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `public protocol AssetDataSource: Sendable` with `func read(assetID: String, from offset: Int64, into sink: @Sendable (Data) async throws -> Void) async throws`
  - `struct FakeAssetDataSource: AssetDataSource` with `init(totalBytes: Int64, blobSize: Int, failAfterBlobs: Int? = nil)`
  - `enum FakeSourceError: Error { case injected }`

- [x] **Step 1: Write the failing test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/FakeAssetDataSourceTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct FakeAssetDataSourceTests {

    private actor Collector {
        private(set) var totalBytes = 0
        private(set) var blobCount = 0
        private(set) var maxConcurrent = 0
        private var active = 0

        func begin() { active += 1; maxConcurrent = max(maxConcurrent, active) }
        func end(_ count: Int) { active -= 1; totalBytes += count; blobCount += 1 }
    }

    @Test func deliversExactlyTheRequestedByteCount() async throws {
        let source = FakeAssetDataSource(totalBytes: 25, blobSize: 10)
        let collector = Collector()
        try await source.read(assetID: "A", from: 0) { blob in
            await collector.begin()
            await collector.end(blob.count)
        }
        #expect(await collector.totalBytes == 25)
        #expect(await collector.blobCount == 3)   // 10 + 10 + 5
    }

    @Test func startingOffsetReducesDeliveredBytes() async throws {
        let source = FakeAssetDataSource(totalBytes: 100, blobSize: 30)
        let collector = Collector()
        try await source.read(assetID: "A", from: 40) { blob in
            await collector.begin()
            await collector.end(blob.count)
        }
        #expect(await collector.totalBytes == 60)
    }

    /// The capacity-1 guarantee from spec §5.2 clause 2.
    @Test func neverInvokesSinkConcurrently() async throws {
        let source = FakeAssetDataSource(totalBytes: 1000, blobSize: 10)
        let collector = Collector()
        try await source.read(assetID: "A", from: 0) { blob in
            await collector.begin()
            try await Task.sleep(for: .microseconds(50))
            await collector.end(blob.count)
        }
        #expect(await collector.maxConcurrent == 1)
    }

    @Test func propagatesSinkErrors() async throws {
        let source = FakeAssetDataSource(totalBytes: 100, blobSize: 10)
        await #expect(throws: FakeSourceError.injected) {
            try await source.read(assetID: "A", from: 0) { _ in
                throw FakeSourceError.injected
            }
        }
    }

    @Test func injectedFailureStopsDelivery() async throws {
        let source = FakeAssetDataSource(totalBytes: 100, blobSize: 10, failAfterBlobs: 3)
        let collector = Collector()
        await #expect(throws: FakeSourceError.injected) {
            try await source.read(assetID: "A", from: 0) { blob in
                await collector.begin()
                await collector.end(blob.count)
            }
        }
        #expect(await collector.blobCount == 3)
    }

    /// Spec §5.2 clause 3 — conformances must honour cancellation.
    @Test func honoursTaskCancellation() async throws {
        let source = FakeAssetDataSource(totalBytes: 10_000, blobSize: 10)
        let collector = Collector()
        let task = Task {
            try await source.read(assetID: "A", from: 0) { blob in
                await collector.begin()
                try await Task.sleep(for: .milliseconds(1))
                await collector.end(blob.count)
            }
        }
        try await Task.sleep(for: .milliseconds(20))
        task.cancel()
        await #expect(throws: (any Error).self) { try await task.value }
        #expect(await collector.blobCount < 1000)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter FakeAssetDataSourceTests`
Expected: FAIL — `cannot find 'FakeAssetDataSource' in scope`

- [x] **Step 3: Write the seam**

Create `apple/FilesNestCore/Sources/FilesNestCore/AssetDataSource.swift`:

```swift
import Foundation

/// Streams an asset's bytes to a sink, one blob at a time.
///
/// The sink IS the backpressure mechanism. Because the sink is `async` and the
/// source must await it, "at most one blob in flight" is guaranteed by this
/// signature rather than by coordination code that has to be kept correct.
///
/// This is deliberately NOT an `AsyncSequence`: `AsyncThrowingStream` has no
/// producer backpressure, and its buffering policies DROP elements rather than
/// throttling — which for file bytes is silent corruption. See spec §2.1.
///
/// `FilesNestCore` stays pure Foundation; the `Photos`-backed conformance lives
/// in the app target.
public protocol AssetDataSource: Sendable {

    /// Delivers the asset's bytes starting at `offset`.
    ///
    /// Conformances MUST:
    /// 1. Deliver bytes starting at `offset`, in order, with no gaps or overlaps.
    /// 2. Fully await `sink` before producing the next blob (capacity-1).
    /// 3. Honour `Task` cancellation and stop producing promptly.
    /// 4. Throw on failure. Partial delivery is legal — the next run resumes
    ///    from the TUS offset.
    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws
}
```

- [x] **Step 4: Write the fake**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeAssetDataSource.swift`:

```swift
import Foundation
@testable import FilesNestCore

enum FakeSourceError: Error, Equatable { case injected }

/// Synthetic `AssetDataSource` for headless tests, including the memory gate.
///
/// HARNESS VALIDITY (spec §7.4): this type must never retain the blobs it
/// yields. Each blob is freshly allocated, handed to the sink, and dropped —
/// holding them in an array would make the memory gate measure the fake
/// instead of the uploader.
struct FakeAssetDataSource: AssetDataSource {
    let totalBytes: Int64
    let blobSize: Int
    var failAfterBlobs: Int?

    init(totalBytes: Int64, blobSize: Int, failAfterBlobs: Int? = nil) {
        self.totalBytes = totalBytes
        self.blobSize = blobSize
        self.failAfterBlobs = failAfterBlobs
    }

    func read(assetID: String,
              from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws {
        var produced = min(offset, totalBytes)
        var emitted = 0
        while produced < totalBytes {
            try Task.checkCancellation()
            if let limit = failAfterBlobs, emitted >= limit { throw FakeSourceError.injected }
            let n = Int(min(Int64(blobSize), totalBytes - produced))
            try await sink(Data(count: n))     // fresh allocation, never retained
            produced += Int64(n)
            emitted += 1
        }
    }
}
```

- [x] **Step 5: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter FakeAssetDataSourceTests`
Expected: PASS — 6 tests

- [x] **Step 6: Verify strict build**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: exit 0

- [x] **Step 7: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/AssetDataSource.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeAssetDataSource.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/FakeAssetDataSourceTests.swift
git commit -m "feat: add AssetDataSource seam with async-sink backpressure"
```

---

### Task 5: OffsetSkip

A tested skip helper for adapters. PhotoKit always restarts at byte 0, so `PhotosAssetDataSource` must discard bytes below `offset` — and that discard is exactly the `dropFirst` shape implicated in the original leak (spec §2.2), living in the one component `swift test` cannot reach. Core ships it tested so adapters don't hand-roll it.

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/OffsetSkip.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/OffsetSkipTests.swift`

**Interfaces:**
- Consumes: nothing
- Produces: `public struct OffsetSkip` with `init(skipping: Int64)` and `mutating func take(_ blob: Data) -> Data?`

- [x] **Step 1: Write the failing test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/OffsetSkipTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

@Suite struct OffsetSkipTests {

    private func bytes(_ range: ClosedRange<UInt8>) -> Data {
        Data(range.map { $0 })
    }

    @Test func zeroSkipPassesBlobsThrough() {
        var skip = OffsetSkip(skipping: 0)
        let blob = bytes(1...10)
        #expect(skip.take(blob) == blob)
    }

    @Test func negativeSkipIsTreatedAsZero() {
        var skip = OffsetSkip(skipping: -5)
        let blob = bytes(1...10)
        #expect(skip.take(blob) == blob)
    }

    @Test func fullyConsumedBlobReturnsNil() {
        var skip = OffsetSkip(skipping: 10)
        #expect(skip.take(bytes(1...10)) == nil)
    }

    @Test func partiallyConsumedBlobReturnsRemainder() {
        var skip = OffsetSkip(skipping: 4)
        #expect(skip.take(bytes(1...10)) == bytes(5...10))
    }

    @Test func skipSpansMultipleBlobs() {
        var skip = OffsetSkip(skipping: 25)
        #expect(skip.take(bytes(1...10)) == nil)          // 15 remaining
        #expect(skip.take(bytes(1...10)) == nil)          // 5 remaining
        #expect(skip.take(bytes(1...10)) == bytes(6...10)) // consumed
        #expect(skip.take(bytes(1...10)) == bytes(1...10)) // pass-through after
    }

    @Test func skipBeyondAllDataReturnsNilForever() {
        var skip = OffsetSkip(skipping: 1000)
        #expect(skip.take(bytes(1...10)) == nil)
        #expect(skip.take(bytes(1...10)) == nil)
    }

    /// The returned Data must NOT be a slice aliasing the input's storage — a
    /// retained slice keeps the whole input buffer alive, which is the
    /// copy-on-write failure mode from spec §2.2.
    @Test func returnedDataDoesNotAliasInputStorage() {
        var skip = OffsetSkip(skipping: 4)
        let result = skip.take(bytes(1...10))
        #expect(result?.startIndex == 0)
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter OffsetSkipTests`
Expected: FAIL — `cannot find 'OffsetSkip' in scope`

- [x] **Step 3: Write minimal implementation**

Create `apple/FilesNestCore/Sources/FilesNestCore/OffsetSkip.swift`:

```swift
import Foundation

/// Discards a fixed prefix of a byte stream delivered as successive blobs.
///
/// `PHAssetResourceManager.requestData` cannot resume mid-file: after an
/// interruption iCloud restarts at byte 0 regardless of the TUS offset. Adapters
/// use this to satisfy `AssetDataSource`'s "deliver from `offset`" contract.
/// Expected behaviour, not a bug — see `docs/architecture.md`.
public struct OffsetSkip: Sendable {
    private var remaining: Int64

    public init(skipping: Int64) {
        self.remaining = max(0, skipping)
    }

    /// Returns the portion of `blob` at or beyond the skip point, or nil when
    /// the blob falls entirely inside the skipped prefix.
    public mutating func take(_ blob: Data) -> Data? {
        guard remaining > 0 else { return blob }
        let count = Int64(blob.count)
        if remaining >= count {
            remaining -= count
            return nil
        }
        let dropCount = Int(remaining)
        remaining = 0
        // `Data(_:)` copies into fresh storage. Returning `blob.dropFirst(_:)`
        // directly would hand back a slice that keeps the ENTIRE input buffer
        // alive for as long as the remainder is retained.
        return Data(blob.dropFirst(dropCount))
    }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter OffsetSkipTests`
Expected: PASS — 7 tests

- [x] **Step 5: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/OffsetSkip.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/OffsetSkipTests.swift
git commit -m "feat: add OffsetSkip helper for non-seekable asset sources"
```

---

### Task 6: AssetUploader — happy path

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/AssetUploader.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/AssetUploaderTests.swift`

**Interfaces:**
- Consumes: `AssetDataSource` (Task 4); `ServerClient.offset(forUploadID:) -> UploadOffset`, `ServerClient.patchData(uploadID:offset:data:finalLength:) -> Int64`, `ServerClient.markComplete(uploadID:)`; `MockURLProtocol` (existing)
- Produces: `public struct AssetUploader` with `init(client: ServerClient, source: any AssetDataSource)` and `func upload(assetID: String, uploadID: String) async throws`; `public enum AssetUploaderError: Error, Equatable { case concurrentSinkCall }`

- [x] **Step 1: Write the failing test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/AssetUploaderTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

/// Serialized: shares the static `MockURLProtocol.handler`.
@Suite(.serialized)
struct AssetUploaderTests {

    /// Records PATCHes without retaining bodies.
    final class Recorder: @unchecked Sendable {
        private let lock = NSLock()
        private var _patches: [(offset: Int64, length: Int64, finalLength: Int64?)] = []
        private var _markedComplete = false

        var patches: [(offset: Int64, length: Int64, finalLength: Int64?)] {
            lock.lock(); defer { lock.unlock() }; return _patches
        }
        var markedComplete: Bool {
            lock.lock(); defer { lock.unlock() }; return _markedComplete
        }
        func addPatch(offset: Int64, length: Int64, finalLength: Int64?) {
            lock.lock(); defer { lock.unlock() }
            _patches.append((offset, length, finalLength))
        }
        func markComplete() {
            lock.lock(); defer { lock.unlock() }; _markedComplete = true
        }
    }

    /// Installs a handler emulating the TUS data endpoints.
    /// Body bytes are COUNTED, never accumulated.
    func installHandler(startOffset: Int64, recorder: Recorder) {
        MockURLProtocol.handler = { req in
            let url = req.url!
            switch req.httpMethod {
            case "HEAD":
                return MockURLProtocol.respond(
                    status: 200, headers: ["Upload-Offset": String(startOffset)], for: url)
            case "PATCH" where url.path.hasSuffix("/data"):
                let offset = Int64(req.value(forHTTPHeaderField: "Upload-Offset") ?? "0") ?? 0
                let finalLength = req.value(forHTTPHeaderField: "Upload-Length").flatMap(Int64.init)
                let length = req.httpBodyByteCount()
                recorder.addPatch(offset: offset, length: length, finalLength: finalLength)
                return MockURLProtocol.respond(
                    status: 204, headers: ["Upload-Offset": String(offset + length)], for: url)
            case "PATCH":
                recorder.markComplete()
                return MockURLProtocol.respond(
                    status: 200,
                    body: #"{"id":"U","local_identifier":"L","status":"complete","backend_id":"b"}"#
                        .data(using: .utf8)!,
                    for: url)
            default:
                return MockURLProtocol.respond(status: 500, for: url)
            }
        }
    }

    func makeClient() -> ServerClient {
        ServerClient(baseURL: URL(string: "https://h.test")!,
                     credentials: FakeCredentialStore(creds: nil),
                     session: MockURLProtocol.makeSession())
    }

    @Test func uploadsAllBlobsInOrderAndMarksComplete() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 250, blobSize: 100)
        let uploader = AssetUploader(client: makeClient(), source: source)
        try await uploader.upload(assetID: "A", uploadID: "U")

        let patches = recorder.patches
        #expect(patches.count == 3)
        #expect(patches.map(\.offset) == [0, 100, 200])
        #expect(patches.map(\.length) == [100, 100, 50])
        #expect(recorder.markedComplete)
    }

    /// Only the LAST PATCH declares Upload-Length, and it equals the total.
    @Test func declaresFinalLengthOnLastPatchOnly() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 250, blobSize: 100)
        try await AssetUploader(client: makeClient(), source: source)
            .upload(assetID: "A", uploadID: "U")

        let patches = recorder.patches
        #expect(patches.dropLast().allSatisfy { $0.finalLength == nil })
        #expect(patches.last?.finalLength == 250)
    }

    @Test func resumesFromServerReportedOffset() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 100, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 250, blobSize: 100)
        try await AssetUploader(client: makeClient(), source: source)
            .upload(assetID: "A", uploadID: "U")

        let patches = recorder.patches
        #expect(patches.map(\.offset) == [100, 200])
        #expect(patches.map(\.length) == [100, 50])
        #expect(patches.last?.finalLength == 250)
    }
}

extension URLRequest {
    /// Counts body bytes WITHOUT retaining them.
    ///
    /// HARNESS VALIDITY (spec §7.4): the existing `httpBodyStreamData()` helper
    /// accumulates the whole body into a `Data`. Using it in the memory gate
    /// would measure the test harness rather than the uploader.
    func httpBodyByteCount() -> Int64 {
        if let body = httpBody { return Int64(body.count) }
        guard let stream = httpBodyStream else { return 0 }
        stream.open()
        defer { stream.close() }
        let size = 64 * 1024
        let buf = UnsafeMutablePointer<UInt8>.allocate(capacity: size)
        defer { buf.deallocate() }
        var total: Int64 = 0
        while stream.hasBytesAvailable {
            let read = stream.read(buf, maxLength: size)
            if read <= 0 { break }
            total += Int64(read)
        }
        return total
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter AssetUploaderTests`
Expected: FAIL — `cannot find 'AssetUploader' in scope`

- [x] **Step 3: Write minimal implementation**

Create `apple/FilesNestCore/Sources/FilesNestCore/AssetUploader.swift`:

```swift
import Foundation

public enum AssetUploaderError: Error, Equatable, Sendable {
    /// An `AssetDataSource` invoked the sink concurrently, violating the
    /// capacity-1 contract. Signals a broken conformance, not a transient fault.
    case concurrentSinkCall
}

/// Uploads one asset's bytes to one TUS upload record.
///
/// Handles nothing and propagates everything: `ServerClientError.backendLost`
/// goes to `SyncCoordinator`, which owns delete-and-re-register recovery.
/// Keeping recovery out of here is what lets this stay a stateless `struct`.
public struct AssetUploader: Sendable {
    private let client: ServerClient
    private let source: any AssetDataSource

    public init(client: ServerClient, source: any AssetDataSource) {
        self.client = client
        self.source = source
    }

    public func upload(assetID: String, uploadID: String) async throws {
        let start = try await client.offset(forUploadID: uploadID)
        let state = LookAhead(client: client, uploadID: uploadID, startOffset: start.offset)

        // The closure captures ONLY a Sendable actor reference. Capturing and
        // mutating local vars here is rejected under Swift 6 strict concurrency
        // (#SendableClosureCaptures) — see spec §6.2.1.
        try await source.read(assetID: assetID, from: start.offset) { blob in
            try await state.consume(blob)
        }
        try await state.finish()
    }
}

/// Holds one blob back so the final PATCH can declare `Upload-Length`.
///
/// A blob is only known to be the last one when the source completes — after a
/// naive pass-through would already have sent it. tusd will not finalize an
/// upload whose size is still deferred, so the last PATCH must carry the length.
private actor LookAhead {
    private let client: ServerClient
    private let uploadID: String
    private var offset: Int64
    private var held: Data?
    private var inFlight = false

    init(client: ServerClient, uploadID: String, startOffset: Int64) {
        self.client = client
        self.uploadID = uploadID
        self.offset = startOffset
    }

    func consume(_ blob: Data) async throws {
        // Actors serialize access but are REENTRANT: two concurrent calls would
        // interleave across the patchData await, producing out-of-order offsets
        // and a silently corrupted file. The contract forbids this; nothing in
        // the type system enforces it, so enforce it here.
        guard !inFlight else { throw AssetUploaderError.concurrentSinkCall }
        inFlight = true
        defer { inFlight = false }

        if let previous = held {
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: previous, finalLength: nil)
        }
        held = blob
    }

    func finish() async throws {
        if let last = held {
            held = nil
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: last,
                                                finalLength: offset + Int64(last.count))
        } else {
            // Zero-blob path: no chunk to carry the length, so declare it with an
            // empty PATCH. Verified against tusd in Task 3.
            offset = try await client.patchData(uploadID: uploadID, offset: offset,
                                                data: Data(), finalLength: offset)
        }
        try await client.markComplete(uploadID: uploadID)
    }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd apple/FilesNestCore && swift test --filter AssetUploaderTests`
Expected: PASS — 3 tests

- [x] **Step 5: Verify strict build**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: exit 0

- [x] **Step 6: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/AssetUploader.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/AssetUploaderTests.swift
git commit -m "feat: add AssetUploader with actor-held look-ahead"
```

---

### Task 7: AssetUploader — edge cases

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/AssetUploaderTests.swift` (append tests)

**Interfaces:**
- Consumes: everything from Task 6; `ServerClientError.backendLost`; `AssetUploaderError.concurrentSinkCall`
- Produces: no new production symbols — Task 6's implementation should already satisfy these

- [x] **Step 1: Write the failing tests**

Append inside `struct AssetUploaderTests` in `apple/FilesNestCore/Tests/FilesNestCoreTests/AssetUploaderTests.swift`:

```swift
    /// Spec §6.3 — a resumed upload with no new bytes still needs its length declared.
    @Test func zeroBlobUploadDeclaresLengthWithEmptyPatch() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 500, recorder: recorder)

        // Source is fully consumed by the start offset, so it yields nothing.
        let source = FakeAssetDataSource(totalBytes: 500, blobSize: 100)
        try await AssetUploader(client: makeClient(), source: source)
            .upload(assetID: "A", uploadID: "U")

        let patches = recorder.patches
        #expect(patches.count == 1)
        #expect(patches.first?.length == 0)
        #expect(patches.first?.offset == 500)
        #expect(patches.first?.finalLength == 500)
        #expect(recorder.markedComplete)
    }

    @Test func propagatesBackendLostWithoutRecovering() async throws {
        MockURLProtocol.handler = { req in
            if req.httpMethod == "HEAD" {
                return MockURLProtocol.respond(
                    status: 200, headers: ["Upload-Offset": "0"], for: req.url!)
            }
            return MockURLProtocol.respond(
                status: 409,
                body: #"{"error":"backend_lost"}"#.data(using: .utf8)!,
                for: req.url!)
        }
        let source = FakeAssetDataSource(totalBytes: 250, blobSize: 100)
        let uploader = AssetUploader(client: makeClient(), source: source)

        await #expect(throws: ServerClientError.backendLost) {
            try await uploader.upload(assetID: "A", uploadID: "U")
        }
    }

    @Test func propagatesSourceErrors() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 1000, blobSize: 100, failAfterBlobs: 3)
        let uploader = AssetUploader(client: makeClient(), source: source)

        await #expect(throws: FakeSourceError.injected) {
            try await uploader.upload(assetID: "A", uploadID: "U")
        }
        // Look-ahead means blob 3 is still held when the source throws,
        // so only blobs 1 and 2 were PATCHed. No finalLength was declared.
        #expect(recorder.patches.count == 2)
        #expect(recorder.patches.allSatisfy { $0.finalLength == nil })
        #expect(!recorder.markedComplete)
    }

    @Test func propagatesCancellation() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 10_000_000, blobSize: 1000)
        let uploader = AssetUploader(client: makeClient(), source: source)

        let task = Task { try await uploader.upload(assetID: "A", uploadID: "U") }
        try await Task.sleep(for: .milliseconds(20))
        task.cancel()

        await #expect(throws: (any Error).self) { try await task.value }
        #expect(!recorder.markedComplete)
    }

    /// Spec §6.2.1 — a broken conformance must fail loudly, not corrupt the file.
    @Test func rejectsConcurrentSinkCalls() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        struct ConcurrentSource: AssetDataSource {
            func read(assetID: String, from offset: Int64,
                      into sink: @Sendable (Data) async throws -> Void) async throws {
                // Deliberately violates the capacity-1 contract.
                try await withThrowingTaskGroup(of: Void.self) { group in
                    for _ in 0..<4 {
                        group.addTask { try await sink(Data(count: 100)) }
                    }
                    try await group.waitForAll()
                }
            }
        }

        let uploader = AssetUploader(client: makeClient(), source: ConcurrentSource())
        await #expect(throws: AssetUploaderError.concurrentSinkCall) {
            try await uploader.upload(assetID: "A", uploadID: "U")
        }
    }
```

- [x] **Step 2: Run tests**

Run: `cd apple/FilesNestCore && swift test --filter AssetUploaderTests`

Expected: PASS — 8 tests total.

If `rejectsConcurrentSinkCalls` does not throw, the `inFlight` guard is not doing its job — the four child tasks may be serializing by accident. Make the sink slower (add `try await Task.sleep(for: .milliseconds(5))` inside `consume` temporarily) to confirm the guard actually fires, then remove the sleep.

- [x] **Step 3: Verify strict build**

Run: `cd apple/FilesNestCore && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
Expected: exit 0

- [x] **Step 4: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/AssetUploaderTests.swift
git commit -m "test: cover AssetUploader resume, zero-blob, cancellation, reentrancy"
```

---

### Task 8: The memory gate

The point of the whole slice.

**Files:**
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/MemoryGateTests.swift`

**Interfaces:**
- Consumes: `MemoryProbe` (Task 1), `FakeAssetDataSource` (Task 4), `AssetUploader` (Task 6), `URLRequest.httpBodyByteCount()` (Task 6)
- Produces: nothing consumed by later tasks

- [x] **Step 1: Write the gate test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/MemoryGateTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

/// Peak memory must be a function of BLOB size, not ASSET size.
///
/// Naively comparing 3 MB against 3 GB does not prove this. With 8 MB blobs a
/// 3 MB asset yields ONE 3 MB blob while a 3 GB asset holds TWO 8 MB blobs, so
/// the peaks differ by ~13 MB by construction — that gap is the look-ahead
/// floor, not a leak. A tolerance loose enough to absorb it (>=16 MB) would also
/// wave through a real 15 MB-per-GB leak.
///
/// So cases B and C hold blob size AND steady-state blob count fixed while
/// differing 8x in total size. `peak(C) - peak(B)` is the assertion that proves
/// the property; the ceiling and case A are sanity checks.
///
/// Serialized: shares `MockURLProtocol.handler`, and concurrent tests would
/// pollute a process-wide footprint measurement.
@Suite(.serialized)
struct MemoryGateTests {

    static let blobSize = 8 * 1024 * 1024        // 8 MB
    static let ceiling: Int64 = 64 * 1024 * 1024 // 64 MB
    static let tolerance: Int64 = 8 * 1024 * 1024 // 8 MB

    /// Discards bodies. Never call `httpBodyStreamData()` here — it accumulates
    /// the whole body and would measure the harness instead of the uploader.
    func installDiscardingHandler() {
        MockURLProtocol.handler = { req in
            let url = req.url!
            switch req.httpMethod {
            case "HEAD":
                return MockURLProtocol.respond(
                    status: 200, headers: ["Upload-Offset": "0"], for: url)
            case "PATCH" where url.path.hasSuffix("/data"):
                let offset = Int64(req.value(forHTTPHeaderField: "Upload-Offset") ?? "0") ?? 0
                let length = req.httpBodyByteCount()
                return MockURLProtocol.respond(
                    status: 204, headers: ["Upload-Offset": String(offset + length)], for: url)
            default:
                return MockURLProtocol.respond(
                    status: 200,
                    body: #"{"id":"U","local_identifier":"L","status":"complete","backend_id":"b"}"#
                        .data(using: .utf8)!,
                    for: url)
            }
        }
    }

    func measurePeak(totalBytes: Int64) async throws -> Int64 {
        installDiscardingHandler()
        let client = ServerClient(baseURL: URL(string: "https://h.test")!,
                                  credentials: FakeCredentialStore(creds: nil),
                                  session: MockURLProtocol.makeSession())
        let source = FakeAssetDataSource(totalBytes: totalBytes, blobSize: Self.blobSize)
        let uploader = AssetUploader(client: client, source: source)
        return try await MemoryProbe.peakGrowth {
            try await uploader.upload(assetID: "A", uploadID: "U")
        }
    }

    @Test func caseA_photoSizedAssetStaysUnderCeiling() async throws {
        let peak = try await measurePeak(totalBytes: 3 * 1024 * 1024)          // 3 MB
        #expect(peak < Self.ceiling)
    }

    @Test func caseBC_peakIsIndependentOfAssetSize() async throws {
        let small = try await measurePeak(totalBytes: 384 * 1024 * 1024)       // 384 MB
        let large = try await measurePeak(totalBytes: 3 * 1024 * 1024 * 1024)  // 3 GB

        #expect(small < Self.ceiling)
        #expect(large < Self.ceiling)

        // The assertion that actually proves size-independence: 8x the bytes,
        // same blob size, so growth must not track total size.
        #expect(large - small <= Self.tolerance)
    }
}
```

- [x] **Step 2: Run the gate**

Run: `cd apple/FilesNestCore && swift test --filter MemoryGateTests`

Expected: PASS — 2 tests. The 3 GB case moves ~3 GB of synthetic data through the stubbed stack; expect tens of seconds, not minutes.

**If `large - small` exceeds the tolerance, do NOT raise the tolerance.** That is the failure this whole slice exists to catch. Diagnose in this order:
1. Is `FakeAssetDataSource` retaining blobs? (spec §7.4 precondition 1)
2. Is the handler reading bodies into `Data`? (precondition 2 — check for `httpBodyStreamData()`)
3. Is `LookAhead.held` being cleared before the final `patchData` in `finish()`?
4. Is `URLSession` retaining request bodies across requests?

- [x] **Step 3: Record the actual numbers**

Run the gate with verbose output and note the real peaks:

`cd apple/FilesNestCore && swift test --filter MemoryGateTests 2>&1 | tail -20`

Add the observed values as a comment at the top of `MemoryGateTests.swift`, e.g.
`// Observed 2026-07-24 on M-series: A=~12MB, B=~26MB, C=~27MB`.
Spec §7.2 calls the 64 MB / 8 MB values starting points, not tuned constants — this is the data that lets a later pass tighten them.

- [x] **Step 4: Run the full suite**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS — all tests, including the 34 pre-existing ServerClient tests

- [x] **Step 5: Commit** *(wait for the user's go-ahead)*

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/MemoryGateTests.swift
git commit -m "test: add size-independent peak memory gate"
```

---

### Task 9: Documentation corrections

**Files:**
- Modify: `docs/architecture.md:19` (constraint #1)
- Modify: `docs/architecture.md:159-167` (Mac app streaming section)
- Modify: `docs/plans/20260724-serverclient.md` (stale `.macOS(.v15)` in Global Constraints)
- Modify: `docs/design/20260723-serverclient.md` (§10 open items now resolved)

**Interfaces:**
- Consumes: nothing
- Produces: nothing

- [x] **Step 1: Rewrite constraint #1**

In `docs/architecture.md`, replace the constraint #1 paragraph:

> 1. **No temp files.** PHAssetResourceManager streams data via callbacks; the server proxies chunks directly to the upload backend. A 7GB video never lands on disk on the Mac or as a completed file in the server before it is moved to final storage.

with:

```markdown
1. **No temp files *we* own, and no whole-file buffering in *our* memory.**
   `PHAssetResourceManager` streams data via callbacks; the server proxies chunks directly to
   the upload backend. A 7GB video never lands on the server as a completed file before it is
   moved to final storage, and never exists as a file this app created, named, or must clean up.

   **The bytes do touch the Mac's disk, and this is unavoidable.** When an asset is iCloud-only
   (Optimize Mac Storage), its bytes are not on the machine. The only sanctioned way to obtain
   them is `requestData` with `isNetworkAccessAllowed = true`, and PhotoKit materializes the
   resource into the Photos library container in order to serve it. **There is no public PhotoKit
   API for a ranged iCloud fetch** — you cannot request "bytes 0-8MB of this asset" and have only
   that fetched. That copy is PhotoKit's: it creates it, owns its lifecycle, and evicts it under
   disk pressure. We never create, name, or delete it.

   What we control is how many copies exist and how often they are made:
   - **Single pass** (`AssetUploader`) — one materialization per asset, not one per chunk.
   - **Sequential processing** — at most one asset materialized at a time, so peak transient
     cost is roughly the largest single asset, not the library.
   - **Free-space pre-flight** — skip an asset with a typed error rather than filling the disk.

   The previous iOS client did not avoid this cost either; it depended on the materialization and,
   per `CODE_AUDIT.md` §5.1, triggered it once per chunk.
```

- [x] **Step 2: Rewrite the Mac app streaming section**

In `docs/architecture.md`, replace the "Mac app: streaming without temp files" body (the numbered list describing `AsyncThrowingStream` and the 8MB buffer) with:

```markdown
`PHAssetResourceManager.requestData` delivers data via a callback (`dataReceivedHandler`). The app:

1. Bridges the callback API to an **async sink**: `AssetDataSource.read(assetID:from:into:)` takes
   a `@Sendable (Data) async throws -> Void`, and the source must fully await the sink before
   consuming the next callback.
2. **PATCHes each blob straight through** as it arrives. There is no accumulation buffer.
3. Holds exactly **one blob back** (look-ahead), so the final PATCH can carry `Upload-Length` —
   a blob is only known to be the last one once the source completes.

Peak memory is therefore two blobs, independent of asset size, and enforced by a test gate.

**Why not `AsyncThrowingStream`?** It has no producer backpressure: while the consumer uploads
chunk 1, the stream buffers chunks 2..N — 7GB resident for a 7GB file. Its `bufferingPolicy`
options do not help, because `.bufferingNewest(1)` and `.bufferingOldest(1)` **drop** elements
rather than throttling the producer, and dropping file bytes is silent corruption. A stream plus a
`DispatchSemaphore` was tried in the previous client: it serialized correctly but still grew
linearly, because `Data.append` with `prefix`/`dropFirst` on one long-lived buffer kept
copy-on-write backing storage alive. The async sink removes the buffer entirely rather than
managing it. See `docs/design/20260724-assetuploader.md` §2.

**iCloud resume asymmetry:** `requestData` cannot resume mid-file. If interrupted at 3GB of a 7GB
video, iCloud restarts from byte 0 even if the TUS offset is at 3GB. The adapter discards the
initial bytes up to `startOffset` using `OffsetSkip` and logs this clearly — it is expected
behavior, not a bug.
```

- [x] **Step 3: Fix the stale platform floor**

In `docs/plans/20260724-serverclient.md`, find `.macOS(.v15)` in Global Constraints and replace with:

```markdown
- Platform floor: core `.macOS(.v13)` / `.iOS(.v17)`; the macOS app target is macOS 14
  (`@Observable` requires 14).
```

- [x] **Step 4: Mark the resolved open items**

In `docs/design/20260723-serverclient.md`, replace the §10 heading and its four items with:

```markdown
## 10. Open items — all resolved

1. ~~`UploadRecord` exact fields~~ — confirmed against `server/internal/store`; modelled in
   `Models/UploadRecord.swift` and covered by `ModelCodingTests`.
2. ~~PATCH finalization header~~ — resolved: a resolved **`Upload-Length` on the last chunk**, not
   `Upload-Complete: 1`. `handlers.go:616-619` forwards the client's `Upload-Length` into
   `ForwardPatch`, and `tushandler_test.go:154-189` confirms deferred length survives PATCHes that
   omit it. Implemented as `patchData(finalLength:)`.
3. ~~409-on-PATCH disambiguation~~ — resolved: the server overloads 409 and the **error body string**
   is the discriminator. Mapped in `ServerClientError.map(status:body:)`, where match order matters
   because the PATCH-data handler emits a combined "already completed or deleted" message.
4. ~~`metadata` schema~~ — omitted. Live Photo pairing uses `bundleID`; the client has no use for
   `metadata` yet, so it is not sent.
```

- [x] **Step 5: Verify no stale claims remain**

Run:
```bash
cd ~/Projects/filesnest/files-nest
grep -n "never lands on disk" docs/architecture.md || echo "OK: stale claim removed"
grep -n "AsyncThrowingStream" docs/architecture.md
grep -n "macOS(.v15)" docs/plans/20260724-serverclient.md || echo "OK: stale floor removed"
```
Expected: the first and third print "OK: ..."; the second shows only the *explanatory* mention in the "Why not `AsyncThrowingStream`?" paragraph, never a prescriptive one.

- [x] **Step 6: Commit** *(wait for the user's go-ahead)*

```bash
git add docs/architecture.md docs/plans/20260724-serverclient.md docs/design/20260723-serverclient.md
git commit -m "docs: correct no-temp-files constraint and streaming design"
```

---

## Completion

After Task 9:

- [x] Run the full verification: `cd apple/FilesNestCore && swift test && swift build -Xswiftc -warnings-as-errors --scratch-path /tmp/x`
- [x] Run the server suite: `cd server && go test ./... && go vet ./...`
- [x] **Codex review at slice completion** (project convention — review the slice, not each task)
- [x] Verify any Codex findings against the actual Go/Swift source before acting on them
- [x] Open a PR only when the user asks

**Deferred to the adapter slice, deliberately:** `PhotosAssetDataSource`, real iCloud disk-delta numbers, the free-space pre-flight guard (needs resource size, which only the real adapter supplies), Xcode project setup, Photos entitlements, TCC consent. This slice builds the instrument; it does not answer the PhotoKit question in spec §3.3.
