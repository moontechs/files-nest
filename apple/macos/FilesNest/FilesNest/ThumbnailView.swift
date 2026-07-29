import SwiftUI

/// Shows a PHAsset thumbnail, or a gradient placeholder while loading / when the asset is absent.
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
            let loaded = await loader.thumbnail(for: id, size: CGSize(width: size * 2, height: size * 2))
            guard !Task.isCancelled else { return }   // item changed mid-load; don't overwrite with the stale image
            image = loaded
        }
    }
}
