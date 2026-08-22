import Testing
import Foundation
@testable import FilesNestCore

@Test func assetResourceRoundTripsThroughJSON() throws {
    let original = AssetResource(
        key: ResourceKey(localIdentifier: "ABC#weird", kind: .pairedVideo),
        filename: "IMG_0001.mov",
        creationDate: Date(timeIntervalSince1970: 1_700_000_000),
        bundleID: "com.example.live")
    let data = try JSONEncoder().encode(original)
    let decoded = try JSONDecoder().decode(AssetResource.self, from: data)
    #expect(decoded == original)
}

@Test func assetResourceArrayRoundTrips() throws {
    let items = (0..<3).map {
        AssetResource(key: ResourceKey(localIdentifier: "A\($0)", kind: .photo),
                      filename: "IMG\($0).jpg",
                      creationDate: Date(timeIntervalSince1970: 1_700_000_000 + Double($0)),
                      bundleID: nil)
    }
    let data = try JSONEncoder().encode(items)
    #expect(try JSONDecoder().decode([AssetResource].self, from: data) == items)
}
