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

    // `showNewIssue` is purely a local toolbar action so it stays as @State.
    // The other four sheet flags live on AppState because Mac Catalyst menu-bar
    // commands (see IDTrackApp) need to be able to trigger them too.
    @State private var showNewIssue      = false

    // Controls whether both columns of the NavigationSplitView are visible
    // simultaneously. `.all` shows sidebar + detail at once on iPad.
    @State private var columnVisibility: NavigationSplitViewVisibility = .all

    var body: some View {
        NavigationSplitView(columnVisibility: $columnVisibility) {
            // --- Sidebar column (always visible) ---
            //
            // On Mac Catalyst, the issue list is the primary working view —
            // it's naturally wide (many columns: id, title, project, priority,
            // status, assignee, dates) while the issue editor is comparatively
            // narrow. Override the default sidebar proportions so the user can
            // drag the divider up to ~2/3 of the window width.
            IssueListView(selectedIssueId: $selectedIssueId)
                .navigationTitle(appState.appName)
                // `listToolbar` is a computed property below; the `@ToolbarContentBuilder`
                // attribute lets it return multiple ToolbarItems as a group.
                .toolbar { listToolbar }
                #if targetEnvironment(macCatalyst)
                .navigationSplitViewColumnWidth(min: 360, ideal: 700, max: 1500)
                #endif
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
        .sheet(isPresented: $appState.showSettings)     { SettingsView() }
        .sheet(isPresented: $appState.showAbout)        { AboutView() }
        .sheet(isPresented: $appState.showManageUsers)  { ManageUsersView() }
        .sheet(isPresented: $appState.showEditProjects) { EditProjectsView() }
        // Explicitly re-inject `appState` here. On Mac Catalyst, sheets can be
        // presented in a separate window context whose environment does not
        // always inherit EnvironmentObjects from the presenter — passing it
        // through manually guarantees the lookup inside ManualView succeeds.
        .sheet(isPresented: $appState.showManual)       { ManualView().environmentObject(appState) }
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
        // On Mac Catalyst every item it used to host (About, Settings, Edit
        // Users, Edit Projects, Sign Out) has moved to the native menu bar
        // (see IDTrackApp.commands), so the whole ToolbarItem is omitted there.
        #if !targetEnvironment(macCatalyst)
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                Button(action: { appState.showAbout = true }) {
                    Label("About", systemImage: "info.circle")
                }
                Divider()
                Button(action: { appState.showSettings = true }) {
                    Label("Settings", systemImage: "gear")
                }
                // Admin-only items — only rendered when the current user has admin privileges.
                // `?.isAdmin == true` safely handles the case where currentUser is nil.
                if appState.currentUser?.isAdmin == true {
                    Button(action: { appState.showManageUsers = true }) {
                        Label("Edit Users…", systemImage: "person.2")
                    }
                    Button(action: { appState.showEditProjects = true }) {
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
        #endif
    }
}
