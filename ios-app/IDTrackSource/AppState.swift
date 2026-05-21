import Foundation
import Combine   // provides ObservableObject and @Published

// MARK: - AppState
//
// AppState is the single source of truth for the entire app. All views that
// need to read or write shared data do so through this object.
//
// `@MainActor` — a global actor that guarantees every method and property
// access on this class runs on the main thread. SwiftUI requires UI updates
// to happen on the main thread; annotating the whole class is safer than
// remembering to add `await MainActor.run { }` inside every async function.
//
// `ObservableObject` — a Combine protocol. Any class that conforms to it can
// have `@Published` properties. When a @Published property changes, SwiftUI
// automatically re-renders any view that reads that property.
//
// `class` (not `struct`) is required because ObservableObject needs a
// reference type — multiple views must share the same instance.

@MainActor
class AppState: ObservableObject {

    // MARK: - Published state
    //
    // `@Published` wraps a stored property in a Combine publisher.
    // The moment any @Published value changes, every SwiftUI view that
    // observes this object via @EnvironmentObject or @StateObject is
    // notified and re-renders if needed.

    @Published var isLoggedIn:   Bool = false
    @Published var currentUser:  User? = nil   // nil when signed out
    @Published var users:        [User] = []   // full user list; used for assignee dropdowns
    @Published var userMap:      [String: String] = [:]   // username → display name
    @Published var projects:     [Project] = []
    @Published var appName:      String = "idtrack"
    @Published var appDesc:      String = "Issue Tracker"
    @Published var idleTimeout:  Int = 0       // seconds; 0 = no timeout
    @Published var onboardingToken: String? = nil   // non-nil when server needs first user

    // These three properties persist their values to UserDefaults using
    // `didSet` — an observer block that runs immediately after the property
    // is assigned a new value. This keeps UserDefaults in sync automatically
    // without any extra "save" calls in the rest of the code.

    @Published var serverURL: String {
        didSet {
            UserDefaults.standard.set(serverURL, forKey: "serverURL")
            // Keep the API client in sync whenever the URL changes.
            api.setBaseURL(serverURL)
        }
    }
    @Published var keepLoggedIn: Bool {
        didSet { UserDefaults.standard.set(keepLoggedIn, forKey: "keepLoggedIn") }
    }
    @Published var darkMode: Bool {
        didSet { UserDefaults.standard.set(darkMode, forKey: "darkMode") }
    }
    @Published var pageSize: Int {
        didSet { UserDefaults.standard.set(pageSize, forKey: "pageSize") }
    }

    // MARK: - API client
    //
    // A single APIClient instance is shared across the whole app.
    // `let` ensures nobody can accidentally replace it.

    let api = APIClient()

    // MARK: - Init
    //
    // Restore all persisted preferences from UserDefaults on startup.
    // UserDefaults returns zero/false/nil for keys that have never been set,
    // so no "first launch" special-casing is needed.

    init() {
        let saved = UserDefaults.standard.string(forKey: "serverURL") ?? ""
        self.serverURL    = saved
        self.keepLoggedIn = UserDefaults.standard.bool(forKey: "keepLoggedIn")
        self.darkMode     = UserDefaults.standard.bool(forKey: "darkMode")
        // Validate pageSize against the allowed set; fall back to 50 if the
        // stored value is 0 (never set) or some unexpected number.
        let ps = UserDefaults.standard.integer(forKey: "pageSize")
        self.pageSize = [10, 25, 50, 100, 200].contains(ps) ? ps : 50
        if !saved.isEmpty { api.setBaseURL(saved) }
    }

    // MARK: - Helpers

    // Resolve a username to its display name via the cached userMap.
    // The `??` nil-coalescing operator returns the username itself as a
    // fallback if the key isn't in the map (e.g. a deleted user).
    func displayName(for username: String) -> String {
        userMap[username] ?? username
    }

    // Returns true when the current user may edit or delete the given issue.
    // Admins can always modify; reporters and assignees can modify their own.
    // `guard let` exits early with a default if currentUser is nil (not logged in).
    func canModify(issue: Issue) -> Bool {
        guard let u = currentUser else { return false }
        return u.isAdmin || u.username == issue.reporter || u.username == issue.assignee
    }

    // MARK: - Session management

    // Called after a successful login or onboarding.
    // Encodes the User as JSON and writes it to UserDefaults so the session
    // can be restored across app launches when keepLoggedIn is true.
    func signIn(user: User) {
        currentUser = user
        isLoggedIn  = true
        // `try?` silently ignores encoding errors — in practice they can't
        // happen for a simple Codable struct, but Swift still requires handling.
        if let data = try? JSONEncoder().encode(user) {
            UserDefaults.standard.set(data, forKey: "persistedUser")
        }
    }

    // Called on sign-out (user action, session expiry, or server error).
    // Clears all in-memory state so no data from the previous session bleeds
    // into a subsequent login by a different user on the same device.
    func signOut() async {
        // Tell the server to invalidate the session cookie.
        await api.logout()
        isLoggedIn      = false
        currentUser     = nil
        users           = []
        userMap         = [:]
        projects        = []
        onboardingToken = nil
        // Only wipe the persisted user when keepLoggedIn is off.
        // If it's on, leaving the data lets the next app launch restore it.
        if !keepLoggedIn {
            UserDefaults.standard.removeObject(forKey: "persistedUser")
        }
        // Delete all cookies from the shared jar. Without this, the old
        // session cookie would be sent on the first request of the next
        // login attempt, causing a stale-session error.
        if let cookies = HTTPCookieStorage.shared.cookies {
            cookies.forEach { HTTPCookieStorage.shared.deleteCookie($0) }
        }
    }

    // Returns the User stored in UserDefaults, but only when keepLoggedIn
    // is enabled. The caller (ContentView.boot) uses this to skip the login
    // screen on subsequent launches; if the server-side session has expired
    // the first API call will throw .unauthorized and signOut() is called.
    func loadPersistedUser() -> User? {
        // Multi-condition `guard let` — all three bindings must succeed or we fall through.
        guard keepLoggedIn,
              let data = UserDefaults.standard.data(forKey: "persistedUser"),
              let user = try? JSONDecoder().decode(User.self, from: data) else { return nil }
        return user
    }

    // MARK: - Data loading

    // Fetches the full user list and rebuilds userMap in one call.
    // `Dictionary(uniqueKeysWithValues:)` builds a [String: String] from an
    // array of (key, value) tuples. The ternary expression prefers the display
    // name but falls back to the username when display name is blank.
    func refreshUsers() async throws {
        let list = try await api.getUsers()
        users   = list
        userMap = Dictionary(uniqueKeysWithValues: list.map {
            ($0.username, $0.displayName.isEmpty ? $0.username : $0.displayName)
        })
    }

    func refreshProjects() async throws {
        projects = try await api.getProjects()
    }

    // Convenience accessor used by the cascaded project→component pickers.
    // `first(where:)` returns the first element satisfying the closure, or nil.
    // The `?.components ?? []` chain safely handles a nil result.
    func components(for projectName: String) -> [String] {
        projects.first(where: { $0.name == projectName })?.components ?? []
    }
}
