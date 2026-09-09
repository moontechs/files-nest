import Testing
import Foundation
@testable import FilesNestCore

private func body(_ s: String) -> Data { #"{"error":"\#(s)"}"#.data(using: .utf8)! }

@Test func map409BackendLost() {
    #expect(ServerClientError.map(status: 409, body: body("backend_lost")) == .backendLost)
}
@Test func map409OffsetMismatchPrefix() {
    #expect(ServerClientError.map(status: 409, body: body("offset mismatch: client=5, server=10")) == .offsetConflict)
}
@Test func map409AlreadyCompleted() {
    #expect(ServerClientError.map(status: 409, body: body("upload already completed")) == .alreadyCompleted)
}
@Test func map409AlreadyDeletedIsDistinct() {
    // The status handler distinguishes deleted from completed; so must we.
    #expect(ServerClientError.map(status: 409, body: body("upload already deleted")) == .alreadyDeleted)
}
@Test func map409CombinedCompletedOrDeletedIsTerminal() {
    // PATCH /data emits a combined message; treat it as the terminal completed case.
    #expect(ServerClientError.map(status: 409, body: body("upload already completed or deleted")) == .alreadyCompleted)
}
@Test func map409UploadIncomplete() {
    #expect(ServerClientError.map(status: 409, body: body("upload_incomplete")) == .uploadIncomplete)
}
@Test func map409NotUploading() {
    #expect(ServerClientError.map(status: 409, body: body("upload not in uploading state")) == .notUploading)
}
@Test func mapStandardCodes() {
    #expect(ServerClientError.map(status: 401, body: Data()) == .unauthorized)
    #expect(ServerClientError.map(status: 404, body: body("upload not found")) == .notFound)
    #expect(ServerClientError.map(status: 413, body: Data()) == .requestTooLarge)
    #expect(ServerClientError.map(status: 400, body: body("bad filename")) == .badRequest(message: "bad filename"))
}
@Test func mapSuccessReturnsNil() {
    #expect(ServerClientError.map(status: 204, body: Data()) == nil)
}

@Test func readableDescriptionCoversEveryCase() {
    let cases: [(identifier: String, error: ServerClientError, expected: String)] = [
        ("unauthorized", .unauthorized, "Not authorized — check your server credentials in Settings."),
        ("notFound", .notFound, "This item is no longer on the server."),
        ("backendLost", .backendLost, "The server lost track of this upload. It will start over."),
        ("alreadyCompleted", .alreadyCompleted, "Already uploaded."),
        ("alreadyDeleted", .alreadyDeleted, "Already deleted on the server."),
        ("notUploading", .notUploading, "This upload isn't in a state that accepts more data."),
        ("offsetConflict", .offsetConflict, "Upload progress didn't match the server's records. It will start over."),
        ("uploadIncomplete", .uploadIncomplete, "This upload isn't finished yet on the server."),
        ("badRequest", .badRequest(message: "details"), "The server rejected this request: details."),
        ("requestTooLarge", .requestTooLarge, "This file is too large to upload."),
        ("unexpectedStatus", .unexpectedStatus(code: 500, message: "details"), "Server error (500): details"),
        ("decoding", .decoding("raw decoding details"), "Couldn't understand the server's response."),
        ("transport", .transport("raw transport details"), "No connection to the server."),
        ("serviceUnavailable", .serviceUnavailable(retryAfter: 5), "The server is busy. This will be retried."),
    ]

    for item in cases {
        let identifier = item.identifier
        let error = item.error
        let description = error.readableDescription
        #expect(!description.isEmpty)
        #expect(!description.contains(identifier))
        #expect(description == item.expected)
    }
}

@Test func readableDescriptionIncludesNonEmptyServerMessages() {
    #expect(ServerClientError.badRequest(message: "bad filename").readableDescription.contains("bad filename"))
    #expect(ServerClientError.unexpectedStatus(code: 500, message: "failed to write upload data").readableDescription.contains("failed to write upload data"))
}

@Test func readableDescriptionOmitsEmptyServerMessageDetails() {
    let badRequest = ServerClientError.badRequest(message: "").readableDescription
    let unexpected = ServerClientError.unexpectedStatus(code: 500, message: "").readableDescription

    #expect(badRequest == "The server rejected this request.")
    #expect(unexpected == "Server error (500).")
    #expect(ServerClientError.unexpectedStatus(code: 500, message: nil).readableDescription == unexpected)
    #expect(!badRequest.contains(": ."))
    #expect(!unexpected.contains(": "))
}

@Test func readableDescriptionHidesRawAssociatedErrorText() {
    #expect(!ServerClientError.decoding("secret decoding payload").readableDescription.contains("secret decoding payload"))
    #expect(!ServerClientError.transport("secret transport payload").readableDescription.contains("secret transport payload"))
}

@Test func readableFailureReasonDispatchesServerClientError() {
    let error = ServerClientError.notFound
    #expect(readableFailureReason(for: error) == error.readableDescription)
}

@Test func readableFailureReasonDispatchesLocalFolderSyncError() {
    let error = LocalFolderSyncError.unsafeDestination
    #expect(readableFailureReason(for: error) == error.readableDescription)
}

@Test func readableFailureReasonFallsBackToLocalizedDescription() {
    let error = NSError(domain: "FilesNestCoreTests", code: 1,
                        userInfo: [NSLocalizedDescriptionKey: "A localized failure"])
    #expect(readableFailureReason(for: error) == error.localizedDescription)
}
