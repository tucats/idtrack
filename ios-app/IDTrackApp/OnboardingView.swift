import SwiftUI

// MARK: - OnboardingView
//
// Shown when GET /api/status reports `onboarding: true` — meaning the server
// has no user accounts yet. This creates the first admin account.
//
// The flow:
//   1. GET /api/status returns { onboarding: true, token: "<uuid>" }
//   2. ContentView sets appState.onboardingToken and shows OnboardingView.
//   3. The user fills in the form and taps "Create Account".
//   4. submit() calls POST /api/onboarding with HTTP Basic auth carrying the token.
//   5. On success, the server returns the new user + sets a session cookie.
//   6. appState.onboardingToken is cleared → ContentView shows MainAppView.

struct OnboardingView: View {
    @EnvironmentObject var appState: AppState

    @State private var username    = ""
    @State private var displayName = ""
    @State private var password    = ""
    @State private var confirm     = ""    // password confirmation field
    @State private var errorMsg    = ""
    @State private var isLoading   = false

    // Enum-backed @FocusState — one case per field enables keyboard traversal
    // via the Return key (see LoginView for a detailed explanation).
    @FocusState private var focus: Field?
    enum Field { case username, displayName, password, confirm }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 32) {
                    Spacer(minLength: 40)

                    VStack(spacing: 8) {
                        Image(systemName: "person.badge.plus")
                            .font(.system(size: 56))
                            .foregroundStyle(.tint)
                        Text("Welcome to \(appState.appName)")
                            .font(.largeTitle.bold())
                            .multilineTextAlignment(.center)
                        Text("Create the first admin account to get started.")
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.center)
                    }

                    VStack(spacing: 16) {
                        // LabeledField is a reusable helper defined below.
                        // The `required: true` argument adds a red asterisk.
                        // The trailing closure is a @ViewBuilder — it accepts
                        // any View and positions it below the label.
                        LabeledField("Username", required: true) {
                            TextField("login name", text: $username)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .focused($focus, equals: .username)
                                .submitLabel(.next)
                                .onSubmit { focus = .displayName }
                        }

                        LabeledField("Display Name") {
                            TextField("(defaults to username)", text: $displayName)
                                .autocorrectionDisabled()
                                .focused($focus, equals: .displayName)
                                .submitLabel(.next)
                                .onSubmit { focus = .password }
                        }

                        LabeledField("Password", required: true) {
                            SecureField("password", text: $password)
                                .focused($focus, equals: .password)
                                .submitLabel(.next)
                                .onSubmit { focus = .confirm }
                        }

                        LabeledField("Confirm Password", required: true) {
                            SecureField("confirm password", text: $confirm)
                                .focused($focus, equals: .confirm)
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

                    Button(action: { Task { await submit() } }) {
                        if isLoading {
                            HStack { ProgressView(); Text("Creating…") }
                                .frame(maxWidth: .infinity)
                        } else {
                            Text("Create Account")
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .disabled(username.isEmpty || password.isEmpty || confirm.isEmpty || isLoading)
                    .padding(.horizontal, 32)

                    Spacer(minLength: 40)
                }
                .frame(maxWidth: 420)
                .frame(maxWidth: .infinity)
            }
            .navigationTitle("First-Time Setup")
            .navigationBarTitleDisplayMode(.inline)
        }
        .onAppear { focus = .username }
    }

    // MARK: - Submit

    private func submit() async {
        let name = username.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        errorMsg = ""

        // Client-side validation before making any network call.
        // Each `guard` exits the function with an error message if the
        // condition is false. `else { ... return }` is the exit clause.
        guard !name.isEmpty             else { errorMsg = "Username is required."; return }
        guard !password.isEmpty         else { errorMsg = "Password is required."; return }
        guard password == confirm       else { errorMsg = "Passwords do not match."; return }
        // The token must still be in AppState — it's cleared after success, and
        // the app can't reach this view without it, but we guard defensively.
        guard let token = appState.onboardingToken else {
            errorMsg = "Onboarding token missing — restart the app."
            return
        }

        isLoading = true
        defer { isLoading = false }

        do {
            // If display name is blank, fall back to the username as the default.
            let dn   = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
            let user = try await appState.api.onboard(
                username: name,
                displayName: dn.isEmpty ? name : dn,
                password: password,
                token: token
            )
            // Clear the token — this causes ContentView to exit OnboardingView.
            appState.onboardingToken = nil
            appState.signIn(user: user)
            try? await appState.refreshUsers()
            try? await appState.refreshProjects()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}

// MARK: - LabeledField
//
// A reusable form helper that pairs a text label (with an optional red
// asterisk for required fields) with any content view below it.
//
// `<Content: View>` makes this generic: it can wrap a TextField,
// SecureField, or any other view.
//
// `@ViewBuilder` on the `content` parameter allows the caller to write
// multiple views inside the trailing closure — SwiftUI combines them
// automatically. Without @ViewBuilder, only a single expression would
// be allowed.

struct LabeledField<Content: View>: View {
    let label: String
    let required: Bool     // when true, a red asterisk appears after the label
    let content: Content   // the wrapped input control

    // `required: Bool = false` gives the parameter a default value so callers
    // that don't need an asterisk can omit it: LabeledField("Name") { ... }
    init(_ label: String, required: Bool = false, @ViewBuilder content: () -> Content) {
        self.label    = label
        self.required = required
        // The @ViewBuilder closure is invoked immediately at init time.
        // `content()` produces the Content value that is stored.
        self.content  = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 2) {
                Text(label).font(.subheadline.weight(.medium))
                if required { Text("*").foregroundStyle(.red) }
            }
            content
                // Apply rounded-border style to whatever input control is
                // passed in. Works for TextField and SecureField.
                .textFieldStyle(.roundedBorder)
        }
    }
}
