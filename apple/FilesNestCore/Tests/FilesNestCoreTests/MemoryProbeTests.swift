#if canImport(Darwin)
import Testing
import Foundation
@testable import FilesNestCore

/// Serialized: `phys_footprint` is process-wide, so tests running in parallel
/// contaminate each other's readings. Observed concretely — the trivial-work
/// test measured 134 MB of growth because a sibling test allocated 128 MB
/// concurrently. Nested under `ProcessFootprintTests` (see MemoryGateTests.swift)
/// so that guarantee also holds against MemoryGateTests's own large
/// allocations, not just against tests within this suite.
extension ProcessFootprintTests {

@Suite(.serialized) struct MemoryProbeTests {

    @Test func footprintReturnsPositiveValue() throws {
        let f = try #require(MemoryProbe.footprint())
        #expect(f > 0)
    }

    /// The probe must actually SEE an allocation. A probe that always reports
    /// ~0 would make every later memory assertion pass vacuously.
    @Test func peakGrowthDetectsALargeAllocation() async throws {
        let growth = try await MemoryProbe.peakGrowth {
            var block = Data(count: 128 * 1024 * 1024)
            // Touch every page: `Data(count:)` is zero-filled and the kernel can
            // back it lazily with the shared zero page, which never shows up in
            // phys_footprint.
            block.withUnsafeMutableBytes { raw in
                guard let base = raw.baseAddress else { return }
                for offset in stride(from: 0, to: raw.count, by: 4096) {
                    base.storeBytes(of: UInt8(1), toByteOffset: offset, as: UInt8.self)
                }
            }
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

}
#endif
