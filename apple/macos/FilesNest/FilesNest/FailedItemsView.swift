import SwiftUI
import FilesNestCore

/// Slide-in list of items that failed in the last sync (filename + reason).
/// Mirrors SettingsView's in-panel navigation (Back button, 320-wide).
struct FailedItemsView: View {
    let items: [FailedItem]
    var onDone: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Button { onDone() } label: { Label("Back", systemImage: "chevron.left") }
                    .buttonStyle(.link)
                Spacer()
            }
            Text("Failed items").font(.headline)

            if items.isEmpty {
                Text("No failures").font(.caption).foregroundStyle(.secondary)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        ForEach(items, id: \.key.encoded) { item in
                            VStack(alignment: .leading, spacing: 2) {
                                Text(item.filename).font(.caption).bold().lineLimit(1)
                                Text(item.reason).font(.caption2)
                                    .foregroundStyle(.secondary).lineLimit(2)
                            }.frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                }.frame(maxHeight: 220)
            }
        }
        .padding(16).frame(width: 320)
    }
}
