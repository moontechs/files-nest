import Foundation
@testable import FilesNestCore

/// One-shot gate a test can release synchronously (e.g. from an `onProgress`
/// callback, which is `@Sendable` and non-async).
final class Baton: @unchecked Sendable {
    private let lock = NSLock()
    private var released = false
    private var waiter: CheckedContinuation<Void, Never>?

    func release() {
        lock.lock()
        released = true
        let w = waiter; waiter = nil
        lock.unlock()
        w?.resume()
    }

    func wait() async {
        await withCheckedContinuation { register($0) }
    }

    // Synchronous so NSLock is legal (locking directly in an async body is rejected
    // under Swift 6). Resumes immediately if already released, else parks the waiter.
    private func register(_ cont: CheckedContinuation<Void, Never>) {
        lock.lock()
        if released {
            lock.unlock()
            cont.resume()
        } else {
            waiter = cont
            lock.unlock()
        }
    }
}

/// `AssetDataSource` where the upload of `waitID` blocks on a `Baton` before
/// producing bytes, letting a test force a specific completion order.
struct OrderedDataSource: AssetDataSource {
    let baton: Baton
    let waitID: String
    let totalBytes: Int64
    let blobSize: Int

    func read(assetID: String, from offset: Int64,
              into sink: @Sendable (Data) async throws -> Void) async throws {
        if assetID == waitID { await baton.wait() }
        var sent = offset
        while sent < totalBytes {
            let n = Int(min(Int64(blobSize), totalBytes - sent))
            try await sink(Data(count: n))
            sent += Int64(n)
        }
    }
}
