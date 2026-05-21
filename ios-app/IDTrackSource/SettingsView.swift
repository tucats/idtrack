import SwiftUI

// MARK: - SettingsView
//
// User-configurable preferences. Changes take effect immediately because the
// form controls bind directly to @Published properties on AppState — SwiftUI
// propagates the change to all views that observe those properties as soon as
// the user interacts with a control.

struct SettingsView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    // The allowed page sizes; presented in a Picker.
    private let pageSizes = [10, 25, 50, 100, 200]

    var body: some View {
        NavigationStack {
            Form {
                Section("Appearance") {
                    // `$appState.darkMode` is a two-way binding to the @Published
                    // property. Toggle reads it to show the current state and
                    // writes to it when the user flips the switch. The didSet
                    // observer in AppState persists the change to UserDefaults.
                    Toggle("Dark Mode", isOn: $appState.darkMode)
                }

                Section("Session") {
                    Toggle("Keep Me Logged In", isOn: $appState.keepLoggedIn)
                    // Only show the idle timeout row when the server has configured
                    // a non-zero timeout. A value of 0 means "no timeout" per the API.
                    if appState.idleTimeout > 0 {
                        HStack {
                            Text("Idle Timeout")
                            Spacer()
                            // `idleLabel` (below) formats the raw seconds into a
                            // human-readable string like "30m" or "1h".
                            Text(idleLabel)
                                .foregroundStyle(.secondary)
                        }
                    }
                }

                Section("Issues") {
                    // Binding directly to appState.pageSize means the
                    // IssueListView respects the new value on the next reload.
                    Picker("Issues Per Page", selection: $appState.pageSize) {
                        ForEach(pageSizes, id: \.self) { n in
                            // `\.self` as the `id` parameter tells ForEach to
                            // use the Int value itself as the item's identity.
                            Text("\(n)").tag(n)
                        }
                    }
                }

                Section("Server") {
                    HStack {
                        Text("URL")
                        Spacer()
                        Text(appState.serverURL)
                            .foregroundStyle(.secondary)
                            .font(.callout)
                            .lineLimit(1)
                            // `.middle` truncation shows the start and end of long
                            // URLs, which is more informative than truncating the end.
                            .truncationMode(.middle)
                    }
                    // "Change Server" is destructive because it signs the user out.
                    // The `role: .destructive` parameter makes iOS render it in red.
                    Button("Change Server…", role: .destructive) {
                        // Dismiss the settings sheet first, then — after a short
                        // delay to let the dismiss animation complete — sign out
                        // and clear the server URL. Without the delay, clearing
                        // serverURL while the sheet is still animating can cause
                        // a visual glitch as ContentView tries to show a new screen.
                        dismiss()
                        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                            Task {
                                await appState.signOut()
                                appState.serverURL = ""
                                // Clearing serverURL causes ContentView to navigate
                                // to ServerConfigView automatically.
                            }
                        }
                    }
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    // MARK: - Idle timeout label
    //
    // Converts a raw second count into a concise string:
    //   3600 → "1h"
    //   1800 → "30m"
    //   90   → "90s"
    //
    // `private var` — a computed property; recalculated each time the view renders.
    private var idleLabel: String {
        let s = appState.idleTimeout
        if s >= 3600 { return "\(s / 3600)h" }
        if s >= 60   { return "\(s / 60)m" }
        return "\(s)s"
    }
}
