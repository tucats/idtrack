import Foundation

// MARK: - User
//
// Swift structs are value types — every assignment creates an independent copy.
// That means changes to one variable never silently affect another, making state
// management predictable in SwiftUI.
//
// Three protocol conformances are declared here:
//   • Identifiable — requires a property named `id`. SwiftUI's List and ForEach
//     use this to track items across updates without relying on array position.
//   • Codable      — shorthand for Encodable & Decodable; lets JSONDecoder/
//     JSONEncoder convert between Swift values and JSON automatically.
//   • Equatable    — lets Swift compare two values with ==. SwiftUI needs this
//     to detect changes and decide when to re-render a view.

struct User: Identifiable, Codable, Equatable {

    // `id` is a computed property that returns `username`. This satisfies
    // Identifiable without storing a separate integer key. The server
    // identifies users by username, so username IS the stable identity.
    var id: String { username }

    let username: String    // login name (immutable after creation)
    var displayName: String // human-readable name shown in the UI
    var isAdmin: Bool
    var lastLoginAt: String // RFC3339 timestamp string, empty if never logged in

    // MARK: JSON key mapping
    //
    // Swift convention is camelCase; the server sends snake_case JSON.
    // CodingKeys maps one to the other. Any key whose Swift name already
    // matches the JSON key (like `username`) can be omitted from the enum
    // and the compiler handles it automatically.
    enum CodingKeys: String, CodingKey {
        case username
        case displayName  = "display_name"
        case isAdmin      = "is_admin"
        case lastLoginAt  = "last_login_at"
    }

    // MARK: Memberwise initializer
    //
    // When we write a custom init(from decoder:) below, Swift no longer
    // auto-generates the memberwise initializer. We define it manually so
    // code can create a User directly without going through JSON decoding.
    init(username: String, displayName: String, isAdmin: Bool, lastLoginAt: String = "") {
        self.username = username
        self.displayName = displayName
        self.isAdmin = isAdmin
        self.lastLoginAt = lastLoginAt
    }

    // MARK: Custom JSON decoding
    //
    // `init(from decoder: Decoder)` is called automatically by JSONDecoder.
    // Writing it ourselves (instead of letting the compiler synthesize it)
    // lets us handle two defensive patterns:
    //
    //   1. `decodeIfPresent` — returns nil if the key is missing, so we can
    //      fall back to a sensible default with `?? ""`. The synthesized
    //      decoder would throw an error if a non-Optional property is absent.
    //
    //   2. `is_admin` type ambiguity — the server stores the boolean as a
    //      SQLite INTEGER (0/1), so older API responses may send an Int
    //      instead of a JSON bool. We try Bool first, then Int.
    init(from decoder: Decoder) throws {
        // `container(keyedBy:)` gives us a typed lookup table of the JSON object.
        let c = try decoder.container(keyedBy: CodingKeys.self)
        username    = try c.decode(String.self, forKey: .username)
        displayName = try c.decodeIfPresent(String.self, forKey: .displayName) ?? ""
        lastLoginAt = try c.decodeIfPresent(String.self, forKey: .lastLoginAt) ?? ""

        // Try to decode is_admin as a Bool (modern API sends true/false).
        // If that fails, try Int (legacy or SQLite-raw API sends 0/1).
        // Fall back to false if the key is absent entirely.
        if let b = try? c.decodeIfPresent(Bool.self, forKey: .isAdmin) {
            isAdmin = b
        } else if let i = try? c.decodeIfPresent(Int.self, forKey: .isAdmin) {
            isAdmin = (i) != 0
        } else {
            isAdmin = false
        }
    }
}

// MARK: - Issue
//
// An Issue holds all the fields the server stores for a bug/task record.
// `var` fields (not `let`) are editable in the detail form.

struct Issue: Identifiable, Codable, Equatable {
    let id: Int            // server-assigned auto-increment; immutable
    var title: String
    var description: String
    var reporter: String   // username of the person who filed the issue
    var assignee: String   // username of the person responsible; "" = unassigned
    var priority: String   // "High" | "Medium" | "Low"
    var status: String     // "Open" | "Resolved"
    var project: String
    var component: String
    var createdAt: String  // RFC3339 UTC strings
    var updatedAt: String
    var resolvedAt: String // empty if not yet resolved
    var commentCount: Int  // denormalized count from the server for fast display

    enum CodingKeys: String, CodingKey {
        case id, title, description, reporter, assignee, priority, status, project, component
        case createdAt    = "created_at"
        case updatedAt    = "updated_at"
        case resolvedAt   = "resolved_at"
        case commentCount = "comment_count"
    }

    // Custom decoder for the same reasons as User: defensive defaults for
    // every field so a partially-populated server response doesn't crash.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id           = try c.decode(Int.self, forKey: .id)
        title        = try c.decodeIfPresent(String.self, forKey: .title)       ?? ""
        description  = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
        reporter     = try c.decodeIfPresent(String.self, forKey: .reporter)    ?? ""
        assignee     = try c.decodeIfPresent(String.self, forKey: .assignee)    ?? ""
        priority     = try c.decodeIfPresent(String.self, forKey: .priority)    ?? "Medium"
        status       = try c.decodeIfPresent(String.self, forKey: .status)      ?? "Open"
        project      = try c.decodeIfPresent(String.self, forKey: .project)     ?? ""
        component    = try c.decodeIfPresent(String.self, forKey: .component)   ?? ""
        createdAt    = try c.decodeIfPresent(String.self, forKey: .createdAt)   ?? ""
        updatedAt    = try c.decodeIfPresent(String.self, forKey: .updatedAt)   ?? ""
        resolvedAt   = try c.decodeIfPresent(String.self, forKey: .resolvedAt)  ?? ""
        commentCount = try c.decodeIfPresent(Int.self,    forKey: .commentCount) ?? 0
    }
}

// MARK: - Comment

struct Comment: Identifiable, Codable {
    let id: Int
    let author: String     // username (not display name)
    let body: String
    let createdAt: String

    enum CodingKeys: String, CodingKey {
        case id, author, body
        case createdAt = "created_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id        = try c.decode(Int.self, forKey: .id)
        author    = try c.decodeIfPresent(String.self, forKey: .author)    ?? ""
        body      = try c.decodeIfPresent(String.self, forKey: .body)      ?? ""
        createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
    }
}

// MARK: - Project
//
// A Project is a container for Components. The server encodes it as:
//   { "name": "Backend", "components": ["API", "Auth"] }
//
// Project has no CodingKeys because its Swift names already match the JSON keys.
// The compiler synthesizes the decoder automatically.

struct Project: Identifiable, Codable, Equatable {
    // Like User.id, we use the natural string key rather than a separate integer.
    var id: String { name }
    let name: String
    var components: [String]
}

// MARK: - Helpers
//
// Swift extensions let you add methods or computed properties to an existing
// type without modifying its original declaration. This is especially useful
// for organising view-related logic separately from the model definition.

extension Issue {
    // Returns a color name string used by PriorityBadge.
    // Returning a String (not a Color directly) keeps the model layer free
    // of SwiftUI imports — Models.swift only imports Foundation.
    var priorityColor: String {
        switch priority {
        case "High":   return "red"
        case "Medium": return "orange"
        case "Low":    return "green"
        default:       return "gray"
        }
    }
}
