import Foundation

public struct ServerClient: Sendable {
    let baseURL: URL
    let credentials: any CredentialStore
    let session: URLSession

    public init(baseURL: URL, credentials: any CredentialStore, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.credentials = credentials
        self.session = session
    }

    // MARK: URL construction (client-side; server's upload_url is ignored)

    func dataURL(for id: String) -> URL {
        baseURL.appendingPathComponent("uploads").appendingPathComponent(id).appendingPathComponent("data")
    }
    func uploadsURL() -> URL { baseURL.appendingPathComponent("uploads") }
    func uploadURL(id: String) -> URL { baseURL.appendingPathComponent("uploads").appendingPathComponent(id) }

    // MARK: Request building + sending

    func authorizedRequest(_ url: URL, method: String) async throws -> URLRequest {
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let c = try await credentials.basicCredentials() {
            let token = Data("\(c.username):\(c.password)".utf8).base64EncodedString()
            req.setValue("Basic \(token)", forHTTPHeaderField: "Authorization")
        }
        return req
    }

    @discardableResult
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw ServerClientError.transport(String(describing: error))
        }
        guard let http = response as? HTTPURLResponse else {
            throw ServerClientError.transport("non-HTTP response")
        }
        if let err = ServerClientError.map(status: http.statusCode, body: data) { throw err }
        return (data, http)
    }

    func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw ServerClientError.decoding(String(describing: error))
        }
    }
}
