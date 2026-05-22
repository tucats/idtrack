import SwiftUI

// MARK: - IssueListView
//
// Displays a paginated, filterable, sortable list of issues.
//
// Architecture overview:
//   • Server-side filtering/sorting — every filter change triggers a fresh
//     GET /api/issues with the new parameters. The client holds no in-memory
//     copy of "all" issues; it only knows the current visible window.
//   • Infinite scroll — when the user scrolls to the last visible row,
//     loadNextPage() fetches the next page and appends it to `issues`.
//   • Stale-response prevention — `fetchGen` is an integer counter that is
//     incremented before every full reload. The async response checks that
//     the counter still matches before writing to state, discarding results
//     from any requests that were superseded by a later one (e.g. if the user
//     changes filters rapidly).
//   • Background polling — a Task loop runs every 30 seconds to detect
//     issues changed by other users on the same server.

struct IssueListView: View {
    @EnvironmentObject var appState: AppState

    // Shared with MainAppView: set when a row is tapped; read by IssueDetailView.
    // `@Binding` means IssueListView reads and writes the value owned by
    // MainAppView — changes flow both ways without copying.
    @Binding var selectedIssueId: Int?

    // MARK: Filter / sort state
    //
    // "open" / "High" / "all" are the sentinel strings used by the server API.
    // "all" means "no filter for this dimension".
    @State private var statusFilter   = "open"
    @State private var priorityFilter = "all"
    @State private var projectFilter  = "all"
    @State private var searchText     = ""
    @State private var sortCol        = "id"
    @State private var sortAsc        = false  // default: newest first

    // MARK: Pagination state
    @State private var issues:        [Issue] = []
    @State private var total:         Int = 0         // total matching rows on the server
    @State private var isLoading      = false          // true during the first-page fetch
    @State private var isLoadingMore  = false          // true during subsequent page fetches
    @State private var loadError:     String? = nil
    @State private var fetchGen       = 0              // incremented on each full reload
    @State private var lastSeenAt     = ""             // newest updatedAt seen; used for polling

    // MARK: Background polling
    //
    // `Task<Void, Never>?` — a handle to the polling task so we can cancel it
    // when the view disappears (`.onDisappear`). Without cancellation, the task
    // would continue running and writing to @State after the view is gone,
    // causing a SwiftUI warning.
    @State private var pollTask:  Task<Void, Never>? = nil
    @State private var newChanges = 0   // count of externally-changed issues

    // MARK: Search debounce
    //
    // We don't want to fire a network request for every keystroke. Instead, we
    // cancel any pending debounce task and start a fresh one; only the task
    // that survives the 350 ms delay actually calls reload().
    @State private var searchDebounce: Task<Void, Never>? = nil

    var body: some View {
        VStack(spacing: 0) {
            filterBar
            // Show the "new changes" banner only when there's something to report.
            if newChanges > 0 {
                refreshHint
            }
            issueList
        }
        // `.searchable` integrates with the navigation bar's built-in search
        // UI. `displayMode: .always` keeps the search bar permanently visible
        // rather than hiding it until the user scrolls up.
        .searchable(text: $searchText, placement: .navigationBarDrawer(displayMode: .always), prompt: "Search issues…")
        .onChange(of: searchText) { _ in debounceSearch() }
        // `.refreshable` adds pull-to-refresh support. The async closure runs
        // while the spinner is visible and must complete before it hides.
        .refreshable { await reload() }
        // `.task` fires once when the view appears (equivalent to onAppear but
        // async-aware). Load the first page and start the poll loop.
        .task { await initialLoad() }
        // Cancel the poll loop when the view leaves the hierarchy.
        .onDisappear { pollTask?.cancel() }
    }

    // MARK: - Filter bar

    private var filterBar: some View {
        // Horizontal ScrollView so the filter row can extend beyond the screen
        // width on small devices without wrapping or truncating.
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                // Each Picker + onChange follows the same pattern:
                // the picker updates the @State variable; onChange fires a reload.
                // `_ in` discards the new value argument (we read the @State directly).
                Picker("Status", selection: $statusFilter) {
                    Text("All").tag("all")
                    Text("Open").tag("open")
                    Text("Resolved").tag("resolved")
                    Text("Blocked").tag("blocked")
                    Text("Duplicate").tag("duplicate")
                }
                .pickerStyle(.menu)   // compact inline menu button style
                .onChange(of: statusFilter) { _ in Task { await reload() } }

                Divider().frame(height: 20)

                Picker("Priority", selection: $priorityFilter) {
                    Text("All Priority").tag("all")
                    Text("High").tag("High")
                    Text("Medium").tag("Medium")
                    Text("Low").tag("Low")
                }
                .pickerStyle(.menu)
                .onChange(of: priorityFilter) { _ in Task { await reload() } }

                Divider().frame(height: 20)

                // Project filter — populated from the cached project list.
                Picker("Project", selection: $projectFilter) {
                    Text("All Projects").tag("all")
                    ForEach(appState.projects) { proj in
                        Text(proj.name).tag(proj.name)
                    }
                }
                .pickerStyle(.menu)
                .onChange(of: projectFilter) { _ in Task { await reload() } }

                Divider().frame(height: 20)

                // Sort menu — each option passes the column name and direction
                // to sortButton(), which updates state and triggers a reload.
                Menu {
                    sortButton("ID (newest)", col: "id",         asc: false)
                    sortButton("ID (oldest)", col: "id",         asc: true)
                    sortButton("Title A–Z",   col: "title",      asc: true)
                    sortButton("Priority",    col: "priority",   asc: true)
                    sortButton("Status",      col: "status",     asc: true)
                    sortButton("Project",     col: "project",    asc: true)
                    sortButton("Created",     col: "created_at", asc: false)
                    sortButton("Updated",     col: "updated_at", asc: false)
                } label: {
                    Label("Sort", systemImage: "arrow.up.arrow.down")
                        .font(.callout)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
        }
        .background(Color(.secondarySystemBackground))
    }

    // Builds one sort-menu entry. A checkmark is shown next to the currently
    // active sort to give the user visual feedback. Returns `some View` because
    // the compiler infers the concrete type from the body.
    private func sortButton(_ label: String, col: String, asc: Bool) -> some View {
        Button(action: {
            sortCol = col; sortAsc = asc
            Task { await reload() }
        }) {
            HStack {
                Text(label)
                // Show a checkmark only when this entry matches the active sort.
                if sortCol == col && sortAsc == asc {
                    Image(systemName: "checkmark")
                }
            }
        }
    }

    // MARK: - Refresh hint banner

    // A yellow-tinted banner that appears when the background poll detects
    // issues created or modified by other users. The user can dismiss it or
    // tap "Refresh" to reload the full list.
    private var refreshHint: some View {
        HStack {
            Text("\(newChanges) new or updated issue\(newChanges == 1 ? "" : "s") available")
                .font(.callout)
            Spacer()
            Button("Refresh") { Task { newChanges = 0; await reload() } }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
            // Dismiss button — clears the count without reloading.
            Button { newChanges = 0 } label: {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Color(.systemYellow).opacity(0.2))
    }

    // MARK: - Issue list

    private var issueList: some View {
        Group {
            // Three possible states: loading, error, or data (possibly empty).
            if isLoading && issues.isEmpty {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let err = loadError {
                EmptyStatePlaceholder(
                    icon: "exclamationmark.triangle",
                    title: "Error",
                    message: err,
                    action: { Task { await reload() } },
                    actionLabel: "Retry"
                )
            } else if issues.isEmpty && !isLoading {
                EmptyStatePlaceholder(
                    icon: "checkmark.circle",
                    title: "No Issues",
                    message: "No issues match the current filter."
                )
            } else {
                // `List(issues, selection: $selectedIssueId)` renders each Issue
                // as a row. The `selection:` binding lets the List automatically
                // highlight the selected row and update the binding when the user
                // taps a different row.
                List(issues, selection: $selectedIssueId) { issue in
                    IssueRowView(issue: issue)
                        // `.tag` associates this row with the issue's id. When
                        // the row is tapped, the List sets selectedIssueId = issue.id.
                        .tag(issue.id)
                        // Infinite scroll trigger: when the last row appears on
                        // screen, request the next page. Guards prevent duplicate
                        // calls and unnecessary fetches.
                        .onAppear {
                            if issue.id == issues.last?.id && !isLoadingMore && issues.count < total {
                                Task { await loadNextPage() }
                            }
                        }
                }
                .listStyle(.plain)
                // Overlay a small "loading more" capsule at the bottom of the
                // list while an additional page is being fetched.
                .overlay(alignment: .bottom) {
                    if isLoadingMore {
                        ProgressView()
                            .padding()
                            .background(Material.bar)  // frosted-glass effect
                            .clipShape(Capsule())
                            .padding(.bottom, 8)
                    }
                }
            }
        }
        // Show "Showing N of M" / "N issues" counter in the bottom-left corner.
        .overlay(alignment: .bottomLeading) {
            if !isLoading && total > 0 {
                Text(counterText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(6)
            }
        }
    }

    // Produces the counter string, pluralising "issue" correctly.
    private var counterText: String {
        issues.count < total
            ? "Showing \(issues.count) of \(total)"
            : "\(total) issue\(total == 1 ? "" : "s")"
    }

    // MARK: - Loading

    private func initialLoad() async {
        await reload()
        startPolling()
    }

    // Full reload: fetch page 1 with the current filters/sort.
    // The `fetchGen` counter prevents a race condition where a slow
    // earlier request overwrites the results of a faster later one:
    //
    //   1. User changes filter → fetchGen becomes 3 → reload() starts.
    //   2. User changes filter again → fetchGen becomes 4 → second reload() starts.
    //   3. Second request returns first; it checks fetchGen == 4 → writes.
    //   4. First request returns; it checks fetchGen == 3, but fetchGen is now 4 → discards.
    private func reload() async {
        let gen = fetchGen + 1
        fetchGen = gen
        isLoading = true
        loadError = nil
        do {
            let resp = try await appState.api.getIssues(
                status: statusFilter, priority: priorityFilter,
                project: projectFilter, search: searchText.isEmpty ? nil : searchText,
                sort: sortCol, order: sortAsc ? "asc" : "desc",
                limit: appState.pageSize, offset: 0
            )
            // Discard if a newer reload was triggered while this was in flight.
            guard fetchGen == gen else { return }
            issues     = resp.issues
            total      = resp.total
            // Track the newest updatedAt so the poll can ask "what changed since X?".
            // `map(\.updatedAt)` is key-path syntax: shorthand for `map { $0.updatedAt }`.
            lastSeenAt = resp.issues.map(\.updatedAt).max() ?? ""
        } catch APIError.unauthorized {
            await appState.signOut()
        } catch {
            if fetchGen == gen { loadError = error.localizedDescription }
        }
        if fetchGen == gen { isLoading = false }
    }

    // Append the next page of issues to the existing list.
    // Uses the current `issues.count` as the offset, which works because
    // the server returns pages in a deterministic order for a given sort.
    private func loadNextPage() async {
        guard !isLoadingMore, issues.count < total else { return }
        isLoadingMore = true
        let gen = fetchGen  // capture current generation; don't increment
        do {
            let resp = try await appState.api.getIssues(
                status: statusFilter, priority: priorityFilter,
                project: projectFilter, search: searchText.isEmpty ? nil : searchText,
                sort: sortCol, order: sortAsc ? "asc" : "desc",
                limit: appState.pageSize, offset: issues.count
            )
            if fetchGen == gen {
                // `append(contentsOf:)` appends an entire sequence at once.
                issues.append(contentsOf: resp.issues)
                total = resp.total
                // Advance lastSeenAt if this page has newer timestamps.
                if let max = resp.issues.map(\.updatedAt).max(), max > lastSeenAt {
                    lastSeenAt = max
                }
            }
        } catch {}  // non-fatal; the user can pull-to-refresh if needed
        isLoadingMore = false
    }

    // Debounce search: cancel any pending search task and schedule a new one
    // to fire after 350 ms. If the user types another character before 350 ms
    // elapse, the old task is cancelled and a new one starts.
    private func debounceSearch() {
        searchDebounce?.cancel()
        searchDebounce = Task {
            try? await Task.sleep(for: .milliseconds(350))
            // `Task.isCancelled` is true if cancel() was called while sleeping.
            if !Task.isCancelled { await reload() }
        }
    }

    // MARK: - Polling

    // Start a long-lived Task that wakes up every 30 seconds and calls poll().
    // The task is stored in pollTask so it can be cancelled in onDisappear.
    private func startPolling() {
        pollTask?.cancel()
        pollTask = Task {
            // Infinite loop — runs until the task is cancelled.
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(30))
                if Task.isCancelled { break }
                await poll()
            }
        }
    }

    // Ask the server for issues changed since lastSeenAt.
    // Issues already in the visible list are updated in-place (unless they
    // are currently selected — don't stomp on edits in progress).
    // Entirely new issues increment newChanges to trigger the refresh banner.
    private func poll() async {
        guard !lastSeenAt.isEmpty else { return }
        do {
            let changed = try await appState.api.getChanges(since: lastSeenAt)
            guard !changed.isEmpty else { return }
            var external = 0
            for iss in changed {
                // `firstIndex(where:)` returns the index of the matching element, or nil.
                if let idx = issues.firstIndex(where: { $0.id == iss.id }) {
                    // Update in-place, but skip the currently-selected issue so
                    // any unsaved edits in IssueDetailView aren't discarded.
                    if iss.id != selectedIssueId {
                        issues[idx] = iss
                    }
                } else {
                    external += 1   // issue not in our window → new to us
                }
                // Advance lastSeenAt so the next poll only asks about newer changes.
                if iss.updatedAt > lastSeenAt { lastSeenAt = iss.updatedAt }
            }
            if external > 0 { newChanges += external }
        } catch {}  // network errors during polling are silently ignored
    }
}

// MARK: - Issue row view
//
// A compact three-line row showing the most important issue fields at a glance.
// The detailed information lives in IssueDetailView.

struct IssueRowView: View {
    @EnvironmentObject var appState: AppState
    let issue: Issue

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            // Top line: issue number (monospaced digits stay aligned) + badges
            HStack {
                Text("#\(issue.id)")
                    // `.monospacedDigit()` makes digits the same width, so
                    // issue numbers stay aligned vertically in the list.
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                Spacer()
                PriorityBadge(priority: issue.priority)
                StatusBadge(status: issue.status)
            }
            // Middle line: issue title, truncated to 2 lines
            Text(issue.title)
                .font(.body)
                .lineLimit(2)
            // Bottom line: project > component breadcrumb + comment count
            HStack(spacing: 4) {
                if !issue.project.isEmpty {
                    // `Label` combines an icon and text in a standard layout.
                    Label(issue.project, systemImage: "folder")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if !issue.component.isEmpty {
                    Text("›")
                        .foregroundStyle(.secondary)
                        .font(.caption)
                    Text(issue.component)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if issue.commentCount > 0 {
                    Label("\(issue.commentCount)", systemImage: "bubble.left")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Priority badge
//
// A small capsule-shaped label colour-coded by priority level.
// Designed to be consistent and reusable wherever a priority needs to be shown.

struct PriorityBadge: View {
    let priority: String

    var body: some View {
        Text(priority)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            // Semi-transparent fill: colour at 15% opacity so the badge is
            // visible but doesn't overwhelm the surrounding text.
            .background(color.opacity(0.15))
            .foregroundStyle(color)
            .clipShape(Capsule())
    }

    // `var color` without `private` allows PriorityBadge to be subclassed or
    // extended if needed in the future — though in practice these badges are
    // value types and unlikely to be subclassed.
    var color: Color {
        switch priority {
        case "High":   return .red
        case "Medium": return .orange
        default:       return .green    // "Low" and any unexpected value
        }
    }
}

// MARK: - Status badge

struct StatusBadge: View {
    let status: String

    var body: some View {
        Text(status)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundStyle(color)
            .clipShape(Capsule())
    }

    // Each status maps to a distinct colour so issues are scannable at a glance.
    var color: Color {
        switch status {
        case "Open":      return .blue
        case "Resolved":  return .gray
        case "Blocked":   return .orange
        case "Duplicate": return .purple
        default:          return .gray
        }
    }
}
