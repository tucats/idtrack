import SwiftUI

// MARK: - EditProjectsView

struct EditProjectsView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var showNewProject   = false
    @State private var selectedProject: Project? = nil

    var body: some View {
        NavigationStack {
            List {
                if appState.projects.isEmpty {
                    EmptyStatePlaceholder(
                        icon: "folder.badge.plus",
                        title: "No Projects",
                        message: "Tap + to add a project."
                    )
                } else {
                    ForEach(appState.projects) { proj in
                        Button(action: { selectedProject = proj }) {
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(proj.name).font(.body)
                                    Text("\(proj.components.count) component\(proj.components.count == 1 ? "" : "s")")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                    // Show teams as small badges.
                                    if !proj.teams.isEmpty {
                                        HStack(spacing: 4) {
                                            ForEach(proj.teams, id: \.self) { t in
                                                TeamBadge(name: t)
                                            }
                                        }
                                    }
                                }
                                Spacer()
                                Image(systemName: "chevron.right")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .navigationTitle("Edit Projects")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button(action: { showNewProject = true }) {
                        Label("New Project", systemImage: "folder.badge.plus")
                    }
                }
            }
        }
        .task { try? await appState.refreshProjects() }
        .sheet(isPresented: $showNewProject,
               onDismiss: { Task { try? await appState.refreshProjects() } }) {
            NewProjectView()
        }
        .sheet(item: $selectedProject,
               onDismiss: { Task { try? await appState.refreshProjects() } }) { proj in
            ProjectDetailView(projectName: proj.name)
        }
    }
}

// MARK: - New Project

struct NewProjectView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var projectName      = ""
    @State private var componentName    = ""
    @State private var pendingComponents: [String] = []
    @State private var teams: [String]  = ["any"]
    @State private var errorMsg         = ""
    @State private var isCreating       = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Project") {
                    LabeledField("Name", required: true) {
                        TextField("e.g. Backend", text: $projectName)
                            .autocorrectionDisabled()
                    }
                }

                Section("Teams") {
                    TeamsField(label: "Visible to", teams: $teams, availableTeams: appState.availableTeams)
                }

                Section("Components") {
                    HStack {
                        TextField("Component name", text: $componentName)
                            .autocorrectionDisabled()
                            .submitLabel(.done)
                            .onSubmit { addPending() }
                        Button("Add", action: addPending)
                            .disabled(componentName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                    if !pendingComponents.isEmpty {
                        ForEach(pendingComponents, id: \.self) { comp in
                            HStack {
                                Text(comp)
                                Spacer()
                                Button(role: .destructive, action: {
                                    pendingComponents.removeAll { $0 == comp }
                                }) {
                                    Image(systemName: "minus.circle.fill")
                                        .foregroundStyle(.red)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }

                if !errorMsg.isEmpty {
                    Section { Text(errorMsg).foregroundStyle(.red).font(.callout) }
                }
            }
            .navigationTitle("New Project")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button(isCreating ? "Creating…" : "Create") { Task { await create() } }
                        .disabled(projectName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isCreating)
                }
            }
        }
        .task { try? await appState.refreshTeams() }
    }

    private func addPending() {
        let name = componentName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { return }
        guard !pendingComponents.map({ $0.lowercased() }).contains(name.lowercased()) else {
            errorMsg = "\"\(name)\" is already in the list."
            return
        }
        pendingComponents.append(name)
        componentName = ""
        errorMsg = ""
    }

    private func create() async {
        let name = projectName.trimmingCharacters(in: .whitespacesAndNewlines)
        errorMsg = ""
        guard !name.isEmpty else { errorMsg = "Project name is required."; return }
        if appState.projects.contains(where: { $0.name.lowercased() == name.lowercased() }) {
            errorMsg = "Project \"\(name)\" already exists."
            return
        }
        isCreating = true
        defer { isCreating = false }
        do {
            try await appState.api.createProject(name: name, teams: teams)
            for comp in pendingComponents {
                try? await appState.api.createComponent(project: name, name: comp)
            }
            try? await appState.refreshProjects()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}

// MARK: - Project Detail

struct ProjectDetailView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    let projectName: String

    @State private var componentName  = ""
    @State private var teams: [String] = []
    @State private var errorMsg       = ""
    @State private var isAdding       = false
    @State private var isSavingTeams  = false
    @State private var showDeleteProject = false

    private var project: Project? {
        appState.projects.first { $0.name == projectName }
    }

    var body: some View {
        NavigationStack {
            Form {
                if let proj = project {
                    Section("Teams") {
                        TeamsField(label: "Visible to", teams: $teams, availableTeams: appState.availableTeams)
                        Button(isSavingTeams ? "Saving…" : "Save Teams") {
                            Task { await saveTeams() }
                        }
                        .disabled(isSavingTeams)
                    }

                    Section("Components") {
                        if proj.components.isEmpty {
                            Text("No components yet.")
                                .foregroundStyle(.secondary)
                        } else {
                            ForEach(proj.components, id: \.self) { comp in
                                HStack {
                                    Text(comp)
                                    Spacer()
                                    Button(role: .destructive, action: {
                                        Task { await deleteComponent(comp) }
                                    }) {
                                        Image(systemName: "trash")
                                            .foregroundStyle(.red)
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }
                    }

                    Section("Add Component") {
                        HStack {
                            TextField("Component name", text: $componentName)
                                .autocorrectionDisabled()
                                .submitLabel(.done)
                                .onSubmit { Task { await addComponent() } }
                            Button("Add") { Task { await addComponent() } }
                                .disabled(componentName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isAdding)
                        }
                    }

                    if !errorMsg.isEmpty {
                        Section { Text(errorMsg).foregroundStyle(.red).font(.callout) }
                    }

                    Section {
                        Button("Delete Project", role: .destructive) { showDeleteProject = true }
                    }
                }
            }
            .navigationTitle(projectName)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
            .confirmationDialog("Delete project \"\(projectName)\"?", isPresented: $showDeleteProject, titleVisibility: .visible) {
                Button("Delete", role: .destructive) { Task { await deleteProject() } }
            } message: {
                Text("This deletes all components and cannot be undone.")
            }
        }
        .onAppear {
            teams = project?.teams ?? ["any"]
        }
        .task { try? await appState.refreshTeams() }
    }

    private func saveTeams() async {
        isSavingTeams = true
        defer { isSavingTeams = false }
        do {
            try await appState.api.updateProjectTeams(project: projectName, teams: teams)
            try? await appState.refreshProjects()
        } catch {
            errorMsg = error.localizedDescription
        }
    }

    private func addComponent() async {
        let name = componentName.trimmingCharacters(in: .whitespacesAndNewlines)
        errorMsg = ""
        guard !name.isEmpty else { return }
        if let comps = project?.components, comps.map({ $0.lowercased() }).contains(name.lowercased()) {
            errorMsg = "\"\(name)\" already exists."
            return
        }
        isAdding = true
        defer { isAdding = false }
        do {
            try await appState.api.createComponent(project: projectName, name: name)
            componentName = ""
            try? await appState.refreshProjects()
        } catch {
            errorMsg = error.localizedDescription
        }
    }

    private func deleteComponent(_ comp: String) async {
        errorMsg = ""
        do {
            try await appState.api.deleteComponent(project: projectName, component: comp)
            try? await appState.refreshProjects()
        } catch {
            errorMsg = error.localizedDescription
        }
    }

    private func deleteProject() async {
        do {
            try await appState.api.deleteProject(name: projectName)
            try? await appState.refreshProjects()
            dismiss()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}
