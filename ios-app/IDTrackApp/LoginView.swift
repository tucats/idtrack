import SwiftUI

// MARK: - LoginView
//
// Standard username/password login form. On successful login, AppState
// flips isLoggedIn to true, which causes ContentView to swap to MainAppView.

struct LoginView: View {
    @EnvironmentObject var appState: AppState

    @State private var username    = ""
    @State private var password    = ""
    @State private var errorMsg    = ""
    @State private var isLoading   = false

    // @FocusState tracks which form field currently has keyboard focus.
    // It can be any Hashable type; an enum works well because the compiler
    // enforces that the cases match the actual fields in the view.
    @FocusState private var focus: Field?

    // The two focusable fields in this form.
    enum Field { case username, password }

    var body: some View {
        NavigationStack {
            // ScrollView ensures the form remains accessible when the
            // software keyboard pushes content up on small iPhones.
            ScrollView {
                VStack(spacing: 32) {
                    Spacer(minLength: 40)

                    // App icon and branding
                    VStack(spacing: 8) {
                        Image(systemName: "list.bullet.clipboard.fill")
                            .font(.system(size: 56))
                            .foregroundStyle(.tint)
                        Text(appState.appName)
                            .font(.largeTitle.bold())
                        Text(appState.appDesc)
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }

                    // Form fields
                    VStack(spacing: 16) {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Username")
                                .font(.subheadline.weight(.medium))
                            TextField("username", text: $username)
                                .textFieldStyle(.roundedBorder)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                // `.focused($focus, equals: .username)` — this field
                                // receives focus when `focus == .username` and
                                // updates `focus` when the user taps it.
                                .focused($focus, equals: .username)
                                // `.submitLabel` controls the Return key label.
                                // `.next` shows "Next" to hint that another field follows.
                                .submitLabel(.next)
                                // `.onSubmit` fires when the user taps the Return key.
                                // Moving focus to the next field provides a smooth
                                // keyboard-navigation flow.
                                .onSubmit { focus = .password }
                        }

                        VStack(alignment: .leading, spacing: 6) {
                            Text("Password")
                                .font(.subheadline.weight(.medium))
                            // SecureField masks input — shows bullets instead of characters.
                            SecureField("password", text: $password)
                                .textFieldStyle(.roundedBorder)
                                .focused($focus, equals: .password)
                                // `.go` shows "Go" / "Sign In" on the Return key,
                                // signalling that pressing it submits the form.
                                .submitLabel(.go)
                                .onSubmit { Task { await submit() } }
                        }

                        if !errorMsg.isEmpty {
                            Text(errorMsg)
                                .font(.callout)
                                .foregroundStyle(.red)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    .padding(.horizontal, 32)

                    // Primary action button. `Task { await submit() }` bridges
                    // the synchronous action closure into async/await.
                    Button(action: { Task { await submit() } }) {
                        if isLoading {
                            HStack { ProgressView(); Text("Signing in…") }
                                .frame(maxWidth: .infinity)
                        } else {
                            Text("Sign In")
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .disabled(username.isEmpty || password.isEmpty || isLoading)
                    .padding(.horizontal, 32)

                    Spacer(minLength: 40)
                }
                // Constrain width on iPad — the form looks odd if it stretches
                // across the full 1024-pt iPad screen.
                .frame(maxWidth: 420)
                .frame(maxWidth: .infinity)
            }
            .navigationTitle("Sign In")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                // "Server" button in the top-right corner clears the server URL,
                // which causes ContentView to navigate back to ServerConfigView.
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Server") {
                        appState.serverURL = ""
                    }
                    .font(.callout)
                }
            }
        }
        // Auto-focus the username field when the view appears.
        .onAppear { focus = .username }
    }

    // MARK: - Submit

    private func submit() async {
        // Trim whitespace and lowercase the username. The server stores
        // usernames lowercase, so matching must be case-insensitive.
        let name = username.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !name.isEmpty, !password.isEmpty else { return }

        errorMsg  = ""
        isLoading = true
        // `defer` runs when the function returns — whether by normal exit or
        // by throwing. This guarantees the button re-enables even if an error
        // is thrown deep inside the do block.
        defer { isLoading = false }

        do {
            let user = try await appState.api.login(
                username: name, password: password, keepLoggedIn: appState.keepLoggedIn
            )
            appState.signIn(user: user)
            // Pre-load reference data. `try?` makes failures non-fatal —
            // the app can still function if these requests fail; dropdowns
            // will just be empty until the next navigation.
            try? await appState.refreshUsers()
            try? await appState.refreshProjects()
            // appState.isLoggedIn is now true → ContentView shows MainAppView.
        } catch APIError.unauthorized {
            // The server returned 401 for an incorrect username/password.
            // Show a user-friendly message rather than "Session expired".
            errorMsg = "Invalid username or password."
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}
