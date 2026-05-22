import SwiftUI
import WebKit

// MARK: - ManualView
//
// A sheet that renders the server's user manual. The server exposes
// GET /manual which returns a complete self-contained HTML page (the
// goldmark-rendered MANUAL.md wrapped in a minimal HTML document with
// inline CSS and a prefers-color-scheme dark-mode rule). We fetch that
// HTML via APIClient — so it travels over the same TLS-trusting session
// that all the other API calls use — and hand the bytes to a WKWebView
// via loadHTMLString. No markdown-to-HTML conversion is needed on the
// client side; the server already did it.
//
// A search bar above the WKWebView lets the user jump to occurrences of
// a query string, using WKWebView's built-in find API (iOS 14+ / macCatalyst
// 14+) which highlights matches and scrolls them into view. Cmd+G / Cmd+Shift+G
// step forward and backward through matches on Mac Catalyst.

struct ManualView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    // `.idle` until the .task fires, then either `.loaded(html)` or `.failed(msg)`.
    private enum LoadState {
        case idle
        case loading
        case loaded(String)
        case failed(String)
    }
    @State private var state: LoadState = .idle

    // Bridge to the underlying WKWebView. ManualWebView assigns its WKWebView
    // into `bridge.webView` from makeUIView, so the search bar can invoke
    // WKWebView.find() against the same instance the sheet is displaying.
    @StateObject private var bridge = WebViewBridge()
    @State private var searchText: String = ""
    @State private var noMatches: Bool = false

    var body: some View {
        // NavigationStack (not the deprecated NavigationView) — on Mac Catalyst,
        // NavigationView wraps content in a split-view container that does not
        // reliably forward EnvironmentObjects through to its inner content
        // when the parent is itself a sheet, which manifests as a runtime
        // "No ObservableObject of type AppState found" crash. NavigationStack
        // has no such issue.
        NavigationStack {
            content
                .navigationTitle("\(appState.appName) Help")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Done") { dismiss() }
                    }
                }
        }
        // `.task` runs the closure when the view first appears and
        // cancels it if the view disappears before completion.
        .task { await load() }
    }

    @ViewBuilder
    private var content: some View {
        switch state {
        case .idle, .loading:
            ProgressView("Loading manual…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)

        case .loaded(let html):
            VStack(spacing: 0) {
                searchBar
                Divider()
                // baseURL is nil because the rendered page is self-contained
                // (inline CSS, no external resources). Passing the server URL
                // would only matter if we were going to follow relative links.
                ManualWebView(html: html, bridge: bridge)
            }

        case .failed(let message):
            VStack(spacing: 12) {
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 36))
                    .foregroundColor(.orange)
                Text("Could not load the manual")
                    .font(.headline)
                Text(message)
                    .font(.callout)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                Button("Retry") {
                    Task { await load() }
                }
                .buttonStyle(.borderedProminent)
            }
            .padding()
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: - Search bar

    private var searchBar: some View {
        HStack(spacing: 10) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(.secondary)

            TextField("Find in manual", text: $searchText)
                .textFieldStyle(.plain)
                .submitLabel(.search)
                .autocorrectionDisabled(true)
                .textInputAutocapitalization(.never)
                .onSubmit { findNext() }
                // iOS 16 single-argument onChange form — the two-argument
                // (oldValue, newValue) variant requires iOS 17.
                .onChange(of: searchText) { _ in noMatches = false }

            if !searchText.isEmpty {
                Button {
                    searchText = ""
                    noMatches = false
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }

            if noMatches {
                Text("No matches")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }

            Button {
                findPrevious()
            } label: {
                Image(systemName: "chevron.up")
            }
            .keyboardShortcut("g", modifiers: [.command, .shift])
            .disabled(searchText.isEmpty)
            .accessibilityLabel("Find previous")

            Button {
                findNext()
            } label: {
                Image(systemName: "chevron.down")
            }
            .keyboardShortcut("g", modifiers: .command)
            .disabled(searchText.isEmpty)
            .accessibilityLabel("Find next")
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .background(.bar)
    }

    // MARK: - Actions

    private func findNext() {
        let q = searchText
        guard !q.isEmpty else { return }
        Task {
            let found = await bridge.find(q, backwards: false)
            noMatches = !found
        }
    }

    private func findPrevious() {
        let q = searchText
        guard !q.isEmpty else { return }
        Task {
            let found = await bridge.find(q, backwards: true)
            noMatches = !found
        }
    }

    private func load() async {
        state = .loading
        do {
            let html = try await appState.api.getManual()
            state = .loaded(html)
        } catch {
            state = .failed(error.localizedDescription)
        }
    }
}

// MARK: - WebViewBridge
//
// A tiny ObservableObject that holds a weak reference to the WKWebView
// inside ManualWebView. The bridge gives the SwiftUI search UI a stable
// handle to invoke WKWebView.find() — UIViewRepresentable doesn't expose
// its underlying UIView to its host view, so we explicitly route a
// reference back up. `weak` avoids a retain cycle if the WKWebView
// outlives the sheet for any reason.

@MainActor
final class WebViewBridge: ObservableObject {
    weak var webView: WKWebView?

    func find(_ query: String, backwards: Bool) async -> Bool {
        guard let webView, !query.isEmpty else { return false }
        // Use the JavaScript window.find() API instead of WKWebView.find().
        // The native find call returns matchFound=true but on Mac Catalyst it
        // does not reliably scroll the viewport to the match — window.find
        // both selects the text AND scrolls it into view, which is the
        // behaviour the user expects.
        //
        // Arguments to window.find():
        //   1. search string
        //   2. caseSensitive (false → case-insensitive)
        //   3. backwards (true → previous, false → next)
        //   4. wrapAround (true → continue from the top after the last hit)
        //   5. wholeWord (false → match anywhere inside a word)
        //   6. searchInFrames (true)
        //   7. showDialog (false → no built-in find dialog)
        let escaped = query
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "'",  with: "\\'")
            .replacingOccurrences(of: "\n", with: " ")
            .replacingOccurrences(of: "\r", with: " ")
        let js = "window.find('\(escaped)', false, \(backwards), true, false, true, false)"
        let result = try? await webView.evaluateJavaScript(js)
        return (result as? Bool) ?? false
    }
}

// MARK: - WKWebView wrapper
//
// SwiftUI doesn't have a native web-view component (yet), so we bridge
// WKWebView via UIViewRepresentable. UIViewRepresentable is the iOS /
// Mac Catalyst equivalent of NSViewRepresentable on AppKit — Catalyst
// is UIKit under the hood, so this works on both targets.
//
// Loading happens once in makeUIView. SwiftUI may call updateUIView
// many times for layout reasons; reloading on each of those calls
// would clear scroll position and feel janky, so updateUIView is a
// no-op. If the html ever needed to change for the same WKWebView
// instance we'd need a Coordinator to diff old vs. new — but for a
// sheet that fetches once and displays the result, this is enough.

struct ManualWebView: UIViewRepresentable {
    let html: String
    let bridge: WebViewBridge

    func makeUIView(context: Context) -> WKWebView {
        let config  = WKWebViewConfiguration()
        let webView = WKWebView(frame: .zero, configuration: config)
        webView.isOpaque = false
        webView.backgroundColor = .clear
        webView.scrollView.backgroundColor = .clear
        webView.loadHTMLString(html, baseURL: nil)
        // Register the WKWebView with the search bridge so the toolbar's
        // Find buttons can call WKWebView.find() against this exact view.
        bridge.webView = webView
        return webView
    }

    func updateUIView(_ uiView: WKWebView, context: Context) { /* no-op */ }
}
