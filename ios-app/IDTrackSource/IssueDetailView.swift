import SwiftUI

// MARK: - IssueDetailView
//
// Shows the full details of one issue and allows editing. The view is split
// into three main responsibilities:
//
//   1. Loading — fetches the issue and its comments via GET /api/issues/{id}.
//   2. Editing — provides form fields for every editable property. Changes
//      are tracked with `isDirty` so the Save button is only enabled when
//      something has actually changed.
//   3. Status transitions — changing status from Open → Resolved or vice versa
//      shows an intermediate sheet dialog to capture a reason or fixed-version
//      note, which is posted as a comment alongside the status update.

struct IssueDetailView: View {
    @EnvironmentObject var appState: AppState

    // The ID of the issue to display. Declared as `let` because this view is
    // replaced entirely when a different issue is selected (via `.task(id: issueId)`).
    let issueId: Int

    // Shared with MainAppView. Setting this to nil deselects the issue
    // (used after a successful delete to return to the empty-detail placeholder).
    @Binding var selectedIssueId: Int?

    // MARK: Loaded state
    @State private var issue:    Issue? = nil
    @State private var comments: [Comment] = []
    @State private var isLoading = true
    @State private var loadError: String? = nil

    // MARK: Editable form fields
    //
    // These shadow the loaded Issue properties. We copy them out on load so
    // edits don't mutate the Issue value directly — that lets us detect
    // whether the user actually changed anything (isDirty) and discard
    // changes by simply not saving.
    @State private var title      = ""
    @State private var status     = "Open"
    @State private var priority   = "Medium"
    @State private var assignee   = ""
    @State private var project    = ""
    @State private var component  = ""
    @State private var desc       = ""
    @State private var isDirty    = false   // true when any field differs from server state
    @State private var origStatus = "Open"  // the status as of the last successful save

    // MARK: Comment input
    @State private var newComment = ""
    @State private var isSavingComment = false

    // MARK: Status-change dialog state
    //
    // These are shared between the resolve and reopen sheets.
    // `sc` prefix = "status change".
    @State private var showResolveDialog = false
    @State private var showReopenDialog  = false
    @State private var scVersion         = ""   // "Fixed in X.Y" version string
    @State private var scComment         = ""   // free-text reason / notes
    @State private var scError           = ""   // validation error shown in the sheet

    // MARK: Save / delete state
    @State private var isSaving  = false
    @State private var saveError = ""
    @State private var showDeleteConfirm = false

    var body: some View {
        Group {
            if isLoading {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let err = loadError {
                EmptyStatePlaceholder(
                    icon: "exclamationmark.triangle",
                    title: "Error",
                    message: err,
                    action: { Task { await loadIssue() } },
                    actionLabel: "Retry"
                )
            } else if let iss = issue {
                issueForm(iss)
            }
        }
        // `map` on an Optional transforms the wrapped value if present.
        // `issue.map { "Issue #\($0.id)" }` returns "Issue #42" when issue is
        // non-nil, or nil when issue is nil, letting ?? supply the fallback.
        .navigationTitle(issue.map { "Issue #\($0.id)" } ?? "Issue")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { detailToolbar }
        // `.task(id: issueId)` re-runs the async closure whenever `issueId`
        // changes. This means the view automatically loads the correct data
        // when the user selects a different issue in the sidebar without the
        // view being destroyed and recreated.
        .task(id: issueId) { await loadIssue() }
        .sheet(isPresented: $showResolveDialog) { resolveSheet }
        .sheet(isPresented: $showReopenDialog)  { reopenSheet  }
        // `.confirmationDialog` shows a native action sheet with a destructive
        // confirmation button. Safer than a plain alert for irreversible actions.
        .confirmationDialog("Delete Issue #\(issueId)?", isPresented: $showDeleteConfirm, titleVisibility: .visible) {
            Button("Delete", role: .destructive) { Task { await deleteIssue() } }
        } message: {
            Text("This cannot be undone.")
        }
    }

    // MARK: - Main form

    // Extracted as a function (rather than a computed property) because it
    // takes the loaded `iss` value directly, avoiding repeated `issue!` force-
    // unwrapping throughout the body. The function returns `some View` so the
    // compiler can infer the concrete return type from the body.
    private func issueForm(_ iss: Issue) -> some View {
        let canEdit = appState.canModify(issue: iss)
        return ScrollView {
            VStack(spacing: 16) {

                // Title field — multi-line capable via `axis: .vertical`.
                // `lineLimit(1...3)` lets it grow up to 3 lines before scrolling.
                GroupBox("Title") {
                    TextField("Title", text: $title, axis: .vertical)
                        .lineLimit(1...3)
                        .disabled(!canEdit)
                        .onChange(of: title) { _ in isDirty = true }
                }

                // Status, Priority, Assignee grouped in one card.
                // `.pickerStyle(.menu)` shows a compact dropdown rather than
                // the full-screen wheel or segmented control.
                GroupBox {
                    VStack(spacing: 12) {
                        HStack {
                            Text("Status")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .frame(width: 80, alignment: .leading)
                            Picker("Status", selection: $status) {
                                Text("Open").tag("Open")
                                Text("Resolved").tag("Resolved")
                            }
                            .pickerStyle(.menu)
                            .disabled(!canEdit)
                            .onChange(of: status) { _ in isDirty = true }
                        }
                        Divider()
                        HStack {
                            Text("Priority")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .frame(width: 80, alignment: .leading)
                            Picker("Priority", selection: $priority) {
                                Text("High").tag("High")
                                Text("Medium").tag("Medium")
                                Text("Low").tag("Low")
                            }
                            .pickerStyle(.menu)
                            .disabled(!canEdit)
                            .onChange(of: priority) { _ in isDirty = true }
                        }
                        Divider()
                        HStack {
                            Text("Assignee")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .frame(width: 80, alignment: .leading)
                            Picker("Assignee", selection: $assignee) {
                                Text("(unassigned)").tag("")
                                ForEach(appState.users) { u in
                                    Text(u.displayName.isEmpty ? u.username : u.displayName).tag(u.username)
                                }
                            }
                            .pickerStyle(.menu)
                            .disabled(!canEdit)
                            .onChange(of: assignee) { _ in isDirty = true }
                        }
                    }
                }

                // Project / Component — cascaded pickers:
                // changing the project resets the component if the old selection
                // no longer exists in the new project's component list.
                GroupBox {
                    VStack(spacing: 12) {
                        HStack {
                            Text("Project")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .frame(width: 80, alignment: .leading)
                            Picker("Project", selection: $project) {
                                Text("Choose project…").tag("")
                                ForEach(appState.projects) { p in
                                    Text(p.name).tag(p.name)
                                }
                            }
                            .pickerStyle(.menu)
                            .disabled(!canEdit)
                            // iOS 16 onChange syntax: single-argument closure receives
                            // the new value. (iOS 17+ added a two-argument form.)
                            .onChange(of: project) { newProject in
                                isDirty = true
                                if !appState.components(for: newProject).contains(component) {
                                    component = ""
                                }
                            }
                        }
                        Divider()
                        HStack {
                            Text("Component")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .frame(width: 80, alignment: .leading)
                            // `let` inside a view builder captures the computed
                            // value once for both the Picker and the `.disabled` check.
                            let comps = appState.components(for: project)
                            Picker("Component", selection: $component) {
                                Text("Choose component…").tag("")
                                ForEach(comps, id: \.self) { c in
                                    Text(c).tag(c)
                                }
                            }
                            .pickerStyle(.menu)
                            .disabled(!canEdit || comps.isEmpty)
                            .onChange(of: component) { _ in isDirty = true }
                        }
                    }
                }

                // Read-only metadata row.
                GroupBox {
                    VStack(spacing: 8) {
                        metaRow("Reporter", value: appState.displayName(for: iss.reporter))
                        Divider()
                        metaRow("Created",  value: fmtDateTime(iss.createdAt))
                        Divider()
                        metaRow("Updated",  value: fmtDateTime(iss.updatedAt))
                        if !iss.resolvedAt.isEmpty {
                            Divider()
                            metaRow("Resolved", value: fmtDateTime(iss.resolvedAt))
                        }
                    }
                }

                // Description — multi-line TextEditor (no line limit).
                GroupBox("Description") {
                    TextEditor(text: $desc)
                        .frame(minHeight: 100)
                        .disabled(!canEdit)
                        .onChange(of: desc) { _ in isDirty = true }
                }

                if !saveError.isEmpty {
                    Text(saveError).foregroundStyle(.red).font(.callout)
                }

                commentsSection
            }
            .padding()
        }
    }

    // Helper for a label + value row in the metadata section.
    private func metaRow(_ label: String, value: String) -> some View {
        HStack {
            Text(label)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .frame(width: 80, alignment: .leading)
            Text(value)
                .font(.subheadline)
            Spacer()
        }
    }

    // MARK: - Comments section

    private var commentsSection: some View {
        GroupBox("Comments") {
            VStack(alignment: .leading, spacing: 12) {
                if comments.isEmpty {
                    Text("No comments yet.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
                // Display existing comments. CommentRow is defined below.
                ForEach(comments) { c in
                    CommentRow(
                        comment: c,
                        displayName: appState.displayName(for: c.author),
                        isAdmin: appState.currentUser?.isAdmin == true,
                        // The `onDelete` closure is called by CommentRow's trash button.
                        // `Task { }` bridges the synchronous button action into async.
                        onDelete: { Task { await deleteComment(c) } }
                    )
                }
                Divider()
                // New comment input area.
                VStack(alignment: .leading, spacing: 8) {
                    TextEditor(text: $newComment)
                        .frame(minHeight: 70)
                        .overlay(
                            RoundedRectangle(cornerRadius: 8)
                                .stroke(Color(.separator), lineWidth: 0.5)
                        )
                    Button(action: { Task { await postComment() } }) {
                        if isSavingComment {
                            HStack { ProgressView(); Text("Posting…") }
                        } else {
                            Text("Add Comment")
                        }
                    }
                    .buttonStyle(.bordered)
                    .disabled(newComment.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || isSavingComment)
                }
            }
        }
    }

    // MARK: - Toolbar

    @ToolbarContentBuilder
    private var detailToolbar: some ToolbarContent {
        // Only show editing controls when the current user may modify this issue.
        if let iss = issue {
            if appState.canModify(issue: iss) {
                // Delete button — admin only, shown in red via role: .destructive.
                ToolbarItem(placement: .topBarTrailing) {
                    if appState.currentUser?.isAdmin == true {
                        Button(role: .destructive, action: { showDeleteConfirm = true }) {
                            Label("Delete", systemImage: "trash")
                        }
                    }
                }
                // Save button — enabled only when the form is dirty and no save is in progress.
                ToolbarItem(placement: .topBarTrailing) {
                    Button(action: { Task { await save() } }) {
                        if isSaving {
                            ProgressView()
                        } else {
                            Text("Save")
                        }
                    }
                    .disabled(!isDirty || isSaving)
                }
            }
        }
    }

    // MARK: - Status change sheets

    // Presented when the user changes status from Open → Resolved.
    // The user can optionally add a fixed-version note and a comment.
    // `.presentationDetents` allows the sheet to be resized between medium
    // and large height on iOS 16+.
    private var resolveSheet: some View {
        NavigationStack {
            Form {
                Section {
                    Text("Optionally document the resolution before marking this issue Resolved.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
                Section("Fixed Version (optional)") {
                    TextField("e.g. 1.2-34", text: $scVersion)
                        .autocorrectionDisabled()
                }
                Section("Comment (optional)") {
                    TextEditor(text: $scComment)
                        .frame(minHeight: 80)
                }
                if !scError.isEmpty {
                    Section {
                        Text(scError).foregroundStyle(.red).font(.callout)
                    }
                }
            }
            .navigationTitle("Resolve Issue")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        // Restore the status picker to its pre-transition value.
                        status = origStatus
                        showResolveDialog = false
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Confirm") { Task { await confirmResolve() } }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    // Presented when the user changes status from Resolved → Open.
    // A reason comment is required to reopen.
    private var reopenSheet: some View {
        NavigationStack {
            Form {
                Section {
                    Text("A reason is required to reopen a resolved issue.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
                Section("Reason (required)") {
                    TextEditor(text: $scComment)
                        .frame(minHeight: 100)
                }
                if !scError.isEmpty {
                    Section {
                        Text(scError).foregroundStyle(.red).font(.callout)
                    }
                }
            }
            .navigationTitle("Reopen Issue")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") {
                        status = origStatus
                        showReopenDialog = false
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Confirm") { Task { await confirmReopen() } }
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    // MARK: - Actions

    // Load (or reload) the issue and its comments from the server.
    // Copies all fields into the local @State variables so the form
    // reflects the current server state.
    private func loadIssue() async {
        isLoading = true
        loadError = nil
        do {
            let resp = try await appState.api.getIssue(id: issueId)
            issue     = resp.issue
            comments  = resp.comments
            title     = resp.issue.title
            status    = resp.issue.status
            origStatus = resp.issue.status   // remember the "clean" status
            priority  = resp.issue.priority
            assignee  = resp.issue.assignee
            project   = resp.issue.project
            component = resp.issue.component
            desc      = resp.issue.description
            isDirty   = false    // form is in sync with server
            saveError = ""
        } catch APIError.unauthorized {
            await appState.signOut()
        } catch {
            loadError = error.localizedDescription
        }
        isLoading = false
    }

    // Called when the user taps Save. Validates locally, then either shows a
    // status-transition dialog or proceeds directly to doSave().
    private func save() async {
        guard let iss = issue else { return }
        saveError = ""

        if title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            saveError = "Title is required."; return
        }
        if project.isEmpty   { saveError = "Project is required.";   return }
        if component.isEmpty { saveError = "Component is required."; return }
        // Resolved issues must have an assignee — someone must own the fix.
        if status == "Resolved" && assignee.isEmpty {
            saveError = "An assignee is required before marking an issue Resolved."; return
        }

        // Status transitions require user confirmation and an optional comment.
        // Show the appropriate sheet and return — doSave() is called from the
        // sheet's Confirm button.
        if origStatus == "Open" && status == "Resolved" {
            scVersion = ""; scComment = ""; scError = ""
            showResolveDialog = true
            return
        }
        if origStatus == "Resolved" && status == "Open" {
            scComment = ""; scError = ""
            showReopenDialog = true
            return
        }

        // No status transition — save directly.
        await doSave(iss, commentBody: nil)
    }

    // Called from the resolve sheet's Confirm button.
    // Assembles the optional comment body from version + notes fields.
    private func confirmResolve() async {
        let comment = scComment.trimmingCharacters(in: .whitespacesAndNewlines)
        let version = scVersion.trimmingCharacters(in: .whitespacesAndNewlines)
        // Build "Fixed in X.Y\n\nComment text" — omit either part if empty.
        var parts: [String] = []
        if !version.isEmpty { parts.append("Fixed in \(version)") }
        if !comment.isEmpty { parts.append(comment) }
        // `joined(separator:)` concatenates an array of strings with a separator.
        let body = parts.isEmpty ? nil : parts.joined(separator: "\n\n")
        showResolveDialog = false
        if let iss = issue { await doSave(iss, commentBody: body) }
    }

    // Called from the reopen sheet's Confirm button.
    // Requires a non-empty reason before proceeding.
    private func confirmReopen() async {
        let comment = scComment.trimmingCharacters(in: .whitespacesAndNewlines)
        if comment.isEmpty { scError = "A reason is required to reopen an issue."; return }
        showReopenDialog = false
        if let iss = issue { await doSave(iss, commentBody: comment) }
    }

    // Core save function. Sends the PUT request, optionally posts a comment,
    // then refreshes both the issue and comments to reflect the server's state.
    private func doSave(_ iss: Issue, commentBody: String?) async {
        isSaving = true
        saveError = ""
        do {
            let updated = try await appState.api.updateIssue(
                id: iss.id,
                title: title.trimmingCharacters(in: .whitespacesAndNewlines),
                description: desc.trimmingCharacters(in: .whitespacesAndNewlines),
                priority: priority, status: status,
                assignee: assignee, project: project, component: component
            )
            // If the status transition produced a comment body, post it now.
            // `try?` makes a comment failure non-fatal — the status is already
            // updated, so the comment is best-effort.
            if let body = commentBody {
                _ = try? await appState.api.addComment(issueId: iss.id, body: body)
            }
            issue = updated
            origStatus = updated.status  // advance the "clean" status baseline
            isDirty = false
            // Reload comments to include any just-posted comment.
            let resp = try await appState.api.getIssue(id: iss.id)
            comments = resp.comments
        } catch APIError.unauthorized {
            await appState.signOut()
        } catch {
            saveError = error.localizedDescription
        }
        isSaving = false
    }

    private func postComment() async {
        let body = newComment.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { return }
        isSavingComment = true
        do {
            _ = try await appState.api.addComment(issueId: issueId, body: body)
            newComment = ""
            // Reload the full comment list to get the server-assigned ID and timestamp.
            let resp = try await appState.api.getIssue(id: issueId)
            comments = resp.comments
        } catch {}
        isSavingComment = false
    }

    private func deleteComment(_ c: Comment) async {
        do {
            try await appState.api.deleteComment(issueId: issueId, commentId: c.id)
            // Remove from local array immediately for a snappy UI response.
            // `removeAll(where:)` removes every element matching the predicate.
            comments.removeAll { $0.id == c.id }
        } catch {}
    }

    private func deleteIssue() async {
        do {
            try await appState.api.deleteIssue(id: issueId)
            // Clear the selection — ContentView shows the empty-detail placeholder.
            selectedIssueId = nil
        } catch {}
    }
}

// MARK: - Comment row
//
// A single comment: author name + timestamp on one line, body text below.
// Admins see a trash icon to delete the comment.
//
// `private` — only IssueDetailView uses this struct.

private struct CommentRow: View {
    let comment: Comment
    let displayName: String    // pre-resolved via userMap
    let isAdmin: Bool
    let onDelete: () -> Void   // closure called when the delete button is tapped

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(displayName)
                    .font(.subheadline.weight(.semibold))
                Spacer()
                Text(fmtDateTime(comment.createdAt))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                // Only render the trash button for admins.
                if isAdmin {
                    Button(role: .destructive, action: onDelete) {
                        Image(systemName: "trash")
                            .font(.caption)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.red)
                }
            }
            Text(comment.body)
                .font(.callout)
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Date formatting helpers
//
// These are free functions (not methods on a type) so they can be called from
// any file in the module without namespacing. They parse ISO 8601 timestamps
// from the server and produce locale-appropriate display strings.

func fmtDateTime(_ iso: String) -> String {
    // Guard returns the raw string if parsing fails — better to show the
    // original than an empty string.
    guard !iso.isEmpty,
          let date = ISO8601DateFormatter().date(from: iso) else { return iso }
    let fmt = DateFormatter()
    fmt.dateStyle = .medium   // e.g. "May 21, 2026"
    fmt.timeStyle = .short    // e.g. "3:45 PM"
    return fmt.string(from: date)
}

func fmtDate(_ iso: String) -> String {
    guard !iso.isEmpty,
          let date = ISO8601DateFormatter().date(from: iso) else { return iso }
    let fmt = DateFormatter()
    fmt.dateStyle = .medium
    fmt.timeStyle = .none     // date only, no time
    return fmt.string(from: date)
}
