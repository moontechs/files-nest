import Foundation
import Testing
@testable import FilesNestCore

@Suite(.serialized)
struct LocalFolderWriterTests {
    private func temporaryDirectory() throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("filesnest-writer-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    @Test func streamsToExactDestination() async throws {
        let directory = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let destination = directory.appendingPathComponent("nested/result.jpg")
        let source = FakeAssetDataSource(totalBytes: 250, blobSize: 100, fillRandom: false)
        let writer = LocalFolderWriter(source: source, volumeFreeSpace: { _ in 100 })

        try await writer.write(assetID: "asset", destinationPath: destination)

        #expect(FileManager.default.fileExists(atPath: destination.path))
        #expect(try Data(contentsOf: destination).count == 250)
    }

    @Test func probesExistingAncestorForNestedDestination() async throws {
        let directory = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let probe = Probe()
        let destination = directory.appendingPathComponent("missing/year/month/result.jpg")
        let writer = LocalFolderWriter(source: FakeAssetDataSource(totalBytes: 1, blobSize: 1), volumeFreeSpace: { url in
            probe.record(url)
            return 100
        })

        try await writer.write(assetID: "asset", destinationPath: destination)

        #expect(probe.value == directory)
    }

    @Test func failedReadLeavesNoFinalOrTemporaryFile() async throws {
        let directory = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let destination = directory.appendingPathComponent("result.jpg")
        let writer = LocalFolderWriter(
            source: FakeAssetDataSource(totalBytes: 250, blobSize: 100,
                                        failAfterBlobs: 1, fillRandom: false),
            volumeFreeSpace: { _ in 100 })

        await #expect(throws: FakeSourceError.injected) {
            try await writer.write(assetID: "asset", destinationPath: destination)
        }
        #expect(!FileManager.default.fileExists(atPath: destination.path))
        let files = try FileManager.default.contentsOfDirectory(atPath: directory.path)
        #expect(files.isEmpty)
    }

    @Test func rejectsInsufficientFreeSpaceBeforeReading() async throws {
        let directory = try temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        let destination = directory.appendingPathComponent("result.jpg")
        let writer = LocalFolderWriter(source: FakeAssetDataSource(totalBytes: 10, blobSize: 10),
                                       minimumFreeSpace: 100,
                                       volumeFreeSpace: { _ in 99 })

        await #expect(throws: LocalFolderWriterError.insufficientFreeSpace(available: 99, required: 100)) {
            try await writer.write(assetID: "asset", destinationPath: destination)
        }
        #expect(!FileManager.default.fileExists(atPath: destination.path))
    }
}

private final class Probe: @unchecked Sendable {
    private let lock = NSLock()
    private var _value: URL?
    var value: URL? { lock.lock(); defer { lock.unlock() }; return _value }
    func record(_ url: URL) { lock.lock(); defer { lock.unlock() }; _value = url }
}
