import Testing
import Foundation
@testable import FilesNestCore

struct SyncStatusTests {
    @Test func fractionIsCompletedOverTotal() {
        let p = SyncProgress(completed: 3, total: 12, currentItemName: "IMG.HEIC", bytesRemaining: 100)
        #expect(p.fraction == 0.25)
    }

    @Test func fractionIsZeroWhenTotalZero() {
        let p = SyncProgress(completed: 0, total: 0, currentItemName: nil, bytesRemaining: nil)
        #expect(p.fraction == 0)
    }

    @Test func statusEquates() {
        #expect(SyncStatus.watching(lastSync: nil) == .watching(lastSync: nil))
        #expect(SyncStatus.paused(pending: 2) != .paused(pending: 3))
    }
}
