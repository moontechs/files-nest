import Testing
import Foundation
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

// MARK: - control availability (mirrors the engine's command guards)

@Test func syncNowIsUnavailableWhileARunIsAlreadyInFlight() {
    // doSyncNow guards `syncChild == nil` and bails when paused, so the button would be a
    // no-op in these states; a count would be superseded and re-scan from zero.
    #expect(SyncStatus.syncing(SyncProgress(completed: 1, total: 2, currentItemName: nil,
                                            bytesRemaining: nil)).canSyncNow == false)
    #expect(SyncStatus.paused(pending: 3).canSyncNow == false)
    #expect(SyncStatus.counting(done: 1, total: 2).canSyncNow == false)
    #expect(SyncStatus.signedOut.canSyncNow == false)
    #expect(SyncStatus.watching(lastSync: nil).canSyncNow == true)
}

@Test func pauseAndResumeAreAvailableOnlyWhereTheyDoSomething() {
    #expect(SyncStatus.syncing(SyncProgress(completed: 0, total: 1, currentItemName: nil,
                                            bytesRemaining: nil)).canPause == true)
    #expect(SyncStatus.watching(lastSync: nil).canPause == true)
    #expect(SyncStatus.paused(pending: 1).canPause == false)     // already paused
    #expect(SyncStatus.signedOut.canPause == false)
    #expect(SyncStatus.counting(done: 1, total: 2).canPause == false)

    #expect(SyncStatus.paused(pending: 1).canResume == true)
    #expect(SyncStatus.syncing(SyncProgress(completed: 0, total: 1, currentItemName: nil,
                                            bytesRemaining: nil)).canResume == false)
    #expect(SyncStatus.watching(lastSync: nil).canResume == false)
}

@Test func reconnectingKeepsPauseAvailable() {
    let retry = RetryProgress(retryAt: Date(), waitingRequests: 2)
    let progress = SyncProgress(completed: 1, total: 3, currentItemName: "IMG.jpg",
                                bytesRemaining: nil, retry: retry)
    #expect(SyncStatus.reconnecting(progress).canPause == true)
    #expect(SyncStatus.reconnecting(progress).canSyncNow == false)
}
