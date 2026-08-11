import Testing
import Foundation
@testable import FilesNestCore

@Test func decodeUploadRecordFromServerJSON() throws {
    let json = """
    {"id":"abc","local_identifier":"LID","status":"uploading","backend_id":"b1",
     "filename":"IMG_1.jpg","bundle_id":"","creation_date":"2024-03-15T10:30:00Z",
     "created_at":"2024-03-15T10:30:00Z","updated_at":"2024-03-15T10:30:00Z"}
    """.data(using: .utf8)!
    let rec = try JSONDecoder().decode(UploadRecord.self, from: json)
    #expect(rec.id == "abc")
    #expect(rec.localIdentifier == "LID")
    #expect(rec.status == .uploading)
    #expect(rec.filename == "IMG_1.jpg")
}

@Test func decodeLeanCreateResponseLeavesOptionalsNil() throws {
    let rec = try JSONDecoder().decode(UploadRecord.self,
        from: #"{"id":"NEW","local_identifier":"l","status":"uploading","backend_id":"b"}"#.data(using: .utf8)!)
    #expect(rec.filename == nil)
    #expect(rec.creationDate == nil)
}

@Test func decodeBackendLostStatus() throws {
    let rec = try JSONDecoder().decode(UploadRecord.self,
        from: #"{"id":"x","local_identifier":"l","status":"backend_lost","backend_id":""}"#.data(using: .utf8)!)
    #expect(rec.status == .backendLost)
}

@Test func encodeCreateRequestUsesSnakeCaseAndRFC3339() throws {
    let req = CreateUploadRequest(
        localIdentifier: "LID", filename: "IMG_1.jpg",
        creationDate: Date(timeIntervalSince1970: 1_710_498_600), bundleID: nil)
    let enc = JSONEncoder(); enc.dateEncodingStrategy = .iso8601
    let obj = try JSONSerialization.jsonObject(with: enc.encode(req)) as! [String: Any]
    #expect(obj["local_identifier"] as? String == "LID")
    #expect(obj["creation_date"] as? String == "2024-03-15T10:30:00Z")
    #expect(obj["bundle_id"] == nil)  // nil omitted
}

@Test func decodeCompletingStatus() throws {
    // Transient server state while the file is moved to organized storage.
    let rec = try JSONDecoder().decode(UploadRecord.self,
        from: #"{"id":"x","local_identifier":"l","status":"completing","backend_id":"b"}"#.data(using: .utf8)!)
    #expect(rec.status == .completing)
}

@Test func decodeOrganizedPath() throws {
    let rec = try JSONDecoder().decode(UploadRecord.self,
        from: #"{"id":"x","local_identifier":"l","status":"complete","backend_id":"b","organized_path":"organized/2024/03/15/IMG_1.jpg"}"#.data(using: .utf8)!)
    #expect(rec.organizedPath == "organized/2024/03/15/IMG_1.jpg")
}
