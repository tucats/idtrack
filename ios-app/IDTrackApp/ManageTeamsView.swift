import SwiftUI

// MARK: - ManageTeamsView
//
// Admin-only sheet for managing the canonical team list.
// Uses the same parent–child NavigationStack push pattern as EditProjectsView:
//
//   ManageTeamsView (list of teams)
//     └── TeamDetailView (pushed via NavigationLink — create or edit a team)

struct ManageTeamsView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var isLoading = true

    var body: some View {
        NavigationStack {
            Group {
                if isLoading {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List {
                        if appState.availableTeams.isEmpty {
                            EmptyStatePlaceholder(
                                icon: "person.3",
                                title: "No Teams",
                                message: "Tap + to create a team."
                            )
                        } else {
                            ForEach(appState.availableTeams) { team in
                                NavigationLink(destination: TeamDetailView(team: team)) {
                                    TeamRow(team: team)
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Manage Teams")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                // `.cancellationAction` is the semantic placement for a
                // "dismiss without saving" button. SwiftUI picks the correct
                // slot per platform and — importantly on Mac Catalyst — sizes
                // it to fit the label. The previous `.topBarLeading` placement
                // used a back-button-style tight intrinsic width that
                // truncated "Done" to "D…" on Catalyst.
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    NavigationLink(destination: TeamDetailView(team: nil)) {
                        Label("New Team", systemImage: "plus")
                    }
                }
            }
        }
        .task { await load() }
    }

    private func load() async {
        isLoading = true
        try? await appState.refreshTeams()
        isLoading = false
    }
}

// MARK: - Team Row

private struct TeamRow: View {
    let team: Team

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(team.name)
                    .font(.body)
                if team.isReserved {
                    Image(systemName: "lock.fill")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            if !team.description.isEmpty {
                Text(team.description)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Team Detail View

struct TeamDetailView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    // nil = creating a new team; non-nil = editing an existing one.
    let team: Team?

    @State private var name        = ""
    @State private var description = ""
    @State private var errorMsg    = ""
    @State private var isSaving    = false
    @State private var showDeleteConfirm = false

    private var isNew: Bool { team == nil }
    private var isReserved: Bool { team?.isReserved ?? false }

    var body: some View {
        Form {
            Section("Team") {
                LabeledField("Name", required: isNew) {
                    TextField("e.g. platform", text: $name)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .disabled(isReserved)
                }
                LabeledField("Description") {
                    TextField("Optional description", text: $description)
                        .autocorrectionDisabled()
                }
            }

            if isReserved {
                Section {
                    Text("The \"\(team?.name ?? "")\" team is reserved and cannot be renamed or deleted.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
            }

            if !isNew && !isReserved {
                Section {
                    Button("Delete Team", role: .destructive) { showDeleteConfirm = true }
                }
            }

            if !errorMsg.isEmpty {
                Section { Text(errorMsg).foregroundStyle(.red).font(.callout) }
            }
        }
        .navigationTitle(isNew ? "New Team" : (team?.name ?? "Team"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Save") { Task { await save() } }
                    .disabled(isSaving || (isNew && name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
            }
        }
        .confirmationDialog("Delete team \"\(team?.name ?? "")\"?", isPresented: $showDeleteConfirm, titleVisibility: .visible) {
            Button("Delete", role: .destructive) { Task { await deleteTeam() } }
        } message: {
            Text("This cannot be undone. Ensure no users, projects, or issues reference this team first.")
        }
        .onAppear {
            name        = team?.name ?? ""
            description = team?.description ?? ""
        }
    }

    private func save() async {
        errorMsg = ""
        isSaving = true
        defer { isSaving = false }
        do {
            if isNew {
                let n = name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
                guard !n.isEmpty else { errorMsg = "Team name is required."; return }
                try await appState.api.createTeam(name: n, description: description.trimmingCharacters(in: .whitespacesAndNewlines))
            } else {
                guard let existing = team else { return }
                let newName = name.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
                let renamedName: String? = (newName != existing.name && !newName.isEmpty) ? newName : nil
                let desc = description.trimmingCharacters(in: .whitespacesAndNewlines)
                try await appState.api.updateTeam(
                    name: existing.name,
                    newName: renamedName,
                    description: desc.isEmpty ? nil : desc
                )
            }
            try? await appState.refreshTeams()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }

    private func deleteTeam() async {
        guard let t = team else { return }
        do {
            try await appState.api.deleteTeam(name: t.name)
            try? await appState.refreshTeams()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}
