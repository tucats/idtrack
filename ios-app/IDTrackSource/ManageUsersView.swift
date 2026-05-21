import SwiftUI

// MARK: - ManageUsersView
//
// Admin-only sheet that lists all users and provides access to Add / Edit
// flows via nested sheets. The pattern used here is called the "parent–child
// overlay" pattern:
//
//   ManageUsersView (parent)
//     ├── AddUserView  (child sheet, opened by tapping "+")
//     └── EditUserView (child sheet, opened by tapping a user row)
//
// The `onDismiss` callbacks on both child sheets call `load()` to refresh
// the user list whenever a child closes — whether the user saved, cancelled,
// or deleted an account.

struct ManageUsersView: View {
    @EnvironmentObject var appState: AppState
    // `@Environment(\.dismiss)` gives us the dismiss action for the current
    // presentation context. Calling `dismiss()` closes this sheet.
    @Environment(\.dismiss) private var dismiss

    @State private var users:      [User] = []
    @State private var isLoading   = true
    // Boolean flag for the Add User sheet.
    @State private var showAddUser = false
    // Optional User for the Edit User sheet. When non-nil, the sheet opens
    // and passes the user to EditUserView. Setting it back to nil closes the sheet.
    // `.sheet(item:)` is the right modifier when you want to pass data to the sheet.
    @State private var editTarget: User? = nil

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List {
                        ForEach(users) { u in
                            // Button wrapping the row makes the whole row tappable.
                            // `.buttonStyle(.plain)` prevents the default blue
                            // tint that Button adds to its content.
                            Button(action: { editTarget = u }) {
                                UserRow(user: u)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .navigationTitle("Manage Users")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button(action: { showAddUser = true }) {
                        Label("Add User", systemImage: "person.badge.plus")
                    }
                }
            }
        }
        // `.task` fires once on appear to load the initial user list.
        .task { await load() }
        // `onDismiss:` — refresh the list whenever AddUserView closes, so any
        // newly created user appears immediately without a manual pull-to-refresh.
        .sheet(isPresented: $showAddUser,  onDismiss: { Task { await load() } }) { AddUserView() }
        // `.sheet(item:)` — the sheet appears when `editTarget` is non-nil.
        // The `{ u in ... }` closure receives the non-nil user value.
        // Setting `editTarget = nil` (e.g. inside EditUserView via dismiss) closes the sheet.
        .sheet(item: $editTarget,          onDismiss: { Task { await load() } }) { u in EditUserView(user: u) }
    }

    private func load() async {
        isLoading = true
        do {
            users = try await appState.api.getUsers()
        } catch {}   // errors silently result in an empty list
        isLoading = false
    }
}

// MARK: - User row
//
// Compact display of one user: display name, optional Admin badge, username,
// and last-login time.
//
// `private` — only ManageUsersView uses this.

private struct UserRow: View {
    let user: User

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(user.displayName.isEmpty ? user.username : user.displayName)
                    .font(.body)
                if user.isAdmin {
                    // Admin badge — same capsule style as PriorityBadge / StatusBadge.
                    Text("Admin")
                        .font(.caption2.weight(.semibold))
                        .padding(.horizontal, 5)
                        .padding(.vertical, 2)
                        .background(Color.blue.opacity(0.15))
                        .foregroundStyle(.blue)
                        .clipShape(Capsule())
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Text(user.username)
                .font(.caption)
                .foregroundStyle(.secondary)
            if !user.lastLoginAt.isEmpty {
                Text("Last login: \(fmtDateTime(user.lastLoginAt))")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Add User

struct AddUserView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var username    = ""
    @State private var displayName = ""
    @State private var password    = ""
    @State private var confirm     = ""
    @State private var isAdmin     = false
    @State private var errorMsg    = ""
    @State private var isSaving    = false

    // Boolean @FocusState — simpler than an enum when there's only one
    // field that needs programmatic focus.
    @FocusState private var firstFocus: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section("Credentials") {
                    LabeledField("Username", required: true) {
                        TextField("login name", text: $username)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .focused($firstFocus)
                    }
                    LabeledField("Display Name") {
                        TextField("(defaults to username)", text: $displayName)
                            .autocorrectionDisabled()
                    }
                    LabeledField("Password", required: true) {
                        SecureField("password", text: $password)
                    }
                    LabeledField("Confirm Password", required: true) {
                        SecureField("confirm", text: $confirm)
                    }
                }
                // Toggle binds directly to the @State Bool.
                Section {
                    Toggle("Admin privileges", isOn: $isAdmin)
                }
                if !errorMsg.isEmpty {
                    Section { Text(errorMsg).foregroundStyle(.red).font(.callout) }
                }
            }
            .navigationTitle("Add User")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") { Task { await save() } }
                        .disabled(username.isEmpty || password.isEmpty || isSaving)
                }
            }
        }
        // Set focus on the username field when the sheet opens.
        // `firstFocus = true` triggers the `.focused($firstFocus)` modifier.
        .onAppear { firstFocus = true }
    }

    private func save() async {
        let name = username.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        errorMsg = ""
        guard !name.isEmpty          else { errorMsg = "Username is required."; return }
        guard !password.isEmpty      else { errorMsg = "Password is required."; return }
        guard password == confirm     else { errorMsg = "Passwords do not match."; return }

        isSaving = true
        defer { isSaving = false }
        do {
            let dn = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
            try await appState.api.createUser(username: name, displayName: dn, password: password, isAdmin: isAdmin)
            // Refresh the shared user list and map in AppState so dropdown menus
            // reflect the new user immediately after this sheet closes.
            try? await appState.refreshUsers()
            dismiss()   // success → close the sheet
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}

// MARK: - Edit User

struct EditUserView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    // The user to edit, passed in from ManageUsersView via .sheet(item:).
    // `let` — the identity of the user being edited doesn't change while
    // the sheet is open.
    let user: User

    // Edit fields — pre-populated in .onAppear.
    @State private var displayName = ""
    @State private var password    = ""  // blank = keep existing password
    @State private var confirm     = ""
    @State private var isAdmin     = false
    @State private var errorMsg    = ""
    @State private var isSaving    = false
    @State private var showDeleteConfirm = false

    var body: some View {
        NavigationStack {
            Form {
                Section("User: \(user.username)") {
                    LabeledField("Display Name") {
                        TextField("display name", text: $displayName)
                            .autocorrectionDisabled()
                    }
                    // Leaving both password fields blank means "don't change the password".
                    LabeledField("New Password") {
                        SecureField("(leave blank to keep)", text: $password)
                    }
                    LabeledField("Confirm Password") {
                        SecureField("(leave blank to keep)", text: $confirm)
                    }
                }
                Section {
                    Toggle("Admin privileges", isOn: $isAdmin)
                }
                // Hide the Delete button when editing one's own account.
                // The server would reject self-deletion, but it's better UX
                // to not show the button at all. The comparison uses `!=` so
                // the section only renders for other users.
                if user.username != appState.currentUser?.username {
                    Section {
                        Button("Delete User", role: .destructive) { showDeleteConfirm = true }
                    }
                }
                if !errorMsg.isEmpty {
                    Section { Text(errorMsg).foregroundStyle(.red).font(.callout) }
                }
            }
            .navigationTitle("Edit User")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }
                        .disabled(isSaving)
                }
            }
            .confirmationDialog("Delete \"\(user.username)\"?", isPresented: $showDeleteConfirm, titleVisibility: .visible) {
                Button("Delete", role: .destructive) { Task { await deleteUser() } }
            } message: {
                Text("This cannot be undone.")
            }
        }
        // Pre-populate the form with the user's current values.
        .onAppear {
            displayName = user.displayName
            isAdmin     = user.isAdmin
        }
    }

    private func save() async {
        errorMsg = ""
        // Only validate the password fields if the user typed something.
        // An empty password string means "leave unchanged".
        if !password.isEmpty && password != confirm {
            errorMsg = "Passwords do not match."
            return
        }
        isSaving = true
        defer { isSaving = false }
        do {
            let dn = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
            try await appState.api.updateUser(username: user.username, displayName: dn, password: password, isAdmin: isAdmin)
            try? await appState.refreshUsers()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }

    private func deleteUser() async {
        do {
            try await appState.api.deleteUser(username: user.username)
            try? await appState.refreshUsers()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}
