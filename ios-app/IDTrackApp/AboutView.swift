import SwiftUI

// MARK: - AboutView
//
// A simple "About" screen that displays app branding and the server's version
// string. The version is fetched from GET /api/version when the view appears
// and formatted into a human-readable build timestamp.

struct AboutView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    // Start with placeholder dashes until the version loads.
    @State private var version   = "—"
    @State private var buildTime = ""

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                Spacer()

                Image(systemName: "list.bullet.clipboard.fill")
                    .font(.system(size: 72))
                    .foregroundStyle(.tint)

                VStack(spacing: 6) {
                    // appName and appDesc come from AppState, which receives
                    // them from GET /api/status during boot. Custom server
                    // deployments can brand these to their own product name.
                    Text(appState.appName)
                        .font(.largeTitle.bold())
                    Text(appState.appDesc)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                    Text("Version \(version)")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                    // Only show the build time row when we have the data.
                    if !buildTime.isEmpty {
                        Text("Built \(buildTime)")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    }
                }

                VStack(spacing: 12) {
                    // `Link` is a SwiftUI control that opens a URL in the default
                    // browser. The `URL(string:)!` force-unwrap is safe here
                    // because the literal is a compile-time constant we control.
                    Link(destination: URL(string: "https://github.com/tucats/idtrack")!) {
                        Label("github.com/tucats/idtrack", systemImage: "link")
                            .font(.callout)
                    }
                }

                Spacer()

                Button("Done") { dismiss() }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .padding(.bottom, 32)
            }
            .frame(maxWidth: .infinity)
            .navigationTitle("About")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
        // `.task` fires when the view appears and is automatically cancelled
        // if the view disappears before the async call finishes.
        .task { await loadVersion() }
    }

    // MARK: - Version loading

    private func loadVersion() async {
        do {
            let resp = try await appState.api.getVersion()
            // `??` returns "—" when version is nil (server didn't include it).
            version = resp.version ?? "—"

            // The server encodes build time as a 14-character compact UTC string:
            //   "20260516120000"  →  year=2026, month=05, day=16, hour=12, min=00, sec=00
            //
            // `String.prefix(_:)` returns a Substring of at most N characters.
            // `String.dropFirst(_:)` skips the first N characters.
            // Chaining them extracts specific slices without computing character indices.
            if let bt = resp.buildTime, bt.count == 14 {
                let y  = String(bt.prefix(4))           // "2026"
                let mo = String(bt.dropFirst(4).prefix(2))  // "05"
                let d  = String(bt.dropFirst(6).prefix(2))  // "16"
                let h  = String(bt.dropFirst(8).prefix(2))  // "12"
                let m  = String(bt.dropFirst(10).prefix(2)) // "00"
                let s  = String(bt.dropFirst(12).prefix(2)) // "00"
                buildTime = "\(y)-\(mo)-\(d) \(h):\(m):\(s) UTC"
            } else {
                // If format is unexpected, show the raw string.
                buildTime = resp.buildTime ?? ""
            }
        } catch {}   // version display failure is non-fatal; placeholders remain
    }
}
