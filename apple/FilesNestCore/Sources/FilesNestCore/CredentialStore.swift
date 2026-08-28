public protocol CredentialStore: Sendable {
    func basicCredentials() async throws -> BasicCredentials?
}

/// A credential store that can also persist credentials entered in Settings.
/// Keeping this separate from `CredentialStore` lets sync dependencies remain read-only.
public protocol CredentialSavingStore: CredentialStore {
    func save(_ credentials: BasicCredentials) throws
}
