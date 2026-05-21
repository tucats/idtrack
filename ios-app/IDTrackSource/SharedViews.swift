import SwiftUI

// MARK: - EmptyStatePlaceholder
//
// A reusable placeholder view shown when a list is empty or an error occurred.
// This replaces `ContentUnavailableView`, which was introduced in iOS 17.
// Since this app targets iOS 16, we provide our own equivalent.
//
// Used in:
//   • IssueListView   — no issues match the current filter
//   • IssueListView   — network error loading issues
//   • MainAppView     — no issue selected in the detail panel
//   • IssueDetailView — error loading issue details
//   • EditProjectsView — no projects exist yet
//
// To add a Retry button, supply both `action` and `actionLabel`:
//   EmptyStatePlaceholder(
//       icon: "exclamationmark.triangle",
//       title: "Error",
//       message: err,
//       action: { Task { await reload() } },
//       actionLabel: "Retry"
//   )
//
// For a plain informational placeholder, omit `action`:
//   EmptyStatePlaceholder(icon: "checkmark.circle", title: "All Done", message: "…")

struct EmptyStatePlaceholder: View {
    // SF Symbols name for the large icon (e.g. "exclamationmark.triangle").
    let icon: String
    // Short, bold headline.
    let title: String
    // Longer explanatory message shown below the title.
    let message: String
    // Optional callback for the action button. When nil, no button is rendered.
    // The type `(() -> Void)?` is an Optional closure — a function that takes
    // no arguments and returns nothing, or nil if no action is provided.
    var action: (() -> Void)? = nil
    // Label for the action button; ignored when action is nil.
    var actionLabel: String = "Retry"

    var body: some View {
        VStack(spacing: 16) {
            // `.font(.system(size: 48))` sets an absolute point size for the icon
            // so it remains consistent across all call sites regardless of
            // the surrounding text style.
            Image(systemName: icon)
                .font(.system(size: 48))
                .foregroundStyle(.secondary)
            Text(title)
                .font(.title3.weight(.semibold))
            Text(message)
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)
            // Only render the button when an action closure was provided.
            // `if let action = action` unwraps the Optional — the block
            // runs only when action is non-nil.
            if let action = action {
                Button(actionLabel, action: action)
                    .buttonStyle(.borderedProminent)
            }
        }
        // Expand to fill the available space so the content is centred in
        // whatever container this view is placed in.
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding()
    }
}
