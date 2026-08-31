//
//  FilesNestTests.swift
//  FilesNestTests
//
//  Created by Paulo Garcia on 23.07.26.
//

import Testing
import FilesNestCore
@testable import FilesNest

struct FilesNestTests {

    @Test func coordinatorKindMatchesDestination() {
        #expect(coordinatorKind(for: .server) == .server)
        #expect(coordinatorKind(for: .localFolder) == .localFolder)
    }

    @Test func example() async throws {
        // Write your test here and use APIs like `#expect(...)` to check expected conditions.
    }

}
