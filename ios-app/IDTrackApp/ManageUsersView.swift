import SwiftUI

// MARK: - ManageUsersView

struct ManageUsersView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var users:      [User] = []
    @State private var isLoading   = true
    @State private var showAddUser = false
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
        .task { await load() }
        .sheet(isPresented: $showAddUser,  onDismiss: { Task { await load() } }) { AddUserView() }
        .sheet(item: $editTarget,          onDismiss: { Task { await load() } }) { u in EditUserView(user: u) }
    }

    private func load() async {
        isLoading = true
        do { users = try await appState.api.getUsers() } catch {}
        isLoading = false
    }
}

// MARK: - User row

private struct UserRow: View {
    let user: User

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(user.displayName.isEmpty ? user.username : user.displayName)
                    .font(.body)
                // Show team badges instead of a single Admin badge.
                ForEach(user.teams.prefix(3), id: \.self) { team in
                    TeamBadge(name: team)
                }
                if user.teams.count > 3 {
                    Text("+\(user.teams.count - 3)")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
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

// MARK: - Team Badge

struct TeamBadge: View {
    let name: String

    private var color: Color {
        switch name.lowercased() {
        case "admin": return .blue
        case "any":   return .green
        default:      return .secondary
        }
    }

    var body: some View {
        Text(name)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 5)
            .padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundStyle(color)
            .clipShape(Capsule())
    }
}

// MARK: - Team Picker Sheet

struct TeamPickerView: View {
    @Environment(\.dismiss) private var dismiss
    let availableTeams: [Team]
    @Binding var selectedTeams: [String]

    var body: some View {
        NavigationStack {
            List(availableTeams) { team in
                let isSelected = selectedTeams.contains { $0.lowercased() == team.name.lowercased() }
                Button(action: { toggle(team.name) }) {
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(team.name)
                                .font(.body)
                            if !team.description.isEmpty {
                                Text(team.description)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        Spacer()
                        if isSelected {
                            Image(systemName: "checkmark")
                                .foregroundStyle(.blue)
                        }
                    }
                }
                .buttonStyle(.plain)
            }
            .navigationTitle("Select Teams")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    private func toggle(_ name: String) {
        let lower = name.lowercased()
        if let idx = selectedTeams.firstIndex(where: { $0.lowercased() == lower }) {
            selectedTeams.remove(at: idx)
        } else {
            selectedTeams.append(name)
        }
    }
}

// MARK: - Teams field (reusable inline row for forms)

struct TeamsField: View {
    let label: String
    @Binding var teams: [String]
    let availableTeams: [Team]
    @State private var showPicker = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button(action: { showPicker = true }) {
                HStack {
                    Text(label)
                        .foregroundStyle(.primary)
                    Spacer()
                    if teams.isEmpty {
                        Text("None selected")
                            .foregroundStyle(.secondary)
                    } else {
                        Text(teams.joined(separator: ", "))
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .buttonStyle(.plain)
        }
        .sheet(isPresented: $showPicker) {
            TeamPickerView(availableTeams: availableTeams, selectedTeams: $teams)
        }
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
    @State private var teams: [String] = ["any"]
    @State private var errorMsg    = ""
    @State private var isSaving    = false

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
                Section("Teams") {
                    TeamsField(label: "Teams", teams: $teams, availableTeams: appState.availableTeams)
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
        .onAppear { firstFocus = true }
        .task { try? await appState.refreshTeams() }
    }

    private func save() async {
        let name = username.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        errorMsg = ""
        guard !name.isEmpty          else { errorMsg = "Username is required."; return }
        guard !password.isEmpty      else { errorMsg = "Password is required."; return }
        guard password == confirm     else { errorMsg = "Passwords do not match."; return }
        guard !teams.isEmpty          else { errorMsg = "At least one team is required."; return }

        isSaving = true
        defer { isSaving = false }
        do {
            let dn = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
            try await appState.api.createUser(username: name, displayName: dn, password: password, teams: teams)
            try? await appState.refreshUsers()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}

// MARK: - Edit User

struct EditUserView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    let user: User

    @State private var displayName = ""
    @State private var password    = ""
    @State private var confirm     = ""
    @State private var teams: [String] = []
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
                    LabeledField("New Password") {
                        SecureField("(leave blank to keep)", text: $password)
                    }
                    LabeledField("Confirm Password") {
                        SecureField("(leave blank to keep)", text: $confirm)
                    }
                }
                Section("Teams") {
                    TeamsField(label: "Teams", teams: $teams, availableTeams: appState.availableTeams)
                }
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
        .onAppear {
            displayName = user.displayName
            teams       = user.teams
        }
        .task { try? await appState.refreshTeams() }
    }

    private func save() async {
        errorMsg = ""
        if !password.isEmpty && password != confirm {
            errorMsg = "Passwords do not match."
            return
        }
        guard !teams.isEmpty else { errorMsg = "At least one team is required."; return }
        isSaving = true
        defer { isSaving = false }
        do {
            let dn = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
            try await appState.api.updateUser(username: user.username, displayName: dn, password: password, teams: teams)
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
