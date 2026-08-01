import Testing
import Foundation
@testable import FilesNestCore

struct SyncValueTypeTests {
    @Test func assetResourceEquatableAndFieldsRoundTrip() {
        let k = ResourceKey(localIdentifier: "UUID/L0/001", kind: .photo)
        let d = Date(timeIntervalSince1970: 1_700_000_000)
        let a = AssetResource(key: k, filename: "IMG_1.jpg", creationDate: d, bundleID: "UUID/L0/001")
        let b = AssetResource(key: k, filename: "IMG_1.jpg", creationDate: d, bundleID: "UUID/L0/001")
        #expect(a == b)
        #expect(a.key.kind == .photo)
        #expect(a.bundleID == "UUID/L0/001")
    }

    @Test func syncRangeEquatable() {
        let d = Date(timeIntervalSince1970: 100)
        #expect(SyncRange.modifiedSince(d) == SyncRange.modifiedSince(d))
        #expect(SyncRange.all != SyncRange.modifiedSince(d))
    }
}
