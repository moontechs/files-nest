import Foundation

/// Counts how many of the fake source's blobs are alive at any instant.
///
/// This exists because OS memory accounting proved unusable as a leak detector.
/// Measured during implementation: a deliberately injected leak that retained a
/// verified 200,802,304 bytes moved `phys_footprint` by only 39 MB, because the
/// upload's own allocation churn caused macOS to compress and swap out the cold
/// retained pages. The same 200 MB retained WITHOUT churn reported 148 MB. The
/// metric is structurally blind to the workload it needs to measure.
///
/// Blob liveness is exact instead: `FakeAssetDataSource` hands out
/// `Data(bytesNoCopy:count:deallocator:)` over memory it allocates itself, and
/// the custom deallocator fires precisely when the last reference to that buffer
/// goes away. If the uploader retains a blob — or an aliasing slice of one, which
/// is the previous client's actual bug — the deallocator does not fire and
/// `maxAlive` climbs.
///
/// KNOWN LIMIT: a leak that COPIES bytes into fresh storage allocates memory this
/// tracker never handed out, so it would not be caught here. Aliasing/retention,
/// the historical failure mode, is caught exactly.
///
/// CAVEAT: tracked buffers must exceed `Data`'s inline-storage threshold (~14
/// bytes on 64-bit). At or below it, `Data(bytesNoCopy:count:deallocator:)`
/// copies into inline storage and fires the deallocator immediately, so liveness
/// reads as 0 no matter who retains the value. Measured: a 10-byte buffer freed
/// instantly; 100 bytes and above track correctly. The gate uses 8 MB blobs, so
/// it is unaffected — but a future test using tiny blobs would silently measure
/// nothing.
final class BlobLifetimeTracker: @unchecked Sendable {
    private let lock = NSLock()
    private var _alive = 0
    private var _maxAlive = 0
    private var _allocated = 0

    var alive: Int { lock.lock(); defer { lock.unlock() }; return _alive }
    var maxAlive: Int { lock.lock(); defer { lock.unlock() }; return _maxAlive }
    var allocated: Int { lock.lock(); defer { lock.unlock() }; return _allocated }

    func didAllocate() {
        lock.lock(); defer { lock.unlock() }
        _alive += 1
        _allocated += 1
        _maxAlive = max(_maxAlive, _alive)
    }

    func didFree() {
        lock.lock(); defer { lock.unlock() }
        _alive -= 1
    }
}
