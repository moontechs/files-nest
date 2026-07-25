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

    @Test func directorySizeCountsFilesInSubdirectories() throws {
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(at: dir) }

        let nested = dir.appendingPathComponent("a/b")
        try FileManager.default.createDirectory(at: nested, withIntermediateDirectories: true)
        try Data(count: 4096).write(to: nested.appendingPathComponent("deep.bin"))

        #expect(try DiskProbe.directorySize(at: dir) >= 4096)
    }

    @Test func volumeFreeSpaceIsPositiveForHomeDirectory() throws {
        let home = URL(fileURLWithPath: NSHomeDirectory())
        let free = try DiskProbe.volumeFreeSpace(at: home)
        #expect(free > 0)
    }
}
