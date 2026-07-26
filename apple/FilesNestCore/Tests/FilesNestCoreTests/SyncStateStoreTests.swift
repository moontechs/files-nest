import Testing
import Foundation
@testable import FilesNestCore

struct SyncStateStoreTests {
    @Test func userDefaultsRoundTripsDate() {
        let suite = UserDefaults(suiteName: "scc.state.\(UUID().uuidString)")!
        let store = UserDefaultsSyncStateStore(defaults: suite)
        #expect(store.loadLastSyncStarted() == nil)

        let d = Date(timeIntervalSince1970: 1_700_000_000) // whole second — ISO8601 has no sub-second here
        store.saveLastSyncStarted(d)
        #expect(store.loadLastSyncStarted() == d)
    }

    @Test func inMemoryRoundTrips() {
        let store = InMemorySyncStateStore()
        #expect(store.loadLastSyncStarted() == nil)
        let d = Date(timeIntervalSince1970: 42)
        store.saveLastSyncStarted(d)
        #expect(store.loadLastSyncStarted() == d)
    }
}
