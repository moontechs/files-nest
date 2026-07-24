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
