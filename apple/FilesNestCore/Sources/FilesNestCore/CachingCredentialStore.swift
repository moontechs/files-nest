import Foundation

/// Memoizes one credential lookup for the life of the app process.
///
/// The Keychain remains the sole durable store. This avoids repeatedly prompting
/// while a sync issues many authenticated server requests, and coalesces concurrent
/// first reads into one Keychain operation.
public final class CachingCredentialStore: CredentialSavingStore, @unchecked Sendable {
    private let wrapped: any CredentialSavingStore
    // NSLock, not OSAllocatedUnfairLock: this type needs to build on Linux too
    // (swift test for FilesNestCore runs there), and NSLock is the one lock
    // primitive available on every platform Foundation supports.
    private let lock = NSLock()
    private var state = State()

    private struct State: Sendable {
        var cached: BasicCredentials?
        var hasLoaded = false
        var inFlight: Task<BasicCredentials?, Error>?
    }

    public init(wrapping wrapped: any CredentialSavingStore) {
        self.wrapped = wrapped
    }

    public func basicCredentials() async throws -> BasicCredentials? {
        enum Load {
            case cached(BasicCredentials?)
            case task(Task<BasicCredentials?, Error>)
        }

        let load: Load = lock.withLock {
            if state.hasLoaded {
                return .cached(state.cached)
            }
            if let inFlight = state.inFlight { return .task(inFlight) }
            let wrapped = wrapped
            let read = Task { try await wrapped.basicCredentials() }
            state.inFlight = read
            return .task(read)
        }

        guard case let .task(task) = load else {
            if case let .cached(credentials) = load { return credentials }
            preconditionFailure("unreachable")
        }

        do {
            let credentials = try await task.value
            lock.withLock {
                state.cached = credentials
                state.hasLoaded = true
                state.inFlight = nil
            }
            return credentials
        } catch {
            lock.withLock { state.inFlight = nil }
            throw error
        }
    }

    public func save(_ credentials: BasicCredentials) throws {
        try wrapped.save(credentials)
        lock.withLock {
            state.cached = credentials
            state.hasLoaded = true
        }
    }
}
