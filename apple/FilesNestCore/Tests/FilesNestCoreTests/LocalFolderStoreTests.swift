import Foundation
import XCTest
@testable import FilesNestCore

final class LocalFolderStoreTests: XCTestCase {
    private func makeStore() -> UserDefaultsLocalFolderStore {
        let defaults = UserDefaults(suiteName: "local-folder-(UUID().uuidString)")!
        return UserDefaultsLocalFolderStore(defaults: defaults)
    }

    func testRoundTripAndClear() {
        let store = makeStore()
        XCTAssertNil(store.load())
        let bookmark = Data([1, 2, 3, 4])
        store.save(bookmark)
        XCTAssertEqual(store.load(), bookmark)
        store.clear()
        XCTAssertNil(store.load())
    }

    func testEmptyAndCorruptStoredValuesLoadAsNil() {
        let defaults = UserDefaults(suiteName: "local-folder-(UUID().uuidString)")!
        let store = UserDefaultsLocalFolderStore(defaults: defaults)
        defaults.set(Data(), forKey: "com.filesnest.localFolderBookmark")
        XCTAssertNil(store.load())
        defaults.set("not bookmark data", forKey: "com.filesnest.localFolderBookmark")
        XCTAssertNil(resolveLocalFolder(store: store))
    }

    func testMissingBookmarkCannotResolve() {
        XCTAssertNil(resolveLocalFolder(store: makeStore()))
    }
}
