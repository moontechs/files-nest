# Real Photo Thumbnails — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Show the actual photo (via `PHImageManager`) in the sync-strip current item and each failed-list row, replacing the gradient placeholder.

**Architecture:** One Core change — `SyncProgress` carries the PHAsset local id, populated by `SyncCoordinator`. The app adds a shared `ThumbnailLoader` (PhotoKit + `NSCache`) and a reusable `ThumbnailView` (gradient as the loading/fallback state). Design: `docs/design/20260729-sync-thumbnails.md`.

**Tech Stack:** Swift 6, swift-testing, PhotoKit + SwiftUI (app target).

## Global Constraints

- Swift 6; `FilesNestCore` stays pure Foundation (no PhotoKit/SwiftUI).
- macOS app target is file-system-synchronized — drop `.swift` files in, no `.pbxproj` edits.
- Core test command: `cd /Users/paulohenriquesg/Projects/filesnest/files-nest/apple/FilesNestCore && swift test`.
- App build: `cd /Users/paulohenriquesg/Projects/filesnest/files-nest && xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO build`.
- SwiftUI checklist: `@State` private; passed-in objects not `@State`; `.task(id:)` for reload.
- Standing rule: Codex review before finishing (Task 4).

---

### Task 1: Core — `SyncProgress.currentItemID` + coordinator populates it

**Files:**
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncStatus.swift` (`SyncProgress`)
- Modify: `apple/FilesNestCore/Sources/FilesNestCore/SyncCoordinator.swift`
- Test: `apple/FilesNestCore/Tests/FilesNestCoreTests/SyncCoordinatorTests.swift`

**Interfaces:**
- Produces: `SyncProgress.currentItemID: String?` (PHAsset local id); coordinator sets it to `item.resource.key.localIdentifier`.

- [x] **Step 1: Update the failing test** — `progressFiresOncePerUploadInPlanOrder` expects the id:

```swift
#expect(box.values == [
    SyncProgress(completed: 0, total: 2, currentItemName: "A.jpg", bytesRemaining: nil, currentItemID: "A"),
    SyncProgress(completed: 1, total: 2, currentItemName: "B.jpg", bytesRemaining: nil, currentItemID: "B"),
])
```

- [x] **Step 2: Run it — verify fail** — `swift test --filter progressFiresOncePerUploadInPlanOrder` → FAIL (unknown arg `currentItemID`, then value mismatch once the field exists).

- [x] **Step 3: Add the field to `SyncProgress`** (last init param, defaulted):

```swift
public let currentItemName: String?
public let currentItemID: String?     // PHAsset local identifier, for the thumbnail
public let bytesRemaining: Int64?

public init(completed: Int, total: Int, currentItemName: String?,
            bytesRemaining: Int64?, currentItemID: String? = nil) {
    self.completed = completed
    self.total = total
    self.currentItemName = currentItemName
    self.currentItemID = currentItemID
    self.bytesRemaining = bytesRemaining
}
```

- [x] **Step 4: Populate it in `SyncCoordinator`** — the upload-progress callback:

```swift
onProgress(SyncProgress(completed: uploaded.count,
                        total: uploadTotal,
                        currentItemName: item.resource.filename,
                        bytesRemaining: nil,
                        currentItemID: item.resource.key.localIdentifier))
```

- [x] **Step 5: Run the suite** — `swift test` → PASS (all existing `SyncProgress(...)` sites compile via the default; the updated test passes).

- [x] **Step 6: Commit** — `git commit -m "feat(core): SyncProgress.currentItemID (PHAsset id for thumbnails)"`.

---

### Task 2: App — `ThumbnailLoader`

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/ThumbnailLoader.swift`

**Interfaces:**
- Produces: `final class ThumbnailLoader` with `func thumbnail(for id: String, size: CGSize) async -> NSImage?`.

- [x] **Step 1: Create the loader:**

```swift
import AppKit
import Photos

/// Loads PHAsset thumbnails for the panel, with an in-memory cache. Returns nil when the
/// asset is missing (deleted / not in the library) so callers show the placeholder.
final class ThumbnailLoader: @unchecked Sendable {
    private let cache = NSCache<NSString, NSImage>()
    private let manager = PHImageManager.default()

    func thumbnail(for id: String, size: CGSize) async -> NSImage? {
        if let hit = cache.object(forKey: id as NSString) { return hit }
        guard let asset = PHAsset.fetchAssets(withLocalIdentifiers: [id], options: nil).firstObject else {
            return nil
        }
        let options = PHImageRequestOptions()
        options.deliveryMode = .opportunistic
        options.resizeMode = .fast
        options.isNetworkAccessAllowed = true
        options.isSynchronous = false

        return await withCheckedContinuation { (cont: CheckedContinuation<NSImage?, Never>) in
            let resumed = ResumeOnce()
            manager.requestImage(for: asset, targetSize: size, contentMode: .aspectFill, options: options) { image, info in
                // .opportunistic may call back twice (degraded then full). Cache the latest; resume once.
                if let image { self.cache.setObject(image, forKey: id as NSString) }
                let isDegraded = (info?[PHImageResultIsDegradedKey] as? Bool) ?? false
                if !isDegraded, resumed.tryResume() { cont.resume(returning: image) }
            }
        }
    }
}

/// One-shot guard so a multi-callback PHImageManager request resumes its continuation exactly once.
private final class ResumeOnce: @unchecked Sendable {
    private let lock = NSLock(); private var done = false
    func tryResume() -> Bool { lock.lock(); defer { lock.unlock() }; if done { return false }; done = true; return true }
}
```

- [x] **Step 2: Build the app** — app builds (loader unused yet). BUILD SUCCEEDED.

- [x] **Step 3: Commit** — `git commit -m "feat(app): ThumbnailLoader (PHImageManager + NSCache)"`.

---

### Task 3: App — `ThumbnailView` + wire into sync strip & failed list

**Files:**
- Create: `apple/macos/FilesNest/FilesNest/ThumbnailView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/PanelView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FailedItemsView.swift`
- Modify: `apple/macos/FilesNest/FilesNest/FilesNestApp.swift`
- Create: `docs/plans/20260729-sync-thumbnails-verification.md`

**Interfaces:**
- Consumes: `ThumbnailLoader` (Task 2), `SyncProgress.currentItemID` (Task 1), `FailedItem.key.localIdentifier`.

- [x] **Step 1: Create `ThumbnailView`:**

```swift
import SwiftUI

/// Shows a PHAsset thumbnail, or a gradient placeholder while loading / when absent.
struct ThumbnailView: View {
    let id: String?
    let size: CGFloat
    let loader: ThumbnailLoader
    @State private var image: NSImage?

    var body: some View {
        Group {
            if let image {
                Image(nsImage: image).resizable().scaledToFill()
            } else {
                RoundedRectangle(cornerRadius: 6)
                    .fill(LinearGradient(colors: [.blue.opacity(0.5), .purple.opacity(0.5)],
                                         startPoint: .topLeading, endPoint: .bottomTrailing))
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .task(id: id) {
            image = nil
            guard let id else { return }
            image = await loader.thumbnail(for: id, size: CGSize(width: size * 2, height: size * 2))
        }
    }
}
```

- [x] **Step 2: Thread the loader from `FilesNestApp`** — add `let thumbnails = ThumbnailLoader()` in `init()` and pass it: `PanelView(model: model, settings: settings, thumbnails: thumbnails)`.

- [x] **Step 3: `PanelView`** — add `let thumbnails: ThumbnailLoader` stored property; in `currentItem(_:)` replace the gradient `RoundedRectangle(...).frame(width: 34, height: 34)` with:

```swift
ThumbnailView(id: p.currentItemID, size: 34, loader: thumbnails)
```

and pass the loader into the failed branch: `FailedItemsView(items: model.summary.failed, thumbnails: thumbnails, onDone: { … })`.

- [x] **Step 4: `FailedItemsView`** — add `let thumbnails: ThumbnailLoader`; give each row a leading thumbnail:

```swift
HStack(spacing: 10) {
    ThumbnailView(id: item.key.localIdentifier, size: 34, loader: thumbnails)
    VStack(alignment: .leading, spacing: 2) {
        Text(item.filename).font(.caption).bold().lineLimit(1)
        Text(item.reason).font(.caption2).foregroundStyle(.secondary).lineLimit(2)
    }.frame(maxWidth: .infinity, alignment: .leading)
}
```

- [x] **Step 5: Build the app** — BUILD SUCCEEDED.

- [x] **Step 6: Write the manual-verification checklist** — `docs/plans/20260729-sync-thumbnails-verification.md`: real photo shows in the sync strip during a sync; failed rows show thumbnails; a deleted/missing asset falls back to the gradient; failed-list scrolling stays smooth.

- [x] **Step 7: Commit** — `git commit -m "feat(app): real photo thumbnails in sync strip + failed list"`.

---

### Task 4: Verify, review, finish

- [x] **Step 1: Hammer Core** — `for i in 1 2 3; do perl -e 'alarm 240; exec @ARGV' swift test; done` → green.
- [x] **Step 2: Build app** → BUILD SUCCEEDED.
- [x] **Step 3: Push** — `git push -u origin apple/sync-thumbnails`.
- [x] **Step 4: Codex review** — scoped `codex exec` covering: `SyncProgress.currentItemID` threading + coordinator population; `ThumbnailLoader` continuation-resumes-once under `.opportunistic` multi-callback, cache correctness, Sendable; `ThumbnailView` `.task(id:)` reload + `@State` ownership; graceful nil/placeholder fallback. Address findings.
- [x] **Step 5: Manual UI verification** — walk the Task 3 Step 6 checklist on the real app.
- [x] **Step 6: Finish** — superpowers:finishing-a-development-branch → PR `Apple clients: Sync thumbnails (#N)`; merge on approval; update `active-codebase` memory.

---

## Self-Review

- **Spec coverage:** `SyncProgress.currentItemID` (T1), coordinator populate (T1), `ThumbnailLoader` (T2), `ThumbnailView` + both call sites + wiring (T3), tests + manual checklist, deferrals untouched. Covered.
- **Placeholder scan:** none — concrete code throughout; `.opportunistic` resume-once handled by `ResumeOnce`.
- **Type consistency:** `SyncProgress(…, currentItemID:)`, `ThumbnailLoader.thumbnail(for:size:)`, `ThumbnailView(id:size:loader:)` used identically across tasks.
