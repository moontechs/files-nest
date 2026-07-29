import Testing
import Foundation
@testable import FilesNestCore

/// Serialized: all tests register the same per-host handler (`Self.host`).
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
        MockURLProtocol.setHandler(forHost: Self.host) { req in
            let url = req.url!
            switch (req.httpMethod, url.path) {
            case ("HEAD", _):
                return MockURLProtocol.respond(
                    status: 200, headers: ["Upload-Offset": String(startOffset)], for: url)

            case ("PATCH", let path) where path.hasSuffix("/data"):
                let offset = Int64(req.value(forHTTPHeaderField: "Upload-Offset") ?? "0") ?? 0
                let finalLength = req.value(forHTTPHeaderField: "Upload-Length").flatMap(Int64.init)
                let length = req.httpBodyByteCount()
                recorder.addPatch(offset: offset, length: length, finalLength: finalLength)
                return MockURLProtocol.respond(
                    status: 204, headers: ["Upload-Offset": String(offset + length)], for: url)

            case ("PATCH", let path) where path.hasSuffix("/status"):
                recorder.markComplete()
                return MockURLProtocol.respond(status: 200, for: url)

            default:
                return MockURLProtocol.respond(status: 500, for: url)
            }
        }
    }

    static let host = "uploader.test"

    func makeClient() -> ServerClient {
        ServerClient(baseURL: URL(string: "https://\(Self.host)")!,
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

    @Test func singleBlobAssetDeclaresLengthOnItsOnlyPatch() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 40, blobSize: 100)
        try await AssetUploader(client: makeClient(), source: source)
            .upload(assetID: "A", uploadID: "U")

        let patches = recorder.patches
        #expect(patches.count == 1)
        #expect(patches.first?.offset == 0)
        #expect(patches.first?.length == 40)
        #expect(patches.first?.finalLength == 40)
        #expect(recorder.markedComplete)
    }

    // MARK: - Edge cases

    /// Design §6.3 — a resumed upload with no new bytes still needs its length
    /// declared, and there is no chunk to hang it on. Verified server-side by
    /// `TestTUSZeroByteFinalPatchDeclaresLength`.
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
        MockURLProtocol.setHandler(forHost: Self.host) { req in
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
        // Look-ahead means blob 3 is still held when the source throws, so only
        // blobs 1 and 2 were PATCHed, and no finalLength was ever declared.
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

        // Assert the SPECIFIC error. This previously asserted `(any Error).self`,
        // which passed while cancellation was actually being mangled into
        // ServerClientError.transport("...Code=-999 cancelled..."). A caller could
        // then only distinguish cancellation from a network failure by
        // string-matching -999 — exactly what the typed error exists to prevent.
        await #expect(throws: CancellationError.self) { try await task.value }
        #expect(!recorder.markedComplete)
    }

    /// Design §6.2.1 assumed a runtime reentrancy guard was needed because
    /// "nothing in the type system enforces" the capacity-1 contract. That was
    /// wrong, in a good way: `sink` is a NON-ESCAPING parameter, so a
    /// conformance cannot hand it to a task group or `async let`. Attempting it
    /// fails to compile with:
    ///
    ///     error: escaping closure captures non-escaping parameter 'sink'
    ///
    /// Concurrent sink invocation is therefore unrepresentable, not merely
    /// forbidden, and the guard was removed as unreachable.
    ///
    /// This test pins the *consequence* — sequential offsets with no
    /// interleaving — so a future change to `@escaping` breaks something.
    @Test func sinkIsInvokedStrictlySequentially() async throws {
        let recorder = Recorder()
        installHandler(startOffset: 0, recorder: recorder)

        let source = FakeAssetDataSource(totalBytes: 1000, blobSize: 100)
        try await AssetUploader(client: makeClient(), source: source)
            .upload(assetID: "A", uploadID: "U")

        // Offsets must form a gapless ascending chain. Any interleaving would
        // duplicate or reorder them.
        let offsets = recorder.patches.map(\.offset)
        #expect(offsets == [0, 100, 200, 300, 400, 500, 600, 700, 800, 900])
        #expect(recorder.patches.last?.finalLength == 1000)
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
