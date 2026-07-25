import Testing
@testable import FilesNestCore

@Suite struct ResourceKeyTests {

    @Test func roundTripsLocalIdentifierContainingSlashes() throws {
        let key = ResourceKey(localIdentifier: "B84E8479-475C-4727-A4A4-B77AA9980897/L0/001",
                              kind: .pairedVideo)
        #expect(key.encoded == "B84E8479-475C-4727-A4A4-B77AA9980897/L0/001#pairedVideo")
        let parsed = try ResourceKey(parsing: key.encoded)
        #expect(parsed == key)
    }

    /// The identifier may itself contain '#'. Splitting on the LAST '#' is the
    /// only correct reading because `kind` never contains '#'.
    @Test func parsesOnLastSeparatorWhenIdentifierContainsHash() throws {
        let parsed = try ResourceKey(parsing: "A#B#pairedVideo")
        #expect(parsed.localIdentifier == "A#B")
        #expect(parsed.kind == .pairedVideo)
    }

    @Test func rejectsMissingSeparator() {
        #expect(throws: ResourceKeyError.missingSeparator) {
            _ = try ResourceKey(parsing: "no-separator-here")
        }
    }

    @Test func rejectsUnknownKind() {
        #expect(throws: ResourceKeyError.unknownKind("bogus")) {
            _ = try ResourceKey(parsing: "ABC/L0/001#bogus")
        }
    }

    @Test func rejectsEmptyLocalIdentifier() {
        #expect(throws: ResourceKeyError.emptyLocalIdentifier) {
            _ = try ResourceKey(parsing: "#photo")
        }
    }
}
