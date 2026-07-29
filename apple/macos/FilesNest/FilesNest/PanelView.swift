import SwiftUI
import FilesNestCore

struct PanelView: View {
    @ObservedObject var model: AppModel
    @ObservedObject var settings: SettingsModel
    @State private var showingSettings = false
    @State private var showingFailed = false

    var body: some View {
        ZStack {
            if showingSettings {
                SettingsView(model: settings, onDone: { withAnimation(slide) { showingSettings = false } })
                    .transition(.move(edge: .trailing))
            } else if showingFailed {
                FailedItemsView(items: model.summary.failed,
                                onDone: { withAnimation(slide) { showingFailed = false } })
                    .transition(.move(edge: .trailing))
            } else {
                dashboard
                    .transition(.move(edge: .leading))
            }
        }
        .frame(width: 320)
        .clipped()
        .animation(slide, value: showingSettings)
        .animation(slide, value: showingFailed)
    }

    private var slide: Animation { .easeInOut(duration: 0.28) }

    private var dashboard: some View {
        VStack(spacing: 0) {
            hero
            if case let .syncing(p) = model.status, p.total > 0 { currentItem(p) }
            tiles
            actions
            Divider()
            footer
        }
        .frame(width: 320)
    }

    // MARK: hero ring + status text
    private var hero: some View {
        VStack(spacing: 8) {
            ZStack {
                Circle().stroke(.quaternary, lineWidth: 6).frame(width: 74, height: 74)
                if showsIndeterminateSpinner {
                    ProgressView().controlSize(.large)   // indeterminate: no known total yet
                } else {
                    Circle().trim(from: 0, to: ringFraction)
                        .stroke(ringColor, style: .init(lineWidth: 6, lineCap: .round))
                        .rotationEffect(.degrees(-90)).frame(width: 74, height: 74)
                    Text(glyph).font(.system(size: 24))
                }
            }
            Text(title).font(.headline)
            Text(subtitle).font(.caption).foregroundStyle(.secondary)
        }
        .padding(.top, 18).padding(.bottom, 10).frame(maxWidth: .infinity)
    }

    private func currentItem(_ p: SyncProgress) -> some View {
        HStack(spacing: 10) {
            RoundedRectangle(cornerRadius: 6)
                .fill(LinearGradient(colors: [.blue.opacity(0.5), .purple.opacity(0.5)],
                                     startPoint: .topLeading, endPoint: .bottomTrailing))
                .frame(width: 34, height: 34)
            VStack(alignment: .leading, spacing: 2) {
                Text(p.currentItemName ?? "…").font(.caption).bold().lineLimit(1)
                Text("Uploading · \(p.completed) of \(p.total)")
                    .font(.caption2).foregroundStyle(.secondary)
                ProgressView(value: p.fraction).controlSize(.mini)
            }
        }
        .padding(8).background(.quaternary, in: RoundedRectangle(cornerRadius: 10))
        .padding(.horizontal, 12).padding(.bottom, 8)
    }

    private var tiles: some View {
        HStack(spacing: 8) {
            tile(backedUpText, "Backed up", .primary)
            tile(pendingText, "Pending", pending > 0 ? .orange : .primary)
            failedTile
        }.padding(.horizontal, 12).padding(.bottom, 8)
    }

    @ViewBuilder private var failedTile: some View {
        let count = model.summary.failed.count
        if isSignedOut {
            tile("—", "Failed", .primary)
        } else if count > 0 {
            Button { withAnimation(slide) { showingFailed = true } } label: {
                tile("\(count)", "Failed", .orange)
            }.buttonStyle(.plain)
        } else {
            tile("0", "Failed", .primary)
        }
    }

    private var backedUpText: String { isSignedOut ? "—" : "\(model.summary.backedUp)" }

    private var pendingText: String {
        guard !isSignedOut else { return "—" }
        switch model.status {
        case .syncing, .paused: return "\(pending)"                 // exact for the active run
        default: return model.summary.pending.map { "\($0)" } ?? "—"   // exact at-rest count, or — until first assess
        }
    }

    private func tile(_ v: String, _ k: String, _ color: Color) -> some View {
        VStack(spacing: 1) {
            Text(v).font(.title3).bold().foregroundStyle(color)
            Text(k).font(.caption2).foregroundStyle(.secondary)
        }.frame(maxWidth: .infinity).padding(.vertical, 9)
        .background(.quaternary, in: RoundedRectangle(cornerRadius: 9))
    }

    private var actions: some View {
        HStack(spacing: 8) {
            Button(isPaused ? "Resume" : "Pause") { isPaused ? model.resume() : model.pause() }
                .disabled(isSignedOut || isCounting)   // a count isn't pausable work
            Button("Sync Now") { model.syncNow() }.buttonStyle(.borderedProminent)
                .disabled(isSignedOut)
        }.padding(.horizontal, 12).padding(.bottom, 4)
    }

    private var footer: some View {
        HStack {
            Button("Settings…") { showingSettings = true }
            Spacer()
            Button("Quit") { NSApp.terminate(nil) }
        }
        .buttonStyle(.link).font(.caption)
        .padding(.horizontal, 12).padding(.vertical, 9).background(.quaternary)
    }

    // MARK: derived
    private var isPaused: Bool { if case .paused = model.status { return true }; return false }
    private var isSignedOut: Bool { if case .signedOut = model.status { return true }; return false }
    private var pending: Int {
        switch model.status {
        case .syncing(let p): return max(0, p.total - p.completed)
        case .paused(let n): return n              // remaining work while paused
        default: return model.summary.pending ?? 0 // at rest: exact assessed count (0 until first count)
        }
    }

    private var isCounting: Bool { if case .counting = model.status { return true }; return false }

    /// Enumeration in progress with no known total yet: `.syncing` or `.counting` at total 0.
    private var showsIndeterminateSpinner: Bool {
        switch model.status {
        case .syncing(let p): return p.total == 0
        case .counting(_, let total): return total == 0
        default: return false
        }
    }

    private var ringFraction: CGFloat {
        switch model.status {
        case .syncing(let p): return CGFloat(p.fraction)
        case .counting(let done, let total): return total > 0 ? CGFloat(done) / CGFloat(total) : 0
        case .watching: return 1
        default: return 0
        }
    }
    private var ringColor: Color {
        switch model.status {
        case .syncing, .counting: return .blue
        case .paused: return .orange
        case .error: return .red
        default: return .green
        }
    }
    private var glyph: String {
        switch model.status {
        case .syncing, .counting: return ""
        case .paused: return "⏸"
        case .error: return "✕"
        case .signedOut: return "→"
        case .watching: return "✓"
        }
    }
    private var title: String {
        switch model.status {
        case .signedOut: return "Sign in in Settings"
        case .counting: return "Counting…"
        case .watching: return "Up to date"
        case .syncing: return "Syncing…"
        case .paused: return "Paused"
        case .error: return "Can't reach server"
        }
    }
    private var subtitle: String {
        switch model.status {
        case .signedOut: return "Add your server and credentials"
        case .counting(let done, let total):
            return total > 0 ? "\(done.formatted()) of \(total.formatted())" : "Scanning library…"
        case .watching(let last): return last.map { "Last sync \($0.formatted(.relative(presentation: .named)))" } ?? "Watching for new items"
        case .syncing(let p):
            if p.total == 0 { return "Scanning library…" }
            return "\(p.completed) of \(p.total)"
        case .paused(let n): return "\(n) items waiting"
        case .error(let m): return m
        }
    }
}
