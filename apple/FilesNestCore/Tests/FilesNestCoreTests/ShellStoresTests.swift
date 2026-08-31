import Testing
import Foundation
@testable import FilesNestCore

struct ShellStoresTests {
    @Test func serverURLRoundTrips() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        let store = UserDefaultsServerURLStore(defaults: suite)
        #expect(store.load() == nil)
        store.save(URL(string: "https://nest.home.example")!)
        #expect(store.load() == URL(string: "https://nest.home.example")!)
    }

    @Test func serverURLNilWhenStoredValueInvalid() {
        let suite = UserDefaults(suiteName: "shell.\(UUID().uuidString)")!
        suite.set("", forKey: "com.filesnest.serverURL")
        let store = UserDefaultsServerURLStore(defaults: suite)
        #expect(store.load() == nil)
    }

    @Test func staticCredentialStoreReturnsValueThenNil() async throws {
        let creds = BasicCredentials(username: "u", password: "p")
        #expect(try await StaticCredentialStore(creds).basicCredentials() == creds)
        #expect(try await StaticCredentialStore(nil).basicCredentials() == nil)
    }

    @Test func syncDestinationDefaultsToServerAndRoundTrips() {
        let suite = UserDefaults(suiteName: "destination.\(UUID().uuidString)")!
        let store = UserDefaultsSyncDestinationStore(defaults: suite)
        #expect(store.load() == .server)
        store.save(.localFolder)
        #expect(store.load() == .localFolder)
        store.save(.server)
        #expect(store.load() == .server)
    }

    @Test func remainingUploadsMustMatchTheirDestination() {
        let suite = UserDefaults(suiteName: "destination.resume.\(UUID().uuidString)")!
        let folders = UserDefaultsLocalFolderStore(defaults: suite)
        let bookmark = Data([1, 2, 3])

        #expect(remainingUploadsBelong(to: .server, savedDestination: nil, localFolderStore: folders))
        #expect(!remainingUploadsBelong(to: .server, savedDestination: bookmark, localFolderStore: folders))
        #expect(!remainingUploadsBelong(to: .localFolder, savedDestination: bookmark, localFolderStore: folders))

        folders.save(bookmark)
        #expect(remainingUploadsBelong(to: .localFolder, savedDestination: bookmark, localFolderStore: folders))
    }

    @Test func destinationReadinessRequiresServerURLAndCredentials() async {
        let suite = UserDefaults(suiteName: "destination.\(UUID().uuidString)")!
        let urlStore = UserDefaultsServerURLStore(defaults: suite)
        let localFolderStore = UserDefaultsLocalFolderStore(defaults: suite)
        let credentials = BasicCredentials(username: "u", password: "p")

        #expect(!(await isDestinationReady(.server, urlStore: urlStore,
                                           credStore: StaticCredentialStore(credentials), localFolderStore: localFolderStore)))
        urlStore.save(URL(string: "https://nest.home.example")!)
        #expect(await isDestinationReady(.server, urlStore: urlStore,
                                         credStore: StaticCredentialStore(credentials), localFolderStore: localFolderStore))
        #expect(!(await isDestinationReady(.server, urlStore: urlStore,
                                           credStore: StaticCredentialStore(nil), localFolderStore: localFolderStore)))
        #expect(!(await isDestinationReady(.localFolder, urlStore: urlStore,
                                           credStore: StaticCredentialStore(credentials), localFolderStore: localFolderStore)))
    }

    #if canImport(Darwin)
    // Security-scoped bookmarks are Apple-only (see LocalFolderStore.swift);
    // isDestinationReady(.localFolder, ...) is unconditionally false elsewhere.
    @Test func localFolderReadinessRequiresExistingWritableBookmarkedDirectory() async throws {
        let suite = UserDefaults(suiteName: "destination.folder.\(UUID().uuidString)")!
        let urlStore = UserDefaultsServerURLStore(defaults: suite)
        let localFolderStore = UserDefaultsLocalFolderStore(defaults: suite)
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("filesnest-ready-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let bookmark = try directory.bookmarkData(options: [.withSecurityScope], includingResourceValuesForKeys: nil, relativeTo: nil)
        localFolderStore.save(bookmark)

        #expect(await isDestinationReady(.localFolder, urlStore: urlStore,
                                         credStore: StaticCredentialStore(nil), localFolderStore: localFolderStore))
        try FileManager.default.removeItem(at: directory)
        #expect(!(await isDestinationReady(.localFolder, urlStore: urlStore,
                                           credStore: StaticCredentialStore(nil), localFolderStore: localFolderStore)))
    }
    #endif

    @Test func cachingCredentialStoreCoalescesConcurrentReads() async throws {
        let wrapped = DelayedCredentialStore(
            credentials: BasicCredentials(username: "u", password: "p"))
        let store = CachingCredentialStore(wrapping: wrapped)

        async let first = store.basicCredentials()
        async let second = store.basicCredentials()
        let firstResult = try await first
        let secondResult = try await second
        #expect(firstResult == secondResult)
        #expect(wrapped.readCount == 1)
    }
}

private final class DelayedCredentialStore: CredentialSavingStore, @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0
    private let credentials: BasicCredentials
    var readCount: Int { lock.withLock { count } }

    init(credentials: BasicCredentials) { self.credentials = credentials }

    func basicCredentials() async throws -> BasicCredentials? {
        lock.withLock { count += 1 }
        try await Task.sleep(for: .milliseconds(20))
        return credentials
    }

    func save(_ credentials: BasicCredentials) throws {}
}
