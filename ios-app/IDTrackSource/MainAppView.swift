import SwiftUI

// MARK: - MainAppView
//
// The top-level view after login. It uses NavigationSplitView to produce an
// adaptive two-column layout:
//
//   • iPhone (compact width) — behaves like a NavigationStack: tapping an
//     issue in the list pushes IssueDetailView onto the stack. The back
//     button returns to the list.
//   • iPad (regular width)   — shows IssueListView in the sidebar column and
//     IssueDetailView in the detail column side-by-side.
//
// This adaptive behaviour is provided by NavigationSplitView automatically;
// no device-detection code is needed.
//
// MainAppView also owns all the `showX` Bool flags that present sheets
// (modal forms). Keeping them here rather than inside IssueListView or
// IssueDetailView means any sub-view can trigger them by reading a shared
// binding, and the sheet lifecycle is tied to the whole-app level.

struct MainAppView: View {
    @EnvironmentObject var appState: AppState

    // The selected issue ID is declared here and passed as a Binding to both
    // IssueListView (which sets it when a row is tapped) and IssueDetailView
    // (which reads it to know which issue to load, and clears it to "deselect"
    // after a delete). Using a shared @State means both panels always agree.
    @State private var selectedIssueId: Int? = nil

    // Each Bool drives one `.sheet(isPresented:)` call below.
    @State private var showNewIssue      = false
    @State private var showSettings      = false
    @State private var showAbout         = false
    @State private var showManageUsers   = false
    @State private var showEditProjects  = false

    // Controls whether both columns of the NavigationSplitView are visible
    // simultaneously. `.all` shows sidebar + detail at once on iPad.
    @State private var columnVisibility: NavigationSplitViewVisibility = .all

    var body: some View {
        NavigationSplitView(columnVisibility: $columnVisibility) {
            // --- Sidebar column (always visible) ---
            IssueListView(selectedIssueId: $selectedIssueId)
                .navigationTitle(appState.appName)
                // `listToolbar` is a computed property below; the `@ToolbarContentBuilder`
                // attribute lets it return multiple ToolbarItems as a group.
                .toolbar { listToolbar }
        } detail: {
            // --- Detail column ---
            // Show a placeholder when nothing is selected (common on iPad at
            // launch before the user taps an issue).
            if let id = selectedIssueId {
                IssueDetailView(issueId: id, selectedIssueId: $selectedIssueId)
            } else {
                EmptyStatePlaceholder(
                    icon: "list.bullet.clipboard",
                    title: "No Issue Selected",
                    message: "Select an issue from the list."
                )
            }
        }
        // --- Sheets ---
        // Each `.sheet(isPresented:)` presents a modal when the corresponding
        // Bool flips to true. SwiftUI dismisses the sheet and resets the Bool
        // when the user swipes it down or it calls dismiss().
        .sheet(isPresented: $showNewIssue) {
            // The closure is called back with the new issue's ID on success,
            // or -1 on cancel. We close the sheet either way, and only set
            // selectedIssueId for valid IDs.
            NewIssueView(onCreated: { id in
                showNewIssue   = false
                if id > 0 { selectedIssueId = id }
            })
        }
        .sheet(isPresented: $showSettings)     { SettingsView() }
        .sheet(isPresented: $showAbout)        { AboutView() }
        .sheet(isPresented: $showManageUsers)  { ManageUsersView() }
        .sheet(isPresented: $showEditProjects) { EditProjectsView() }
    }

    // MARK: - Toolbar
    //
    // `@ToolbarContentBuilder` is a result builder that assembles multiple
    // ToolbarItem / ToolbarItemGroup values into a single ToolbarContent value.
    // The `some ToolbarContent` return type is an opaque type — the exact
    // concrete type is inferred by the compiler; callers only see the protocol.

    @ToolbarContentBuilder
    private var listToolbar: some ToolbarContent {
        // "+" button — opens the New Issue form sheet.
        ToolbarItem(placement: .topBarTrailing) {
            Button(action: { showNewIssue = true }) {
                Label("New Issue", systemImage: "plus")
            }
        }

        // Hamburger / "…" menu — groups less-frequent actions behind a tap.
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                Button(action: { showAbout = true }) {
                    Label("About", systemImage: "info.circle")
                }
                Divider()
                Button(action: { showSettings = true }) {
                    Label("Settings", systemImage: "gear")
                }
                // Admin-only items — only rendered when the current user has admin privileges.
                // `?.isAdmin == true` safely handles the case where currentUser is nil.
                if appState.currentUser?.isAdmin == true {
                    Button(action: { showManageUsers = true }) {
                        Label("Edit Users…", systemImage: "person.2")
                    }
                    Button(action: { showEditProjects = true }) {
                        Label("Edit Projects…", systemImage: "folder")
                    }
                }
                Divider()
                // `role: .destructive` colours the button red on iOS, signalling
                // that the action has serious consequences (signs out the user).
                Button(role: .destructive, action: {
                    Task { await appState.signOut() }
                }) {
                    Label("Sign Out", systemImage: "rectangle.portrait.and.arrow.right")
                }
            } label: {
                Label("Menu", systemImage: "ellipsis.circle")
            }
        }
    }
}
