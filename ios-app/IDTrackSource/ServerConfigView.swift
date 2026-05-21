import SwiftUI

// MARK: - ServerConfigView
//
// Shown when appState.serverURL is empty — either on first launch or after
// the user taps "Change Server" in Settings. The user types the base URL of
// their idtrack server; the view validates it by attempting a GET /api/status
// before saving.

struct ServerConfigView: View {
    @EnvironmentObject var appState: AppState

    @State private var urlInput = ""       // raw text from the TextField
    @State private var isChecking = false  // true while the network probe runs
    @State private var errorMessage = ""   // shown in red if connection fails

    var body: some View {
        // NavigationStack provides a navigation bar for the title.
        // There's no back button here because this is a top-level screen.
        NavigationStack {
            Form {
                // An unnamed Section with a custom VStack for the hero icon + text.
                // `.listRowBackground(Color.clear)` removes the default white card
                // background so the VStack blends into the form background.
                Section {
                    VStack(alignment: .leading, spacing: 8) {
                        Image(systemName: "network")
                            .font(.system(size: 44))
                            .foregroundStyle(.tint)
                            .frame(maxWidth: .infinity, alignment: .center)
                            .padding(.vertical, 8)

                        Text("Connect to Server")
                            .font(.headline)
                            .frame(maxWidth: .infinity, alignment: .center)

                        Text("Enter the base URL of your idtrack server, including port.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                            .frame(maxWidth: .infinity, alignment: .center)
                    }
                    .listRowBackground(Color.clear)
                }

                Section("Server URL") {
                    // `.keyboardType(.URL)` shows a keyboard optimised for URL
                    // entry (no space bar, shows "/" and "." prominently).
                    // `.textInputAutocapitalization(.never)` prevents the system
                    // from capitalising the first character.
                    TextField("https://myserver:8443", text: $urlInput)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    if !errorMessage.isEmpty {
                        Text(errorMessage)
                            .font(.callout)
                            .foregroundStyle(.red)
                    }
                }

                Section {
                    // The button label changes between "Connect" and a spinner
                    // while the network probe is in flight.
                    Button(action: connect) {
                        if isChecking {
                            HStack {
                                ProgressView()
                                Text("Connecting…")
                            }
                        } else {
                            Text("Connect")
                                .frame(maxWidth: .infinity, alignment: .center)
                        }
                    }
                    // Disabled when the field is blank or a check is already running.
                    .disabled(urlInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isChecking)
                }

                Section {
                    Text("The app accepts self-signed TLS certificates for self-hosted deployments.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .navigationTitle("Server Setup")
        }
        // Pre-populate the field with the current (possibly partial) URL when
        // the view appears — useful when the user returns here to fix a typo.
        .onAppear { urlInput = appState.serverURL }
    }

    // MARK: - Connect action
    //
    // Called synchronously by Button; spawns an async Task for the network work.
    // `Task { }` bridges the synchronous button handler into Swift concurrency.
    private func connect() {
        errorMessage = ""
        var url = urlInput.trimmingCharacters(in: .whitespacesAndNewlines)
        // Be forgiving: if the user omits the scheme, add "https://".
        if !url.lowercased().hasPrefix("http") { url = "https://" + url }
        // Remove any trailing slashes for consistent path concatenation later.
        while url.hasSuffix("/") { url = String(url.dropLast()) }

        isChecking = true
        // Temporarily set the URL on the API client so getStatus() uses it.
        // We only persist it to appState.serverURL if the probe succeeds.
        appState.api.setBaseURL(url)

        Task {
            do {
                let status = try await appState.api.getStatus()
                // Probe succeeded — persist the URL (triggers the didSet observer
                // in AppState which also calls api.setBaseURL again, harmlessly).
                appState.serverURL = url
                // Apply any branding from the server response.
                if let n = status.appName,        !n.isEmpty { appState.appName = n }
                if let d = status.appDescription, !d.isEmpty { appState.appDesc = d }
                // If the server needs first-time setup, store the onboarding token.
                if let ob = status.onboarding, ob, let tok = status.token {
                    appState.onboardingToken = tok
                }
                // ContentView observes serverURL and onboardingToken via @Published;
                // updating them here automatically navigates to the correct next screen.
            } catch {
                errorMessage = error.localizedDescription
                // Reset the API client's URL so it doesn't hold a bad value.
                appState.api.setBaseURL(appState.serverURL)
            }
            isChecking = false
        }
    }
}
