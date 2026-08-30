import Testing
@testable import FilesNestCore

@Suite struct SafeIDTests {
    @Test(arguments: [
        ("AAAA-BBBB-CCCC-DDDD#photo", "QEzizTsZbhLknu3BxIqchpZg6BiVPEM7p8HYKhmIpCc"),
        ("AAAA-BBBB-CCCC-DDDD#pairedVideo", "FlwSC0rmUccfKH1nEq9BAo3lHk_SeclzxNeV9Sp_-kw"),
        ("", "47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"),
        ("AAAA-BBBB-CCCC-DDDD-café#photo", "8h9r2pPlYMjO0ke3F01cPwtzADNQkhqD2k72i46TAEk"),
    ])
    func matchesGroundTruthVectors(input: String, expected: String) {
        #expect(safeID(input) == expected)
    }

    @Test func hashesSeparatorsAndUnicodeAsRawUTF8() {
        #expect(safeID("asset#photo") != safeID("asset#pairedVideo"))
        #expect(safeID("café") != safeID("cafe\u{301}"))
        #expect(safeID("写真.jpg").count == 43)
    }
}
