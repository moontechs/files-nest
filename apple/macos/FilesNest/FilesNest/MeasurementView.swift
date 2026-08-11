import SwiftUI
import Photos
import FilesNestCore

struct MeasurementView: View {
    @StateObject private var runner = MeasurementRunner()
    @State private var localIdentifier = ""
    @State private var authorized = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("PhotoKit backpressure measurement").font(.headline)
            Text("Use a large, iCloud-only video (Optimize Mac Storage; evicted locally).")
                .font(.caption).foregroundStyle(.secondary)

            if !authorized {
                Button("Request Photos access") {
                    PHPhotoLibrary.requestAuthorization(for: .readWrite) { status in
                        Task { @MainActor in
                            authorized = (status == .authorized || status == .limited)
                        }
                    }
                }
            }

            TextField("localIdentifier of the video asset", text: $localIdentifier)
                .textFieldStyle(.roundedBorder)

            Button(runner.running ? "Measuring…" : "Measure (mid-stream stalls)") {
                Task { await runner.run(localIdentifier: localIdentifier, kind: .video, timestamp: Date()) }
            }
            .disabled(localIdentifier.isEmpty || runner.running)

            ScrollView {
                Text(runner.log.joined(separator: "\n"))
                    .font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding()
        .frame(minWidth: 560, minHeight: 440)
    }
}
