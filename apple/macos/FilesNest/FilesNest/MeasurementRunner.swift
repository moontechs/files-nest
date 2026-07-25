import Foundation
import Combine
import Photos
import Darwin
import FilesNestCore

/// Current `phys_footprint` in bytes, corroboration only (spec §6.2.1). Inlined
/// here rather than exposed from Core — the test-only MemoryProbe is not part of
/// the FilesNestCore library, and footprint is not the primary signal.
private func processFootprint() -> Int64 {
    var info = task_vm_info_data_t()
    var count = mach_msg_type_number_t(
        MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
    let kr = withUnsafeMutablePointer(to: &info) { ptr in
        ptr.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { intPtr in
            task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), intPtr, &count)
        }
    }
    return kr == KERN_SUCCESS ? Int64(info.phys_footprint) : 0
}

/// Thread-safe recorder the @Sendable sink writes to. The runner reads it back
/// on the main actor to render results — the sink can't capture MainActor state.
nonisolated final class MeasurementRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var _lines: [String] = []
    private var _samples: [(t: TimeInterval, free: Int64, footprint: Int64)] = []
    private let start: Date

    init(start: Date) { self.start = start }

    func log(_ s: String) { lock.lock(); _lines.append(s); lock.unlock() }
    func sample(free: Int64, footprint: Int64, now: Date) {
        lock.lock(); _samples.append((now.timeIntervalSince(start), free, footprint)); lock.unlock()
    }
    var lines: [String] { lock.lock(); defer { lock.unlock() }; return _lines }
    var samples: [(t: TimeInterval, free: Int64, footprint: Int64)] {
        lock.lock(); defer { lock.unlock() }; return _samples
    }
}

/// Streams one asset with a discarding sink, stalling three times mid-stream,
/// while sampling free space and footprint. Reports growth-rate around each
/// stall. See spec §6.2.1 / §6.4. Within-run measurement, NOT cross-run.
@MainActor
final class MeasurementRunner: ObservableObject {
    @Published var log: [String] = []
    @Published var running = false

    /// Stall on every Nth blob, up to `maxStalls` times. Blob size is unknown
    /// until the first real run (partly what we measure) — tune down if a run
    /// produces fewer than maxStalls*stallEveryNBlobs blobs. Flagged in the plan.
    private let stallEveryNBlobs = 40
    private let maxStalls = 3
    private let stallSeconds: UInt64 = 30

    func run(localIdentifier: String, kind: ResourceKind, timestamp: Date) async {
        running = true
        defer { running = false }
        log = ["starting…"]

        let key = ResourceKey(localIdentifier: localIdentifier, kind: kind).encoded
        let recorder = MeasurementRecorder(start: timestamp)
        let home = URL(fileURLWithPath: NSHomeDirectory())
        let stallEvery = stallEveryNBlobs
        let maxStalls = self.maxStalls
        let stallSeconds = self.stallSeconds

        // 1 Hz background sampler.
        let sampler = Task.detached {
            while !Task.isCancelled {
                let free = (try? DiskProbe.volumeFreeSpace(at: home)) ?? 0
                let fp = processFootprint()
                recorder.sample(free: free, footprint: fp, now: Date())
                try? await Task.sleep(for: .seconds(1))
            }
        }

        let bytes = ByteBox()
        let stalls = StallBox()
        let source = PhotosAssetDataSource()

        do {
            try await source.read(assetID: key, from: 0) { blob in
                let n = bytes.add(blob.count)
                let idx = stalls.nextBlobIndex()
                if stalls.count() < maxStalls && idx % stallEvery == 0 {
                    let s = stalls.begin()
                    recorder.log("STALL #\(s) at blob \(idx), bytes \(n)")
                    try await Task.sleep(for: .seconds(Double(stallSeconds)))
                    recorder.log("RELEASE #\(s)")
                }
                // discard blob
            }
            recorder.log("completed, total bytes \(bytes.total())")
        } catch {
            recorder.log("ERROR: \(error)")
        }

        sampler.cancel()
        renderReport(recorder)
    }

    private func renderReport(_ recorder: MeasurementRecorder) {
        var out = recorder.lines
        out.append("— samples (t seconds, free bytes, footprint bytes) —")
        for s in recorder.samples {
            out.append(String(format: "t=%.1f free=%lld footprint=%lld", s.t, s.free, s.footprint))
        }
        out.append("INTERPRET (spec §6.2.1): near-zero free-space growth DURING a stall,")
        out.append("resuming after = backpressure real. Growth continuing at the pre-stall")
        out.append("rate through a stall = read-ahead. Ambiguous across the three stalls = UNRESOLVED.")
        log = out
    }
}

/// Sendable counters for the @Sendable sink.
nonisolated final class ByteBox: @unchecked Sendable {
    private let lock = NSLock(); private var _n: Int64 = 0
    func add(_ c: Int) -> Int64 { lock.lock(); defer { lock.unlock() }; _n += Int64(c); return _n }
    func total() -> Int64 { lock.lock(); defer { lock.unlock() }; return _n }
}

nonisolated final class StallBox: @unchecked Sendable {
    private let lock = NSLock(); private var _blob = 0; private var _stalls = 0
    func nextBlobIndex() -> Int { lock.lock(); defer { lock.unlock() }; _blob += 1; return _blob }
    func count() -> Int { lock.lock(); defer { lock.unlock() }; return _stalls }
    func begin() -> Int { lock.lock(); defer { lock.unlock() }; _stalls += 1; return _stalls }
}
