#if canImport(Darwin)
import Foundation
import Darwin

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
    ///
    /// CAUTION: the result is relative to the footprint at entry, so it is only
    /// comparable across runs whose allocator state is comparable. The allocator
    /// does not return freed pages to the OS promptly, so a run following a
    /// large one starts from an inflated baseline and under-reports its growth.
    /// Prefer `peakFootprint` when comparing two measurements to each other.
    static func peakGrowth(sampleInterval: Duration = .milliseconds(1),
                           during work: () async throws -> Void) async throws -> Int64 {
        let baseline = footprint() ?? 0
        let peak = try await peakFootprint(sampleInterval: sampleInterval, during: work)
        return peak - baseline
    }

    /// Runs `work`, sampling footprint throughout, and returns the ABSOLUTE peak.
    ///
    /// Unlike `peakGrowth` this is not relative to a baseline, so two
    /// measurements taken in the same warm process are directly comparable —
    /// which is what the size-independence gate needs.
    static func peakFootprint(sampleInterval: Duration = .milliseconds(1),
                              during work: () async throws -> Void) async throws -> Int64 {
        let recorder = PeakRecorder()
        if let f = footprint() { await recorder.record(f) }

        let sampler = Task.detached(priority: .high) {
            while !Task.isCancelled {
                if let f = footprint() { await recorder.record(f) }
                try? await Task.sleep(for: sampleInterval)
            }
        }
        defer { sampler.cancel() }

        try await work()
        if let f = footprint() { await recorder.record(f) }
        return await recorder.peak
    }
}
#endif
