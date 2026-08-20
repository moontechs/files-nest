import SwiftUI
import FilesNestCore

struct PanelView: View {
    @ObservedObject var model: AppModel
    @ObservedObject var settings: SettingsModel
    let thumbnails: ThumbnailLoader
    @State private var showingSettings = false
    @State private var showingFailed = false

    var body: some View {
        ZStack {
            // Keep the dashboard alive while a detail screen is visible. In particular, a
            // PhotoKit count can continue reporting progress without the dashboard being
            // reconstructed when the user goes to Settings and back.
            dashboard
                .opacity(showingSettings || showingFailed ? 0 : 1)
                .allowsHitTesting(!showingSettings && !showingFailed)
                .accessibilityHidden(showingSettings || showingFailed)

            if showingSettings {
                SettingsView(model: settings, onDone: { withAnimation(slide) { showingSettings = false } })
                    .transition(.move(edge: .trailing))
            } else if showingFailed {
                FailedItemsView(items: model.summary.failed,
                                thumbnails: thumbnails,
                                onDone: { withAnimation(slide) { showingFailed = false } },
                                onRetry: {
                                    model.syncNow()
                                    withAnimation(slide) { showingFailed = false }
                                })
                    .transition(.move(edge: .trailing))
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
            panelHeader
            hero
            if case let .syncing(p) = model.status, p.total > 0 { currentItem(p) }
            tiles
            actions
            Divider()
            footer
        }
        .frame(width: 320)
    }

    private var panelHeader: some View {
        HStack(spacing: 7) {
            Image(systemName: "externaldrive.badge.checkmark")
                .foregroundStyle(.tint)
            Text("FilesNest")
                .font(.headline)
            Spacer()
            Text(statusLabel)
                .font(.caption2.weight(.medium))
                .foregroundStyle(statusColor)
        }
        .padding(.horizontal, 16)
        .padding(.top, 14)
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
                    Image(systemName: stateIcon)
                        .font(.system(size: 23, weight: .semibold))
                        .foregroundStyle(ringColor)
                        .accessibilityHidden(true)
                }
            }
            Text(title).font(.title3.weight(.semibold))
            Text(subtitle).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                .multilineTextAlignment(.center)
        }
        .padding(.top, 16).padding(.bottom, 14).frame(maxWidth: .infinity)
    }

    private func currentItem(_ p: SyncProgress) -> some View {
        HStack(spacing: 10) {
            ThumbnailView(id: p.currentItemID, size: 34, loader: thumbnails)
            VStack(alignment: .leading, spacing: 2) {
                Text(p.currentItemName ?? "…").font(.caption).bold().lineLimit(1)
                Text(p.inFlight > 1
                     ? "Uploading \(p.inFlight) · \(p.completed) of \(p.total)"
                     : "Uploading · \(p.completed) of \(p.total)")
                    .font(.caption2).foregroundStyle(.secondary)
                ProgressView(value: p.fraction).controlSize(.mini)
            }
        }
        .padding(9).background(.quaternary, in: RoundedRectangle(cornerRadius: 10))
        .padding(.horizontal, 12).padding(.bottom, 8)
    }

    private var tiles: some View {
        VStack(spacing: 4) {
            HStack(spacing: 8) {
                tile(backedUpText, "Backed up", .primary)
                tile(pendingText, "Pending", pending > 0 ? .orange : .primary)
                failedTile
            }
            if let caption = backedUpProgressCaption {
                Text(caption).font(.caption2).foregroundStyle(.secondary)
            }
        }.padding(.horizontal, 12).padding(.bottom, 8)
    }

    /// "63,201 of 70,444 backed up" — shown once the library has been counted.
    private var backedUpProgressCaption: String? {
        guard !isSignedOut, let total = model.summary.resourceTotal else { return nil }
        // Hidden during the scan: the "Counting N of M" ring counts ASSETS, while this counts
        // RESOURCES (a Live Photo is 1 asset but 2 resources), so the two totals differ; and these
        // numbers are the stale cached values until the count lands. Show it only at rest / syncing.
        if case .counting = model.status { return nil }
        // Clamp the numerator: backedUp (server records) can briefly exceed resourceTotal
        // (local resources) after deletions, until the next .all reconcile.
        return "\(min(model.summary.backedUp, total).formatted()) of \(total.formatted()) backed up"
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

    private var backedUpText: String { isSignedOut ? "—" : model.summary.backedUp.formatted() }

    private var pendingText: String {
        guard !isSignedOut else { return "—" }
        switch model.status {
        case .syncing, .paused: return "\(pending)"                 // exact for the active run
        default: return model.summary.pending.map { "\($0)" } ?? "—"   // exact at-rest count, or — until first assess
        }
    }

    private func tile(_ v: String, _ k: String, _ color: Color) -> some View {
        VStack(spacing: 2) {
            Text(v).font(.title3.weight(.semibold)).monospacedDigit().foregroundStyle(color)
            Text(k).font(.caption2).foregroundStyle(.secondary)
        }.frame(maxWidth: .infinity).padding(.vertical, 10)
        .background(.quaternary, in: RoundedRectangle(cornerRadius: 9))
    }

    private var actions: some View {
        VStack(spacing: 0) {
            if isSignedOut {
                Button("Set Up FilesNest") { showingSettings = true }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .frame(maxWidth: .infinity, alignment: .center)
            } else if isError {
                HStack(spacing: 8) {
                    Button("Retry") { model.syncNow() }
                        .buttonStyle(.borderedProminent)
                    Button("Settings") { showingSettings = true }
                }
            } else {
                HStack(spacing: 8) {
                    Button(isPaused ? "Resume" : "Pause") { isPaused ? model.resume() : model.pause() }
                        .disabled(isCounting)
                    Button("Sync Now") { model.syncNow() }
                        .buttonStyle(.borderedProminent)
                        .disabled(isCounting)   // don't let Sync Now supersede an in-flight count
                }
            }
        }
        .padding(.horizontal, 12).padding(.bottom, 8)
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
    private var isError: Bool { if case .error = model.status { return true }; return false }
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
    private var stateIcon: String {
        switch model.status {
        case .syncing, .counting: return "arrow.triangle.2.circlepath"
        case .paused: return "pause.fill"
        case .error: return "exclamationmark"
        case .signedOut: return "server.rack"
        case .watching: return "checkmark"
        }
    }

    private var statusLabel: String {
        switch model.status {
        case .signedOut: return "Needs setup"
        case .counting: return "Checking"
        case .watching: return "Protected"
        case .syncing: return "Backing up"
        case .paused: return "Paused"
        case .error: return "Attention needed"
        }
    }

    private var statusColor: Color { ringColor }
    private var title: String {
        switch model.status {
        case .signedOut: return "Your backup needs a server"
        case .counting: return "Counting…"
        case .watching: return "Up to date"
        case .syncing: return "Syncing…"
        case .paused: return "Paused"
        case .error: return "Backup needs attention"
        }
    }
    private var subtitle: String {
        switch model.status {
        case .signedOut: return "Connect your own FilesNest server to begin"
        case .counting(let done, let total):
            return total > 0 ? "\(done.formatted()) of \(total.formatted())" : "Scanning library…"
        case .watching(let last): return last.map { "Last sync \($0.formatted(.relative(presentation: .named)))" } ?? "Watching for new items"
        case .syncing(let p):
            if p.total == 0 { return "Scanning library…" }
            return "\(p.completed) of \(p.total)"
        case .paused(let n): return "\(n) items waiting"
        case .error:
            return "Check that your server is online and the address is correct, then retry."
        }
    }
}
