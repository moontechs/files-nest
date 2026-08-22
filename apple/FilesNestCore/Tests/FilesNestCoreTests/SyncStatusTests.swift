import Testing
@testable import FilesNestCore

@Test func syncProgressInFlightDefaultsToZero() {
    let p = SyncProgress(completed: 1, total: 3, currentItemName: "IMG.jpg", bytesRemaining: nil)
    #expect(p.inFlight == 0)
}

@Test func syncProgressCarriesInFlight() {
    let p = SyncProgress(completed: 1, total: 3, currentItemName: "IMG.jpg",
                         bytesRemaining: nil, currentItemID: "A#photo", inFlight: 4)
    #expect(p.inFlight == 4)
}
