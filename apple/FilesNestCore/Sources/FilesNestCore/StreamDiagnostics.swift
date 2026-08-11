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
