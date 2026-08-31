import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

public enum ConnectionResult: Sendable, Equatable {
    case ok                    // reachable and authenticated
    case unauthorized          // reached server, credentials rejected (401)
    case unreachable(String)   // network/DNS/TLS/other, with a display message
}

/// Verifies a server URL + credentials by issuing an authenticated `GET /uploads`
/// (`listUploads`) — no dedicated health endpoint needed.
public struct ConnectionProbe: Sendable {
    private let session: URLSession
    public init(session: URLSession = .shared) { self.session = session }

    public func probe(baseURL: URL, credentials: BasicCredentials) async -> ConnectionResult {
        let client = ServerClient(baseURL: baseURL,
                                  credentials: StaticCredentialStore(credentials),
                                  session: session,
                                  // An explicit user action must report the current
                                  // connection state, not wait through upload retries.
                                  maxPatchRetries: 0)
        do {
            _ = try await client.listUploads(cursor: nil)
            return .ok
        } catch ServerClientError.unauthorized {
            return .unauthorized
        } catch let error as URLError {
            return .unreachable(error.localizedDescription)
        } catch {
            return .unreachable(String(describing: error))
        }
    }
}
