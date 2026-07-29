import Foundation

/// Memoizes the last `resources(in:)` scan for a short TTL so a Sync Now that immediately
/// follows a launch count reuses the count's scan instead of paying a second full ~60s
/// enumeration. Shared between the assess pass and the `SyncCoordinator`.
///
/// The TTL is a stopgap: the continuous-watching slice replaces it with a
/// `PHPhotoLibraryChangeObserver` that invalidates the cache precisely on library change.
/// Until then, a photo taken within the TTL window won't appear until the cache expires.
public actor CachingAssetLibrary: AssetLibrary {
    private let wrapped: any AssetLibrary
    private let ttl: TimeInterval
    private let now: @Sendable () -> Date
    private var cached: (range: SyncRange, at: Date, result: [AssetResource])?

    public init(wrapping wrapped: any AssetLibrary,
                ttl: TimeInterval = 60,
                now: @escaping @Sendable () -> Date = { Date() }) {
        self.wrapped = wrapped
        self.ttl = ttl
        self.now = now
    }

    public func resources(in range: SyncRange,
                          onProgress: (@Sendable (_ done: Int, _ total: Int) -> Void)?) async throws -> [AssetResource] {
        if let c = cached, c.range == range, now().timeIntervalSince(c.at) < ttl {
            onProgress?(c.result.count, c.result.count)   // jump the counting UI straight to "done"
            return c.result
        }
        let result = try await wrapped.resources(in: range, onProgress: onProgress)
        cached = (range, now(), result)                   // only a completed scan is cached (a throw skips this)
        return result
    }

    /// Drops the memoized scan (e.g. on a library-change signal, once that lands).
    public func invalidate() { cached = nil }
}
