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

    @Test func assessmentRoundTripsCodable() throws {
        let a = Assessment(backedUp: 63_201, pending: 7_243, resourceTotal: 70_444)
        let data = try JSONEncoder().encode(a)
        #expect(try JSONDecoder().decode(Assessment.self, from: data) == a)
    }

    @Test func userDefaultsCachesAssessment() {
        let suite = UserDefaults(suiteName: "scc.assess.\(UUID().uuidString)")!
        let store = UserDefaultsSyncStateStore(defaults: suite)
        #expect(store.loadAssessment() == nil)
        let a = Assessment(backedUp: 5, pending: 7, resourceTotal: 12)
        store.saveAssessment(a)
        #expect(store.loadAssessment() == a)
    }

    @Test func inMemoryCachesAssessment() {
        let store = InMemorySyncStateStore()
        #expect(store.loadAssessment() == nil)
        let a = Assessment(backedUp: 9, pending: 4, resourceTotal: 20)
        store.saveAssessment(a)
        #expect(store.loadAssessment() == a)
    }
}
