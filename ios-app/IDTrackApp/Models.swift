import Foundation

// MARK: - Team

struct Team: Identifiable, Codable, Equatable {
    var id: String { name }
    let name: String
    var description: String

    static let reservedNames: Set<String> = ["admin", "any"]

    var isReserved: Bool { Team.reservedNames.contains(name.lowercased()) }

    init(name: String, description: String = "") {
        self.name = name
        self.description = description
    }
}

// MARK: - User

struct User: Identifiable, Codable, Equatable {
    var id: String { username }

    let username: String
    var displayName: String
    var teams: [String]
    var lastLoginAt: String

    // Computed from teams so callers throughout the app continue to work
    // without changes: u.isAdmin still returns true when "admin" is in teams.
    var isAdmin: Bool { teams.contains { $0.lowercased() == "admin" } }

    enum CodingKeys: String, CodingKey {
        case username
        case displayName  = "display_name"
        case teams
        case isAdminRaw   = "is_admin"
        case lastLoginAt  = "last_login_at"
    }

    init(username: String, displayName: String, teams: [String] = ["any"], lastLoginAt: String = "") {
        self.username    = username
        self.displayName = displayName
        self.teams       = teams
        self.lastLoginAt = lastLoginAt
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        username    = try c.decode(String.self, forKey: .username)
        displayName = try c.decodeIfPresent(String.self, forKey: .displayName) ?? ""
        lastLoginAt = try c.decodeIfPresent(String.self, forKey: .lastLoginAt) ?? ""

        // Prefer the explicit teams array. Fall back to deriving teams from the
        // legacy is_admin flag so the app still works against older server versions.
        if let t = try? c.decodeIfPresent([String].self, forKey: .teams), !t.isEmpty {
            teams = t
        } else {
            var adminFlag = false
            if let b = try? c.decodeIfPresent(Bool.self, forKey: .isAdminRaw) {
                adminFlag = b
            } else if let i = try? c.decodeIfPresent(Int.self, forKey: .isAdminRaw) {
                adminFlag = i != 0
            }
            teams = adminFlag ? ["admin"] : ["any"]
        }
    }
}

// MARK: - Issue

struct Issue: Identifiable, Codable, Equatable {
    let id: Int
    var title: String
    var description: String
    var reporter: String
    var assignee: String
    var priority: String
    var status: String
    var project: String
    var component: String
    var createdAt: String
    var updatedAt: String
    var resolvedAt: String
    var commentCount: Int
    var dependentIssues: [Int]
    var teams: [String]

    enum CodingKeys: String, CodingKey {
        case id, title, description, reporter, assignee, priority, status, project, component, teams
        case createdAt       = "created_at"
        case updatedAt       = "updated_at"
        case resolvedAt      = "resolved_at"
        case commentCount    = "comment_count"
        case dependentIssues = "dependent_issues"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id               = try c.decode(Int.self, forKey: .id)
        title            = try c.decodeIfPresent(String.self, forKey: .title)          ?? ""
        description      = try c.decodeIfPresent(String.self, forKey: .description)    ?? ""
        reporter         = try c.decodeIfPresent(String.self, forKey: .reporter)        ?? ""
        assignee         = try c.decodeIfPresent(String.self, forKey: .assignee)        ?? ""
        priority         = try c.decodeIfPresent(String.self, forKey: .priority)        ?? "Medium"
        status           = try c.decodeIfPresent(String.self, forKey: .status)          ?? "Open"
        project          = try c.decodeIfPresent(String.self, forKey: .project)         ?? ""
        component        = try c.decodeIfPresent(String.self, forKey: .component)       ?? ""
        createdAt        = try c.decodeIfPresent(String.self, forKey: .createdAt)       ?? ""
        updatedAt        = try c.decodeIfPresent(String.self, forKey: .updatedAt)       ?? ""
        resolvedAt       = try c.decodeIfPresent(String.self, forKey: .resolvedAt)      ?? ""
        commentCount     = try c.decodeIfPresent(Int.self,    forKey: .commentCount)    ?? 0
        dependentIssues  = try c.decodeIfPresent([Int].self,  forKey: .dependentIssues) ?? []
        teams            = try c.decodeIfPresent([String].self, forKey: .teams)         ?? ["any"]
    }
}

// MARK: - Comment

struct Comment: Identifiable, Codable {
    let id: Int
    let author: String
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

struct Project: Identifiable, Codable, Equatable {
    var id: String { name }
    let name: String
    var components: [String]
    var teams: [String]

    init(name: String, components: [String] = [], teams: [String] = ["any"]) {
        self.name       = name
        self.components = components
        self.teams      = teams
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name       = try c.decode(String.self, forKey: .name)
        components = try c.decodeIfPresent([String].self, forKey: .components) ?? []
        teams      = try c.decodeIfPresent([String].self, forKey: .teams)      ?? ["any"]
    }

    enum CodingKeys: String, CodingKey {
        case name, components, teams
    }
}

// MARK: - Helpers

extension Issue {
    var priorityColor: String {
        switch priority {
        case "High":   return "red"
        case "Medium": return "orange"
        case "Low":    return "green"
        default:       return "gray"
        }
    }
}
