# Design: Real photo thumbnails (sync strip + failed list)

**Date:** 2026-07-29
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (`SyncProgress.currentItemID`, `SyncCoordinator` progress) · `apple/macos/FilesNest` (`ThumbnailLoader`, `ThumbnailView`, `PanelView`/`FailedItemsView` wiring)
**Depends on:**
- `docs/design/20260728-panel-stats-failed-detail.md` (`FailedItem`, `FailedItemsView`, merged #9)
- `docs/design/20260726-photos-library-real-syncnow.md` (`SyncCoordinator` progress hook, `SyncProgress`, merged #7)

---

## 1. Purpose

The sync-strip "current item" thumbnail (`PanelView.currentItem`) is a blue/purple gradient placeholder carried over from the #6 mock — it never showed the real photo. Replace it with the actual image via `PHImageManager`, and add the same thumbnail to each `FailedItemsView` row so a failed item is recognizable at a glance.

---

## 2. Scope

**In this slice:**
- `apple/FilesNestCore`:
  - `SyncProgress.currentItemID: String?` — the PHAsset local identifier (§4.1).
  - `SyncCoordinator` populates it in the upload-progress callback (§4.2).
- `apple/macos/FilesNest`:
  - `ThumbnailLoader` — PhotoKit thumbnail fetch + in-memory cache (§5.1).
  - `ThumbnailView` — reusable SwiftUI view: image or gradient placeholder (§5.2).
  - `PanelView.currentItem` and `FailedItemsView` rows use it (§5.3).

**Deferred (out of scope):**
- Video / Live-Photo badge overlays on the thumbnail.
- Disk-persistent thumbnail cache (in-memory `NSCache` only).
- Thumbnails anywhere else.

---

## 3. Decisions (with forks considered)

1. **Pass the identifier string through `SyncProgress`, not the `ResourceKey`.** The view only needs the PHAsset id for `PHImageManager`; passing the string keeps `ResourceKey`'s encode/parse semantics out of the view layer. The failed list already carries `FailedItem.key.localIdentifier`, so only the sync strip needs the new field.
2. **On-demand load, no prefetch.** Sync items display briefly; a small `NSCache` covers repeats/retries. Prefetching upcoming items is complexity the UX doesn't need.
3. **The gradient becomes the placeholder.** Reused for loading, missing-asset (deleted / failed-delete), and signed-out states so the layout never jumps and there's always something to render.
4. **`.opportunistic` delivery mode.** `PHImageManager` returns a fast low-res image then a sharp one — right for a tiny, transient thumbnail. `.aspectFill` + a small `targetSize` (≈2× the display point size).
5. **One shared `ThumbnailLoader`, passed explicitly.** Created in `FilesNestApp`, handed to `PanelView` and down to `FailedItemsView`. Not a custom `@Environment`/`@Entry` (its default value would be a fresh instance — a stability pitfall the SwiftUI checklist calls out) and not a closure in the environment.

---

## 4. Core

### 4.1 `SyncProgress` (+ `currentItemID`)

```swift
public struct SyncProgress: Sendable, Equatable {
    public let completed: Int
    public let total: Int
    public let currentItemName: String?
    public let currentItemID: String?     // NEW — PHAsset local identifier, for the thumbnail
    public let bytesRemaining: Int64?

    public init(completed: Int, total: Int, currentItemName: String?,
                bytesRemaining: Int64?, currentItemID: String? = nil) { … }

    public var fraction: Double { total > 0 ? Double(completed) / Double(total) : 0 }
}
```

`currentItemID` is the **last** init parameter with a `= nil` default, so every existing `SyncProgress(completed:total:currentItemName:bytesRemaining:)` call site (engine initial `.syncing`, StubSyncEngine, tests) compiles unchanged.

### 4.2 `SyncCoordinator`

The upload-progress callback gains the id:

```swift
onProgress(SyncProgress(completed: uploaded.count,
                        total: uploadTotal,
                        currentItemName: item.resource.filename,
                        bytesRemaining: nil,
                        currentItemID: item.resource.key.localIdentifier))
```

No other Core change.

---

## 5. App

### 5.1 `ThumbnailLoader`

```swift
final class ThumbnailLoader: @unchecked Sendable {
    private let cache = NSCache<NSString, NSImage>()
    private let manager = PHImageManager.default()

    /// Returns the asset's thumbnail, or nil if the asset is gone (deleted / not in library).
    func thumbnail(for id: String, size: CGSize) async -> NSImage?
}
```

- Cache key: the `id` (one display size in this slice). Hit → return synchronously via the async wrapper.
- `PHAsset.fetchAssets(withLocalIdentifiers: [id], options: nil)` → `firstObject`; nil → return nil (caller shows the placeholder).
- `PHImageManager.requestImage(for:targetSize:contentMode:.aspectFill, options:)` with `deliveryMode = .opportunistic`, `isSynchronous = false`, `resizeMode = .fast`, wrapped in `withCheckedContinuation` — resume once (guard against `.opportunistic`'s multiple callbacks; cache + resume on the first non-degraded image, or the first image if only one arrives). Store in cache before returning.
- No Photos-authorization prompt here — the app already has access (it scans the library); if not authorized, fetch returns empty → nil → placeholder.

### 5.2 `ThumbnailView`

```swift
struct ThumbnailView: View {
    let id: String?
    let size: CGFloat
    let loader: ThumbnailLoader
    @State private var image: NSImage?

    var body: some View {
        Group {
            if let image { Image(nsImage: image).resizable().scaledToFill() }
            else { placeholderGradient }        // loading / nil / missing asset
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .task(id: id) {                          // reload when the item changes
            image = nil
            guard let id else { return }
            image = await loader.thumbnail(for: id, size: CGSize(width: size * 2, height: size * 2))
        }
    }
}
```

`placeholderGradient` is the existing blue/purple `LinearGradient` rectangle. `@State private var image` (owned, private — SwiftUI checklist).

### 5.3 Wiring

- `FilesNestApp` creates one `let thumbnails = ThumbnailLoader()` and passes it: `PanelView(model:settings:thumbnails:)`.
- `PanelView.currentItem` replaces its gradient `RoundedRectangle` with `ThumbnailView(id: p.currentItemID, size: 34, loader: thumbnails)`.
- `PanelView` passes `thumbnails` into `FailedItemsView`; each row gains a leading `ThumbnailView(id: item.key.localIdentifier, size: 34, loader: thumbnails)`.

---

## 6. Testing

- **Core (`swift test`):**
  - `SyncProgress` carries `currentItemID` (equality of two values differing only in the id; default is nil).
  - `SyncCoordinator` progress callback reports `currentItemID == item.resource.key.localIdentifier` (extend an existing progress-capturing coordinator test, or add one that records the progress values).
- **App (manual verify):** real photo appears in the sync strip and on failed rows; a deleted/missing asset falls back to the gradient; scrolling the failed list stays smooth. Documented in a `docs/plans/…-verification.md` checklist.

---

## 7. Deferred / out of scope

- Video / Live-Photo indicator overlay.
- Disk-persistent thumbnail cache.
- Thumbnails in any other surface.

---

## 8. Open items (resolve during implementation)

1. **`.opportunistic` multi-callback handling (§5.1).** Ensure the `withCheckedContinuation` resumes exactly once (PHImageManager may call the handler multiple times for opportunistic delivery). Resume on the first image; let a later higher-res callback update the cache only.
2. **Thumbnail display size (§5.3).** 34pt matches the current strip square; confirm the failed-row size reads well next to two lines of text (34pt likely fine).
