import Foundation

// MARK: - Errors
//
// An enum is a great fit for a closed set of error cases because the compiler
// enforces exhaustive handling in switch statements.
//
// `LocalizedError` is a protocol that lets Swift's error system produce
// human-readable messages. Conforming to it means `error.localizedDescription`
// returns our custom string rather than a generic system message.

enum APIError: LocalizedError {
    case invalidURL
    case unauthorized
    case serverError(String)       // associated value carries the server's message
    case decodingError(String)
    case networkError(String)

    // `errorDescription` is the one required property of LocalizedError.
    // Returning an Optional String (String?) is a protocol requirement; we
    // always provide a value so the UI never shows a blank error.
    var errorDescription: String? {
        switch self {
        case .invalidURL:           return "Invalid server URL"
        case .unauthorized:         return "Session expired — please sign in again"
        case .serverError(let m):   return m
        case .decodingError(let m): return "Data error: \(m)"
        case .networkError(let m):  return m
        }
    }
}

// MARK: - Certificate bypass delegate
//
// The idtrack server ships with a self-signed TLS certificate baked into the
// binary. iOS normally rejects self-signed certs. This delegate tells URLSession
// to accept the certificate unconditionally.
//
// WARNING: Do not use this approach for production apps communicating with
// servers you don't control. It disables certificate trust validation and
// makes the connection vulnerable to man-in-the-middle attacks. It is
// acceptable here only because users connect to their own self-hosted server.
//
// `private` — only this file can create or reference TrustAllDelegate.
// `class` (not `struct`) because URLSessionDelegate requires a reference type
// (NSObject subclass) to work with Objective-C runtime delegation.

private class TrustAllDelegate: NSObject, URLSessionDelegate {
    // This method is called whenever URLSession receives a server authentication
    // challenge (e.g., verifying a TLS certificate).
    func urlSession(
        _ session: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        // Only bypass for server trust (TLS cert) challenges.
        // Other challenge types (e.g. client certificates) fall back to default.
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        // Accept the cert and wrap the trust object in a URLCredential.
        completionHandler(.useCredential, URLCredential(trust: trust))
    }
}

// MARK: - API Client
//
// APIClient is the single point of contact with the idtrack REST API.
// All network calls are `async throws` functions: `async` means they can
// suspend without blocking the UI thread; `throws` means callers must handle
// errors with do/catch or propagate them with `try`.
//
// Using `class` (reference type) is intentional: AppState holds one instance
// and many views share it. Reference semantics mean they all see the same
// baseURL, session, and cookie jar.

class APIClient {

    // The delegate instance is kept alive for the session's lifetime.
    private let delegate = TrustAllDelegate()

    // `lazy var` — the URLSession is created on first access, not at init time.
    // This avoids doing work until it's actually needed, and lets `delegate`
    // be fully initialised before the session references it.
    //
    // The trailing closure `{ ... }()` is an immediately-invoked closure that
    // returns the configured URLSession.
    private lazy var session: URLSession = {
        let cfg = URLSessionConfiguration.default
        // Always accept and send cookies (the session token travels as a cookie).
        cfg.httpCookieAcceptPolicy = .always
        cfg.httpShouldSetCookies   = true
        // Use the shared cookie storage so cookies survive app restarts and are
        // accessible to all URLSession instances in the process.
        cfg.httpCookieStorage      = HTTPCookieStorage.shared
        cfg.timeoutIntervalForRequest  = 30  // seconds per request attempt
        cfg.timeoutIntervalForResource = 60  // seconds for the entire transfer
        // `delegateQueue: nil` means URLSession creates its own serial queue.
        return URLSession(configuration: cfg, delegate: delegate, delegateQueue: nil)
    }()

    // `private(set)` — external code can read baseURL but only APIClient can
    // write it. This enforces that the URL is always set via setBaseURL(),
    // which normalises the value.
    private(set) var baseURL: String = ""

    // Normalise the URL: trim whitespace, remove trailing slashes so every
    // path concatenation in makeURL starts clean (e.g. "/api/login").
    func setBaseURL(_ raw: String) {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasSuffix("/") { s = String(s.dropLast()) }
        baseURL = s
    }

    // MARK: - Core request helpers (private to this class)

    // Builds a URL by appending a path to baseURL.
    // Throws invalidURL if baseURL is empty or the resulting string can't parse.
    private func makeURL(_ path: String) throws -> URL {
        guard !baseURL.isEmpty, let url = URL(string: baseURL + path) else {
            throw APIError.invalidURL
        }
        return url
    }

    // Builds a URLRequest with an optional JSON body.
    // When a body is provided, Content-Type is set automatically — the server
    // requires this header to accept POST/PUT requests.
    private func req(_ url: URL, method: String = "GET", body: Data? = nil) -> URLRequest {
        var r = URLRequest(url: url)
        r.httpMethod = method
        if let b = body {
            r.httpBody = b
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return r
    }

    // Executes a request and returns the raw response bytes.
    // Translates HTTP status codes into typed Swift errors so callers don't
    // have to inspect raw status codes.
    //
    // `session.data(for:)` is an async function that suspends the caller until
    // the network response arrives, without blocking any thread.
    private func perform(_ request: URLRequest) async throws -> Data {
        let (data, response) = try await session.data(for: request)
        // Cast to HTTPURLResponse to access the status code.
        guard let http = response as? HTTPURLResponse else {
            throw APIError.networkError("Invalid response")
        }
        // 401 Unauthorized means the session cookie has expired.
        // Views watch for this and call signOut() to return to the login screen.
        if http.statusCode == 401 { throw APIError.unauthorized }
        if http.statusCode >= 400 {
            // Try to parse the server's JSON error body: {"error": "message"}.
            if let obj = try? JSONDecoder().decode([String: String].self, from: data),
               let msg = obj["error"] {
                throw APIError.serverError(msg)
            }
            throw APIError.serverError("Server error \(http.statusCode)")
        }
        return data
    }

    // Generic helper: performs the request AND decodes the JSON body into type T.
    //
    // `<T: Decodable>` is a generic type parameter. The caller doesn't pass T
    // explicitly — Swift infers it from the return type at the call site:
    //   let user: User = try await decode(req(url))   // T is inferred as User
    //   let resp: IssueListResponse = try await decode(req(url))  // T = IssueListResponse
    private func decode<T: Decodable>(_ request: URLRequest) async throws -> T {
        let data = try await perform(request)
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw APIError.decodingError(error.localizedDescription)
        }
    }

    // MARK: - Status & Version
    //
    // Response types are defined as nested structs inside the method group
    // they belong to. This co-location makes it easy to see exactly what JSON
    // shape each endpoint returns without jumping to a separate file.

    struct StatusResponse: Codable {
        let idleTimeout:    Int?     // seconds; nil means the key was absent
        let appName:        String?
        let appDescription: String?
        let onboarding:     Bool?    // true when the server has no users yet
        let token:          String?  // one-time UUID for the onboarding endpoint
        enum CodingKeys: String, CodingKey {
            case idleTimeout    = "idle_timeout"
            case appName        = "app_name"
            case appDescription = "app_description"
            case onboarding, token
        }
    }

    func getStatus() async throws -> StatusResponse {
        let url = try makeURL("/api/status")
        return try await decode(req(url))
    }

    struct VersionResponse: Codable {
        let version:   String?
        let buildTime: String?   // 14-digit compact UTC timestamp: YYYYMMDDHHmmSS
        enum CodingKeys: String, CodingKey {
            case version
            case buildTime = "build_time"
        }
    }

    func getVersion() async throws -> VersionResponse {
        let url = try makeURL("/api/version")
        return try await decode(req(url))
    }

    // MARK: - Auth

    // Login request body. Defining it as a nested struct keeps the shape
    // right next to the function that uses it.
    struct LoginRequest: Codable {
        let username: String
        let password: String
        let keepLoggedIn: Bool
        enum CodingKeys: String, CodingKey {
            case username, password
            case keepLoggedIn = "keep_logged_in"
        }
    }

    // Returns the logged-in User on success. The server also sets an
    // HttpOnly session cookie — URLSession stores it in HTTPCookieStorage
    // automatically and sends it on every subsequent request.
    func login(username: String, password: String, keepLoggedIn: Bool) async throws -> User {
        let url  = try makeURL("/api/login")
        let body = try JSONEncoder().encode(LoginRequest(username: username, password: password, keepLoggedIn: keepLoggedIn))
        return try await decode(req(url, method: "POST", body: body))
    }

    // logout() is fire-and-forget: the app clears local state regardless of
    // whether the server call succeeds (e.g. when the server is unreachable).
    // That's why errors are silently ignored with `try?`.
    func logout() async {
        guard let url = try? makeURL("/api/logout") else { return }
        _ = try? await perform(req(url, method: "POST"))
    }

    // The onboarding endpoint creates the first admin account.
    // It requires HTTP Basic auth with the one-time token from GET /api/status.
    // The token is base64-encoded as "onboarding:<uuid>" per the HTTP Basic spec.
    func onboard(username: String, displayName: String, password: String, token: String) async throws -> User {
        // Nested struct — only used in this function, so no need to expose it.
        struct Body: Codable {
            let username: String
            let displayName: String
            let password: String
            enum CodingKeys: String, CodingKey {
                case username, password
                case displayName = "display_name"
            }
        }
        let url  = try makeURL("/api/onboarding")
        let body = try JSONEncoder().encode(Body(username: username, displayName: displayName, password: password))
        var r = req(url, method: "POST", body: body)
        // Build the Basic auth header: Base64("onboarding:<token>")
        let cred = Data("onboarding:\(token)".utf8).base64EncodedString()
        r.setValue("Basic \(cred)", forHTTPHeaderField: "Authorization")
        return try await decode(r)
    }

    // MARK: - Users
    //
    // The server wraps arrays in an object: { "users": [...] }
    // A private envelope struct unwraps that outer layer so callers
    // receive the array directly.

    private struct UsersEnvelope: Codable { let users: [User] }

    func getUsers() async throws -> [User] {
        let url = try makeURL("/api/users")
        let env: UsersEnvelope = try await decode(req(url))
        return env.users
    }

    func createUser(username: String, displayName: String, password: String, isAdmin: Bool) async throws {
        struct Body: Encodable {
            let username: String
            let displayName: String
            let password: String
            let isAdmin: Bool
            enum CodingKeys: String, CodingKey {
                case username, password
                case displayName = "display_name"
                case isAdmin     = "is_admin"
            }
        }
        let url  = try makeURL("/api/users")
        let body = try JSONEncoder().encode(Body(username: username, displayName: displayName, password: password, isAdmin: isAdmin))
        // `_` discards the Data return value — we only care whether it throws.
        _ = try await perform(req(url, method: "POST", body: body))
    }

    func updateUser(username: String, displayName: String, password: String, isAdmin: Bool) async throws {
        struct Body: Encodable {
            let displayName: String
            let password: String
            let isAdmin: Bool
            enum CodingKeys: String, CodingKey {
                case password
                case displayName = "display_name"
                case isAdmin     = "is_admin"
            }
        }
        // URL-encode the username in case it contains special characters
        // (spaces, @-signs, etc.) that would break the URL path.
        let enc = username.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? username
        let url  = try makeURL("/api/users/\(enc)")
        let body = try JSONEncoder().encode(Body(displayName: displayName, password: password, isAdmin: isAdmin))
        _ = try await perform(req(url, method: "PUT", body: body))
    }

    func deleteUser(username: String) async throws {
        let enc = username.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? username
        let url = try makeURL("/api/users/\(enc)")
        _ = try await perform(req(url, method: "DELETE"))
    }

    // MARK: - Projects

    private struct ProjectsEnvelope: Codable { let projects: [Project] }

    func getProjects() async throws -> [Project] {
        let url = try makeURL("/api/projects")
        let env: ProjectsEnvelope = try await decode(req(url))
        return env.projects
    }

    func createProject(name: String) async throws {
        let url  = try makeURL("/api/projects")
        // Encoding a plain [String: String] dictionary produces {"name": "..."}.
        let body = try JSONEncoder().encode(["name": name])
        _ = try await perform(req(url, method: "POST", body: body))
    }

    func deleteProject(name: String) async throws {
        let enc = name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? name
        let url = try makeURL("/api/projects/\(enc)")
        _ = try await perform(req(url, method: "DELETE"))
    }

    func createComponent(project: String, name: String) async throws {
        let encP = project.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? project
        let url  = try makeURL("/api/projects/\(encP)/components")
        let body = try JSONEncoder().encode(["name": name])
        _ = try await perform(req(url, method: "POST", body: body))
    }

    func deleteComponent(project: String, component: String) async throws {
        let encP = project.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? project
        let encC = component.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? component
        let url = try makeURL("/api/projects/\(encP)/components/\(encC)")
        _ = try await perform(req(url, method: "DELETE"))
    }

    // MARK: - Issues

    // Server response for the list endpoint. When `limit > 0` the server also
    // returns `total` so the UI can show "Showing N of M" and know when all
    // pages have been loaded.
    struct IssueListResponse: Codable {
        let issues: [Issue]
        let total:  Int
        let offset: Int?
        let limit:  Int?
    }

    // Response for a single issue fetch; includes comments in one round-trip.
    struct IssueDetailResponse: Codable {
        let issue:    Issue
        let comments: [Comment]
    }

    // The create/update endpoints wrap the issue in {"issue": {...}}.
    private struct IssueEnvelope: Codable { let issue: Issue }

    // Build the query string for the issue list. URLComponents handles
    // percent-encoding of query parameter values automatically.
    func getIssues(
        status: String?, priority: String?, project: String?, search: String?,
        sort: String, order: String, limit: Int, offset: Int
    ) async throws -> IssueListResponse {
        guard !baseURL.isEmpty, var comps = URLComponents(string: baseURL + "/api/issues") else {
            throw APIError.invalidURL
        }
        var items: [URLQueryItem] = []
        // Only include filter parameters if they carry a real value.
        // "all" is the client-side sentinel for "no filter" — don't send it.
        if let s = status,   s != "all", !s.isEmpty   { items.append(.init(name: "status",   value: s)) }
        if let p = priority, p != "all", !p.isEmpty   { items.append(.init(name: "priority", value: p)) }
        if let p = project,  p != "all", !p.isEmpty   { items.append(.init(name: "project",  value: p)) }
        if let q = search,   !q.isEmpty               { items.append(.init(name: "search",   value: q)) }
        items.append(.init(name: "sort",   value: sort))
        items.append(.init(name: "order",  value: order))
        items.append(.init(name: "limit",  value: String(limit)))
        items.append(.init(name: "offset", value: String(offset)))
        comps.queryItems = items
        guard let url = comps.url else { throw APIError.invalidURL }
        return try await decode(req(url))
    }

    func getIssue(id: Int) async throws -> IssueDetailResponse {
        let url = try makeURL("/api/issues/\(id)")
        return try await decode(req(url))
    }

    func createIssue(
        title: String, description: String, priority: String,
        assignee: String, project: String, component: String
    ) async throws -> Issue {
        struct Body: Encodable {
            let title, description, priority, assignee, project, component: String
        }
        let url  = try makeURL("/api/issues")
        let body = try JSONEncoder().encode(Body(title: title, description: description, priority: priority, assignee: assignee, project: project, component: component))
        let env: IssueEnvelope = try await decode(req(url, method: "POST", body: body))
        return env.issue
    }

    func updateIssue(
        id: Int, title: String, description: String, priority: String,
        status: String, assignee: String, project: String, component: String
    ) async throws -> Issue {
        struct Body: Encodable {
            let title, description, priority, status, assignee, project, component: String
        }
        let url  = try makeURL("/api/issues/\(id)")
        let body = try JSONEncoder().encode(Body(title: title, description: description, priority: priority, status: status, assignee: assignee, project: project, component: component))
        let env: IssueEnvelope = try await decode(req(url, method: "PUT", body: body))
        return env.issue
    }

    func deleteIssue(id: Int) async throws {
        let url = try makeURL("/api/issues/\(id)")
        _ = try await perform(req(url, method: "DELETE"))
    }

    // Poll for issues updated since a given timestamp.
    // Used by the background polling loop to detect changes made by other users.
    func getChanges(since: String) async throws -> [Issue] {
        guard !baseURL.isEmpty,
              var comps = URLComponents(string: baseURL + "/api/issues/changes") else {
            throw APIError.invalidURL
        }
        comps.queryItems = [URLQueryItem(name: "since", value: since)]
        guard let url = comps.url else { throw APIError.invalidURL }
        let resp: IssueListResponse = try await decode(req(url))
        return resp.issues
    }

    // MARK: - Comments

    // The server wraps the created comment in {"comment": {...}}.
    private struct CommentEnvelope: Codable { let comment: Comment }

    func addComment(issueId: Int, body: String) async throws -> Comment {
        let url  = try makeURL("/api/issues/\(issueId)/comments")
        let data = try JSONEncoder().encode(["body": body])
        let env: CommentEnvelope = try await decode(req(url, method: "POST", body: data))
        return env.comment
    }

    func deleteComment(issueId: Int, commentId: Int) async throws {
        let url = try makeURL("/api/issues/\(issueId)/comments/\(commentId)")
        _ = try await perform(req(url, method: "DELETE"))
    }
}
