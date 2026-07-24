public protocol CredentialStore: Sendable {
    func basicCredentials() async throws -> BasicCredentials?
}
