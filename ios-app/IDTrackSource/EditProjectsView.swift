import SwiftUI

// MARK: - EditProjectsView
//
// Admin-only sheet for managing the project/component taxonomy.
// Uses the same parent–child overlay pattern as ManageUsersView:
//
//   EditProjectsView (list of projects)
//     ├── NewProjectView    (child sheet — create a new project)
//     └── ProjectDetailView (child sheet — add/delete components for an existing project)
//
// Both child sheets call `appState.refreshProjects()` on dismiss so the
// parent list and the issue-creation form always show current data.

struct EditProjectsView: View {
    @EnvironmentObject var appState: AppState
    @Environment(\.dismiss) private var dismiss

    // Boolean flag for the new-project sheet.
    @State private var showNewProject   = false
    // Optional Project for the detail sheet. nil = no sheet.
    @State private var selectedProject: Project? = nil

    var body: some View {
        NavigationStack {
            List {
                if appState.projects.isEmpty {
                    // EmptyStatePlaceholder is an iOS 16-compatible replacement
                    // for ContentUnavailableView (which requires iOS 17).
                    EmptyStatePlaceholder(
                        icon: "folder.badge.plus",
                        title: "No Projects",
                        message: "Tap + to add a project."
                    )
                } else {
                    ForEach(appState.projects) { proj in
                        // Tapping a row opens ProjectDetailView for that project.
                        Button(action: { selectedProject = proj }) {
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(proj.name).font(.body)
                                    // Ternary pluralisation: "1 component" vs "2 components".
                                    Text("\(proj.components.count) component\(proj.components.count == 1 ? "" : "s")")
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
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
        // Refresh the project list when this view first appears.
        // `try?` makes the network call non-fatal; the cached list is shown
        // if the request fails.
        .task { try? await appState.refreshProjects() }
        .sheet(isPresented: $showNewProject,
               onDismiss: { Task { try? await appState.refreshProjects() } }) {
            NewProjectView()
        }
        // `.sheet(item:)` — presents ProjectDetailView for the selected project.
        // The `{ proj in ... }` closure receives the non-nil Project value.
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
    // Components staged locally before any server calls. The user can add and
    // remove entries here; they are all sent when "Create" is tapped.
    @State private var pendingComponents: [String] = []
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

                Section("Components") {
                    // Inline add-component row: a text field + button side by side.
                    HStack {
                        TextField("Component name", text: $componentName)
                            .autocorrectionDisabled()
                            // `.done` on the keyboard Return key confirms the component.
                            .submitLabel(.done)
                            .onSubmit { addPending() }
                        Button("Add", action: addPending)
                            .disabled(componentName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                    // Show the staged list with a delete button for each entry.
                    if !pendingComponents.isEmpty {
                        ForEach(pendingComponents, id: \.self) { comp in
                            HStack {
                                Text(comp)
                                Spacer()
                                Button(role: .destructive, action: {
                                    // `removeAll(where:)` removes every element matching
                                    // the closure. Since component names are unique, this
                                    // effectively removes at most one element.
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
                    // Label switches to "Creating…" while the request is in flight.
                    Button(isCreating ? "Creating…" : "Create") { Task { await create() } }
                        .disabled(projectName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isCreating)
                }
            }
        }
    }

    // Add a component to the pending list. Validates for empty name and
    // case-insensitive duplicates before adding.
    private func addPending() {
        let name = componentName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else { return }
        // Case-insensitive duplicate check. `.lowercased()` on both sides
        // ensures "API" and "api" are treated as the same component.
        guard !pendingComponents.map({ $0.lowercased() }).contains(name.lowercased()) else {
            errorMsg = "\"\(name)\" is already in the list."
            return
        }
        pendingComponents.append(name)
        componentName = ""   // clear the text field for the next entry
        errorMsg = ""
    }

    // Creates the project first, then creates each staged component.
    // Component creation uses `try?` so a single failure doesn't roll back
    // the whole operation — the user can add missing components in the detail view.
    private func create() async {
        let name = projectName.trimmingCharacters(in: .whitespacesAndNewlines)
        errorMsg = ""
        guard !name.isEmpty else { errorMsg = "Project name is required."; return }

        // Client-side duplicate check against the cached project list.
        if appState.projects.contains(where: { $0.name.lowercased() == name.lowercased() }) {
            errorMsg = "Project \"\(name)\" already exists."
            return
        }

        isCreating = true
        defer { isCreating = false }
        do {
            try await appState.api.createProject(name: name)
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

    // The project name passed in from EditProjectsView. We use the name (not
    // the Project value) so the view always reads fresh data from `appState.projects`.
    let projectName: String

    @State private var componentName  = ""
    @State private var errorMsg       = ""
    @State private var isAdding       = false
    @State private var showDeleteProject = false

    // Derived from AppState on every render — stays in sync automatically.
    private var project: Project? {
        appState.projects.first { $0.name == projectName }
    }

    var body: some View {
        NavigationStack {
            Form {
                // Only render the form content if the project exists.
                // After a successful delete, `project` becomes nil and the
                // form safely shows nothing while dismiss() is called.
                if let proj = project {
                    Section("Components") {
                        if proj.components.isEmpty {
                            Text("No components yet.")
                                .foregroundStyle(.secondary)
                        } else {
                            ForEach(proj.components, id: \.self) { comp in
                                HStack {
                                    Text(comp)
                                    Spacer()
                                    // Each component has an inline delete button.
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

                    // Destructive section at the bottom — standard iOS pattern
                    // for "danger zone" actions.
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
    }

    private func addComponent() async {
        let name = componentName.trimmingCharacters(in: .whitespacesAndNewlines)
        errorMsg = ""
        guard !name.isEmpty else { return }
        // Case-insensitive duplicate check against current component list.
        if let comps = project?.components, comps.map({ $0.lowercased() }).contains(name.lowercased()) {
            errorMsg = "\"\(name)\" already exists."
            return
        }
        isAdding = true
        defer { isAdding = false }
        do {
            try await appState.api.createComponent(project: projectName, name: name)
            componentName = ""
            // Refresh projects so the component list updates immediately.
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
            dismiss()   // close the detail sheet after deletion
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}
