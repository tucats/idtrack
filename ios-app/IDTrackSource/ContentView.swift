import SwiftUI

// MARK: - ContentView
//
// ContentView is the root of the visible view hierarchy. Its only job is to
// decide which "screen" to show based on app state. This is sometimes called
// a "router" view — it contains no real UI of its own.
//
// The decision tree:
//   1. Booting      → splash screen (app is fetching initial server status)
//   2. No server    → ServerConfigView (first launch or server was cleared)
//   3. Onboarding   → OnboardingView (server has no users yet)
//   4. Not logged in → LoginView
//   5. Logged in    → MainAppView

struct ContentView: View {
    // @EnvironmentObject — reads the AppState injected by IDTrackApp.
    // No need to pass it as a parameter; SwiftUI finds it in the environment.
    @EnvironmentObject var appState: AppState

    // Local state owned by this view.
    // `@State` tells SwiftUI to store the value across re-renders and to
    // re-render whenever the value changes.
    @State private var isBooting = true        // true while the boot() task runs
    @State private var bootError: String? = nil // non-nil if the initial status probe failed

    var body: some View {
        // `Group` is a transparent container — it lets us switch between
        // different views without adding any visual structure. SwiftUI needs a
        // single root view, so Group satisfies that requirement while acting
        // as a simple conditional branch.
        Group {
            if isBooting {
                BootView(error: bootError)
            } else if appState.serverURL.isEmpty {
                ServerConfigView()
            } else if appState.onboardingToken != nil {
                OnboardingView()
            } else if !appState.isLoggedIn {
                LoginView()
            } else {
                MainAppView()
            }
        }
        // `.task` fires the async closure when the view appears, on the main
        // actor. It automatically cancels the task if the view disappears before
        // the closure finishes — unlike DispatchQueue.main.async, which cannot
        // be cancelled.
        .task { await boot() }
    }

    // MARK: - Boot sequence
    //
    // boot() probes the server and restores any saved session before showing
    // the main UI. This produces a seamless re-launch experience: the user
    // arrives directly at the issue list rather than the login screen.

    private func boot() async {
        // If no server URL is configured yet, skip the network probe and
        // show ServerConfigView immediately.
        guard !appState.serverURL.isEmpty else {
            isBooting = false
            return
        }
        do {
            // GET /api/status: always succeeds even without a session.
            let status = try await appState.api.getStatus()

            // Apply server-supplied branding if present.
            if let n = status.appName,        !n.isEmpty { appState.appName = n }
            if let d = status.appDescription, !d.isEmpty { appState.appDesc = d }
            if let t = status.idleTimeout { appState.idleTimeout = t }

            // If the server has no users, jump straight to onboarding.
            // `ob` unwraps the optional Bool; if both checks pass, set the token.
            if let ob = status.onboarding, ob, let tok = status.token {
                appState.onboardingToken = tok
                isBooting = false
                return
            }

            // Attempt to restore a saved session (keepLoggedIn path).
            if let user = appState.loadPersistedUser() {
                appState.signIn(user: user)
                do {
                    // Pre-load users and projects so the main screen is ready
                    // immediately. If either call returns 401, the session has
                    // expired — sign out and show the login screen.
                    try await appState.refreshUsers()
                    try await appState.refreshProjects()
                } catch APIError.unauthorized {
                    await appState.signOut()
                } catch {}  // other errors are non-fatal; data loads lazily later
            }
        } catch {
            // A network or decoding failure during boot shows the error on the
            // splash screen. The user can tap the server button in LoginView
            // to reconfigure or retry.
            bootError = error.localizedDescription
        }
        isBooting = false
    }
}

// MARK: - Boot splash view
//
// Shown during the async boot() call. Displays a spinner while the server
// probe is in flight; swaps to an error message if boot() throws.
//
// `private` — only ContentView uses this; no reason to expose it.

private struct BootView: View {
    let error: String?   // nil = still loading; non-nil = something went wrong

    var body: some View {
        VStack(spacing: 20) {
            Image(systemName: "list.bullet.clipboard")
                .font(.system(size: 60))
                .foregroundStyle(.tint)   // adapts to the app's accent colour
            Text("idtrack")
                .font(.largeTitle.bold())
            if let err = error {
                // `if let` unwraps the Optional — the block only runs when
                // error is non-nil.
                Text(err)
                    .font(.callout)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal)
            } else {
                ProgressView()   // iOS spinner animation
            }
        }
    }
}
