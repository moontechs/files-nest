import Foundation

public enum SyncRange: Sendable, Equatable {
    case all
    case dates(ClosedRange<Date>)
}
