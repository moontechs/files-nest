import SwiftUI
import FilesNestCore

/// Slide-in list of items that failed in the last sync (filename + reason).
/// Mirrors SettingsView's in-panel navigation (Back button, 320-wide).
struct FailedItemsView: View {
    let items: [FailedItem]
    let thumbnails: ThumbnailLoader
    var onDone: () -> Void
    var onRetry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Button("Back", systemImage: "chevron.left") { onDone() }
                    .buttonStyle(.link)
                    .accessibilityIdentifier("failed.back")
                Spacer()
            }
            Text("Items that need attention").font(.title3.weight(.semibold))

            if items.isEmpty {
                ContentUnavailableView("No failed items", systemImage: "checkmark.circle",
                                        description: Text("Your last backup completed without failures."))
            } else {
                Text("FilesNest will retry these items the next time it syncs.")
                    .font(.caption).foregroundStyle(.secondary)
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 10) {   // only visible rows load thumbnails
                        ForEach(items, id: \.key.encoded) { item in
                            HStack(spacing: 10) {
                                ThumbnailView(id: item.key.localIdentifier, size: 34, loader: thumbnails)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(item.filename).font(.caption).bold().lineLimit(1)
                                    Text(item.reason).font(.caption2)
                                        .foregroundStyle(.secondary).lineLimit(2)
                                }.frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .accessibilityIdentifier("failed.item.\(item.key.encoded)")
                        }
                    }
                }.frame(maxHeight: 190)
                HStack {
                    Spacer()
                    Button("Try Again") { onRetry() }
                        .buttonStyle(.borderedProminent)
                        .accessibilityIdentifier("failed.retry")
                }
            }
        }
        .padding(16).frame(width: 320)
    }
}
