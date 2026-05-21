import SwiftUI

// MARK: - NewIssueView
//
// Modal form for creating a new issue. Presented as a sheet from MainAppView.
//
// On success, `onCreated(newIssue.id)` is called with the server-assigned
// issue ID so MainAppView can immediately select and display the new issue.
// On cancel, `onCreated(-1)` signals that nothing was created (any negative
// ID is treated as a cancellation by the caller).
//
// Required fields: Title, Project, Component.
// The Create button stays disabled until all three have values.

struct NewIssueView: View {
    @EnvironmentObject var appState: AppState

    // Callback closure — called when creation succeeds (id > 0) or when the
    // user taps Cancel (id == -1). The `var` declaration (not `let`) is fine
    // for a stored closure; SwiftUI views are value types so the closure is
    // copied along with the struct.
    var onCreated: (Int) -> Void

    // MARK: Form fields
    @State private var title     = ""
    @State private var desc      = ""
    @State private var priority  = "Medium"   // default matches the server default
    @State private var assignee  = ""         // "" = unassigned
    @State private var project   = ""         // "" = not chosen yet
    @State private var component = ""         // "" = not chosen yet
    @State private var errorMsg  = ""
    @State private var isCreating = false

    // Auto-focus the title field when the sheet opens.
    @FocusState private var titleFocused: Bool

    // Derived computed property: only valid when a project has been selected.
    // This drives the cascaded Component picker — it becomes empty (and the
    // picker disabled) when no project is chosen.
    private var components: [String] {
        appState.components(for: project)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Issue") {
                    // `LabeledField` (defined in OnboardingView.swift) adds a
                    // label row with an optional red asterisk above the content.
                    LabeledField("Title", required: true) {
                        TextField("Brief summary", text: $title)
                            .focused($titleFocused)
                    }
                    LabeledField("Description") {
                        // TextEditor allows multi-line free text.
                        // The `.overlay` adds a subtle border to visually
                        // distinguish the editing area from the form background.
                        TextEditor(text: $desc)
                            .frame(minHeight: 80)
                            .overlay(
                                RoundedRectangle(cornerRadius: 8)
                                    .stroke(Color(.separator), lineWidth: 0.5)
                            )
                    }
                }

                Section("Classification") {
                    Picker("Priority", selection: $priority) {
                        Text("High").tag("High")
                        Text("Medium").tag("Medium")
                        Text("Low").tag("Low")
                    }
                    // Assignee is optional — issues may be filed unassigned.
                    Picker("Assignee", selection: $assignee) {
                        Text("(unassigned)").tag("")
                        ForEach(appState.users) { u in
                            Text(u.displayName.isEmpty ? u.username : u.displayName).tag(u.username)
                        }
                    }
                }

                Section("Location") {
                    // CASCADED PICKERS
                    // Step 1: choose a project. This enables the component picker.
                    Picker("Project", selection: $project) {
                        Text("Choose project…").tag("")
                        ForEach(appState.projects) { p in
                            Text(p.name).tag(p.name)
                        }
                    }
                    // When the project changes, clear the component selection if
                    // it no longer exists in the new project's component list.
                    .onChange(of: project) { _ in
                        if !components.contains(component) { component = "" }
                    }

                    // Step 2: choose a component. Only active when a project
                    // has been selected AND that project has at least one component.
                    Picker("Component", selection: $component) {
                        Text("Choose component…").tag("")
                        // `id: \.self` — use the String value itself as the
                        // ForEach identity, since component names are unique within a project.
                        ForEach(components, id: \.self) { c in
                            Text(c).tag(c)
                        }
                    }
                    .disabled(project.isEmpty || components.isEmpty)
                }

                if !errorMsg.isEmpty {
                    Section {
                        Text(errorMsg)
                            .foregroundStyle(.red)
                            .font(.callout)
                    }
                }
            }
            .navigationTitle("New Issue")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    // Negative ID signals cancellation to the caller.
                    Button("Cancel") { onCreated(-1) }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Create") { Task { await create() } }
                        // All three required fields must be filled before the
                        // button enables. `.trimmingCharacters` strips invisible
                        // whitespace so a title of "   " is treated as empty.
                        .disabled(title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                                  || project.isEmpty || component.isEmpty || isCreating)
                }
            }
        }
        // Programmatically focus the Title field immediately on sheet open.
        // This saves the user from having to tap before typing.
        .onAppear { titleFocused = true }
    }

    // MARK: - Create action

    private func create() async {
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        errorMsg = ""
        // Server-side validation mirrors client-side for defence-in-depth.
        guard !t.isEmpty     else { errorMsg = "Title is required.";     return }
        guard !project.isEmpty   else { errorMsg = "Project is required.";   return }
        guard !component.isEmpty else { errorMsg = "Component is required."; return }

        isCreating = true
        // `defer` guarantees isCreating = false runs whether the do block
        // succeeds, throws, or returns early.
        defer { isCreating = false }
        do {
            let newIssue = try await appState.api.createIssue(
                title: t,
                description: desc.trimmingCharacters(in: .whitespacesAndNewlines),
                priority: priority, assignee: assignee,
                project: project, component: component
            )
            // Pass the new issue ID back to the caller (MainAppView), which
            // closes the sheet and selects the new issue in the detail panel.
            onCreated(newIssue.id)
        } catch APIError.unauthorized {
            // Session expired mid-form — sign out gracefully.
            await appState.signOut()
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}
