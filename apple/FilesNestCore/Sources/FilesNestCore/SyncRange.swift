import Foundation

public enum SyncRange: Sendable, Equatable {
    case all
    case dates(ClosedRange<Date>)
    case modifiedSince(Date)   // incremental, upload-only; matched against modificationDate
}
