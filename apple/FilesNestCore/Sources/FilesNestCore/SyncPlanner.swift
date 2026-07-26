import Foundation

/// Pure diff: (library resources, server records, range) -> plan. No I/O, no fakes.
public enum SyncPlanner {
    public static func plan(library: [AssetResource],
                            server: [UploadRecord],
                            range: SyncRange) -> SyncPlan {
        let serverByKey = Dictionary(server.map { ($0.localIdentifier, $0) },
                                     uniquingKeysWith: { first, _ in first })
        let libraryKeys = Set(library.map { $0.key.encoded })

        // Upload side — iterate library resources (ordered oldest-first).
        var uploads: [PlannedUpload] = []
        var skipped = 0
        for res in library.sorted(by: order) {
            guard let rec = serverByKey[res.key.encoded] else {
                uploads.append(PlannedUpload(resource: res, mode: .create)); continue
            }
            switch rec.status {
            case .uploading:   uploads.append(PlannedUpload(resource: res, mode: .resume(uploadID: rec.id)))
            case .backendLost: uploads.append(PlannedUpload(resource: res, mode: .recover(uploadID: rec.id)))
            case .complete, .completing, .deleted: skipped += 1
            }
        }

        // Delete side — server records absent from the library.
        var deletes: [PlannedDelete] = []
        for rec in server where !libraryKeys.contains(rec.localIdentifier) {
            switch rec.status {
            case .deleted, .completing:
                continue // already gone / mid-move — leave alone
            case .uploading, .complete, .backendLost:
                if case .dates(let window) = range {
                    guard let d = parseDate(rec.creationDate), window.contains(d) else { continue }
                }
                if let key = try? ResourceKey(parsing: rec.localIdentifier) {
                    deletes.append(PlannedDelete(uploadID: rec.id, key: key))
                }
            }
        }

        return SyncPlan(uploads: uploads, deletes: deletes, skipped: skipped)
    }

    static func order(_ a: AssetResource, _ b: AssetResource) -> Bool {
        if a.creationDate != b.creationDate { return a.creationDate < b.creationDate }
        return a.key.encoded < b.key.encoded
    }

    static func parseDate(_ s: String?) -> Date? {
        guard let s else { return nil }
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = withFraction.date(from: s) { return d }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: s)
    }
}
