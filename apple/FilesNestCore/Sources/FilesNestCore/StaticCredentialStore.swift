import Foundation

/// A non-Keychain `CredentialStore` over fixed credentials — used to probe unsaved
/// Settings values before Save, and to inject creds into a `ServerClient`.
public struct StaticCredentialStore: CredentialStore {
    private let credentials: BasicCredentials?
    public init(_ credentials: BasicCredentials?) { self.credentials = credentials }
    public func basicCredentials() async throws -> BasicCredentials? { credentials }
}
