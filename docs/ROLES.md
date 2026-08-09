# Team-Based Access Control — Implementation Plan

## Overview

This document describes the design and phased implementation plan for adding
team-based role segmentation to idtrack.  The goal is to allow issues and
projects to be partitioned by team so that, for example, members of the
"platform" team only see projects and issues owned by that team, while members
of the "database" team see a separate slice of the data.

Two reserved team names have fixed semantics regardless of how they appear in
the data:

| Reserved name | Meaning as a **user** team | Meaning as a **project / issue** team |
| --- | --- | --- |
| `admin` | Full superuser access — sees and can edit everything | (not used on objects; admin users are granted by `admin` team membership) |
| `any` | Matches every project/issue regardless of team | Visible to every user regardless of their team |

All team-name comparisons are case-insensitive.  The canonical stored form is
lower-case.

---

## Data Model Changes

### New table: `teams`

A canonical registry of valid team names.  Admin UI and the CLI may add or
remove entries.  The two reserved names are seeded at startup and may never
be deleted.

```sql
CREATE TABLE IF NOT EXISTS teams (
    name        TEXT PRIMARY KEY,  -- lower-case canonical name
    description TEXT NOT NULL DEFAULT ''
);
```

Seed rows: `('admin', '')`, `('any', '')`.  The description is free-form text;
it is not used in access-control logic but is returned by the API and displayed
in the team-management UI for documentation purposes.

### New column: `users.teams`

A comma-separated list of team names this user belongs to (e.g.
`"platform,database"`).  Stored lower-case.  Padded at query time with
leading/trailing commas (`",platform,database,"`) so safe LIKE patterns can
find a whole word without false substring matches.

```sql
ALTER TABLE users ADD COLUMN teams TEXT NOT NULL DEFAULT 'any';
```

### Retirement of `users.is_admin`

`is_admin` is replaced by the presence of `"admin"` in `users.teams`.  The
column is kept in the schema for backward compatibility (it is never removed)
but is **no longer written**.  All code that checks `is_admin` is changed to
check whether `"admin"` is in the user's teams list.  `db.User.IsAdmin` becomes
a computed property (Go) / `var` (Swift) derived from `Teams`.  The `CountAdmins`
query changes to:

```sql
SELECT COUNT(*) FROM users
WHERE ',' || lower(teams) || ',' LIKE '%,admin,%'
```

### New column: `projects.teams`

Teams that can access this project.  Same padded-CSV format.

```sql
ALTER TABLE projects ADD COLUMN teams TEXT NOT NULL DEFAULT 'any';
```

### New column: `issues.teams`

Teams that can see and edit this issue.  Same format.  Defaults to `"any"` so
all existing issues remain universally visible after migration.

```sql
ALTER TABLE issues ADD COLUMN teams TEXT NOT NULL DEFAULT 'any';
```

---

## Access-Control Rules

A user **can see** a project or issue if **any** of the following are true:

1. The user has `admin` in their teams, **or**
2. The user has `any` in their teams (legacy "match all" users), **or**
3. The object's teams column is `any` (or contains `any`), **or**
4. The intersection of the user's teams and the object's teams is non-empty.

A user **can edit** an issue under the same team-match rules, plus the
existing reporter/assignee/admin restriction is unchanged.

A user **can create** an issue only for projects they can see (same rule as
above applied to the project).

Admin-only operations (delete issue, manage users, manage projects, manage
teams) continue to require the user to have `admin` in their teams.

---

## Migration Logic

Runs automatically inside `initSchema` on first startup with a new binary:

1. Add `teams` table (idempotent `CREATE TABLE IF NOT EXISTS`).
2. Seed `admin` and `any` rows (`INSERT OR IGNORE`).
3. `addColumnIfMissing` for `users.teams`, `projects.teams`, `issues.teams`.
4. **User backfill** (guarded by `WHERE teams = ''` to be a no-op on reruns):
   ```sql
   UPDATE users SET teams = 'admin' WHERE is_admin = 1 AND teams = '';
   UPDATE users SET teams = 'any'   WHERE is_admin = 0 AND teams = '';
   ```
5. **Project backfill** (no-op guard `WHERE teams = ''`):
   ```sql
   UPDATE projects SET teams = 'any' WHERE teams = '';
   ```
6. **Issue backfill** (no-op guard `WHERE teams = ''`):
   ```sql
   UPDATE issues SET teams = 'any' WHERE teams = '';
   ```

---

## Phase 1 — Server-side Go + Web App

### 1.1  `db` package

#### `db/teams.go` (new file)

A `Team` struct carries both fields returned by the API:

```go
type Team struct {
    Name        string `json:"name"`
    Description string `json:"description"`
}
```

Functions:

```
ListTeams(db) ([]Team, error)
CreateTeam(db, name, description string) error     -- error if name is reserved
DeleteTeam(db, name string) error                  -- error if reserved, or if any
                                                   -- user / project / issue still
                                                   -- carries this team name
UpdateTeam(db, name, newName, description string) error
    -- renames when newName != "" and != name; updates description when non-empty;
    -- cascades name change to users.teams, projects.teams, issues.teams in one transaction;
    -- error if name is reserved (admin / any) and newName differs from name
```

`DeleteTeam` checks three tables before deleting:
```sql
SELECT COUNT(*) FROM users    WHERE ',' || lower(teams) || ',' LIKE '%,<name>,%'
SELECT COUNT(*) FROM projects WHERE ',' || lower(teams) || ',' LIKE '%,<name>,%'
SELECT COUNT(*) FROM issues   WHERE ',' || lower(teams) || ',' LIKE '%,<name>,%'
```
If any count is non-zero it returns an error listing how many rows in each
table reference the team, so the admin knows what to reassign first.

`UpdateTeam` deliberately handles both rename and description edit in a single
function so the handler can accept a partial body (only one field provided) and
still make one DB round-trip.  Either field may be left empty to signal "no
change".

#### `db/users.go`

- Add `Teams []string` to `User` struct.
- Remove `IsAdmin bool` as a stored field; replace with `func (u *User) IsAdmin() bool { return containsTeam(u.Teams, "admin") }`.
  - **Note**: This changes the field to a method, requiring updates everywhere `u.IsAdmin` is used.  If the churn is too disruptive, keep `IsAdmin bool` as a computed field that is populated by `FindUser` / `ListUsers` from the teams column instead.  The plan records both options; the simpler one (keep the field, populate from teams) is preferred to minimise the diff.
- Update `FindUser`, `ListUsers`, `AddUser`, `UpdateUser` to read/write the `teams` column.
- `CountAdmins`: rewrite to query `teams` column.
- `AddUser` / `UpdateUser`: accept `teams []string`; the CLI `--admin true` flag becomes `--teams admin` (or both flags may coexist during transition).

#### `db/projects.go`

- Add `Teams []string` to `Project` struct.
- Update `ListProjects`, `CreateProject`, `DeleteProject` to handle `teams`.
- New: `SetProjectTeams(db, project string, teams []string) error`.

#### `db/issues.go`

- Add `Teams []string` to `Issue` struct.
- Add `teams` to `issueColumns` and `scanIssue`.
- `UpdateIssue`: add `teams []string` parameter.
- `buildWhereClause`: add `userTeams []string` parameter.  Generate a WHERE
  fragment that matches issues using the padded-LIKE pattern:
  ```go
  // "any" in user teams or issue teams → no restriction needed
  // otherwise: (',platform,' LIKE '%,' || team || ',%') for each user team
  ```
  Because SQLite cannot use column indexes for LIKE wildcards on both ends,
  this may degrade on very large datasets; for idtrack's target scale it is
  acceptable.  An alternative is to pull all non-`any` issues and filter in
  Go; document that trade-off in a comment.

### 1.2  `server` package

#### `server/teams.go` (new file)

```
handleListTeams   GET    /api/teams          auth, any user
handleCreateTeam  POST   /api/teams          auth + admin
handleDeleteTeam  DELETE /api/teams/{name}   auth + admin
handleUpdateTeam  PUT    /api/teams/{name}   auth + admin
```

`handleCreateTeam` request body: `{ "name": "platform", "description": "..." }`.
`description` is optional; omitting it stores an empty string.

`handleUpdateTeam` request body: `{ "name": "infra", "description": "..." }`.
Either field may be omitted; only supplied fields are changed.  Attempting to
rename a reserved team (`admin`, `any`) returns 400.  Updating the description
of a reserved team is allowed.

`handleDeleteTeam` returns 409 Conflict when `db.DeleteTeam` reports the team
is still in use, with a body that includes the counts per table so the client
can display a helpful message (e.g. "Cannot delete: used by 3 users, 1 project,
12 issues").

`handleListTeams` response: `{ "teams": [ { "name": "...", "description": "..." }, ... ] }`.

#### `server/projects.go`

- `handleListProjects`: filter by calling user's teams before returning.
- `handleCreateProject`: accept `teams []string` in body.
- New: `handleUpdateProjectTeams` — `PUT /api/projects/{project}/teams`
  (admin only).

#### `server/users.go`

- `handleCreateUser`, `handleUpdateUser`: accept `teams []string` in body.
- Remove `is_admin` from request body; derive admin from `teams` content.
  To ease client migration, keep accepting `is_admin: true` as an alias that
  automatically adds `"admin"` to the teams list if absent.
- `handleListUsers`: return `teams` field in each user object.

#### `server/issues.go`

- `handleListIssues`: extract calling user's teams and pass to
  `db.ListIssues` / `db.CountIssues`.
- `handleCreateIssue`: validate that the chosen project is visible to the
  calling user.
- `handleUpdateIssue`: pass `teams` from request body to `db.UpdateIssue`.
  Only admins may change the teams list; non-admins get 403 if `teams`
  differs from the current value.

#### `server/server.go`

Add route registrations:

```
GET    /api/teams                   handleListTeams
POST   /api/teams                   handleCreateTeam
DELETE /api/teams/{name}            handleDeleteTeam
PUT    /api/teams/{name}            handleRenameTeam
PUT    /api/projects/{project}/teams  handleUpdateProjectTeams
```

#### `server/middleware.go`

`currentUser` already stores `*db.User` in context.  The `IsAdmin` helper
used throughout becomes `currentUser(r).IsAdmin` → `currentUser(r).IsAdmin()`
if we switch to a method; or remains `.IsAdmin` if we keep it as a populated
field.

### 1.3  CLI (`commands` package)

- `commands/users.go`:
  - `idtrack user add username:password [--teams t1,t2] [--admin true|false]`
    (`--admin true` is a shortcut for `--teams admin`; both flags may be
    present; they are merged before saving).
  - `idtrack user update username [--teams t1,t2] [--admin true|false]`
  - `idtrack user list`: add TEAMS column to output.
- `commands/projects.go`:
  - `idtrack define project name [--teams t1,t2]`
- New: `commands/teams.go`
  - `idtrack teams list` — two-column table: NAME, DESCRIPTION
  - `idtrack teams add name [--description text]`
  - `idtrack teams delete name`
  - `idtrack teams update name [--name newname] [--description text]`
    (rename and/or description edit; at least one flag required)
- `main.go`: add dispatch for `"teams"` verb.

### 1.4  Web App (`resources/idtrack.js` + `idtrack.css`)

#### Team management overlay (admin only)

- Hamburger menu: add **Edit Teams…** between **Edit Users…** and **Edit Projects…**.
- New overlay `#manage-teams-overlay` following the same parent–overlay pattern
  as manage-users.  The overlay has two screens (same ep-list / ep-detail
  pattern as Edit Projects):
  - **List screen** (`#mt-list-overlay`): table with NAME and DESCRIPTION
    columns; lock icon on `admin` and `any` rows.  **+ New Team** button opens
    the detail screen in create mode.  Clicking an existing team opens detail
    in edit mode.
  - **Detail screen** (`#mt-detail-overlay`): two fields — **Name** (text
    input, read-only for reserved teams) and **Description** (textarea,
    editable for all teams including reserved).  **Save** calls `POST
    /api/teams` (create) or `PUT /api/teams/{name}` (update).  **Delete**
    button (hidden for reserved teams) with `confirm()` guard calls `DELETE
    /api/teams/{name}`.  Closing always re-opens the list screen.
- API calls: `GET /api/teams`, `POST /api/teams`, `DELETE /api/teams/{name}`,
  `PUT /api/teams/{name}`.

#### User edit (manage-users overlay)

- Add a **Teams** chip-input field to both the add-user and edit-user forms.
  Chips are drawn from `GET /api/teams`.  Minimum one chip required.
- Remove the **Admin** checkbox; having `admin` as a chip replaces it.
  For backward compatibility, the server still accepts `is_admin: true` so
  older clients continue to work.

#### Project edit (ep-detail-overlay)

- Add a **Teams** chip-input field.  Default chip: `any`.
- `POST /api/projects` and `PUT /api/projects/{project}/teams` updated.

#### Issue create / detail forms

- Add a read-only **Teams** display in the detail panel for all users.
- Admins see an editable chip-input allowing them to change the issue's teams.
- `populateProjectDropdowns()` is updated to only offer projects visible to
  `_currentUser` (server already filters `GET /api/projects`; client just uses
  the returned list).
- `GET /api/issues` continues to use server-side filtering; client changes
  are minimal.

#### `_teamData` global state

```js
_teamData   // { name: string, description: string }[] — from /api/teams
```

Populated at login alongside `_projectData`.  The `name` field fills chip
pickers; `description` is shown in the manage-teams detail screen.  A helper
`teamNames()` returns just the names array for use in chip pickers elsewhere.

#### `GET /api/status` extension (optional)

If desired, `status` can return `teams_enabled: true` so the client can
gracefully degrade against older server versions.  Mark as optional.

### 1.5  Tests

- `db/db_test.go`: add test cases for `CreateTeam`, `DeleteTeam`,
  team-filtered `ListIssues`, and migration backfill (verify that an old-style
  DB with `is_admin=1` produces a user with `teams="admin"` after `Open`).
- `server` package integration tests (if any exist or are added): cover
  team-filtered project and issue responses.

---

## Phase 2 — iOS App

### 2.1  `Models.swift`

- `User`: add `var teams: [String]`.  `isAdmin` becomes computed:
  ```swift
  var isAdmin: Bool { teams.contains("admin") }
  ```
  Update `CodingKeys` and `init(from decoder:)` for `teams` (decodes
  `[String]`, defaults to `["any"]` if absent).
- `Project`: add `var teams: [String]`.
- `Issue`: add `var teams: [String]`.

### 2.2  `APIClient.swift`

New methods:

```swift
func fetchTeams() async throws -> [Team]
func createTeam(_ name: String, description: String) async throws
func deleteTeam(_ name: String) async throws
func updateTeam(_ name: String, newName: String?, description: String?) async throws
func updateProjectTeams(_ project: String, teams: [String]) async throws
```

Update `createUser`, `updateUser` to include `teams`.
Update `createIssue`, `updateIssue` to include `teams`.

### 2.3  `Models.swift`

Add a `Team` struct (mirrors the Go struct):

```swift
struct Team: Identifiable, Codable, Equatable {
    var id: String { name }
    let name: String
    var description: String

    enum CodingKeys: String, CodingKey {
        case name, description
    }
}
```

### 2.4  `AppState.swift`

- Add `@Published var availableTeams: [Team] = []`.
- Populate in `loadInitialData()` alongside `projects`.
- Convenience accessor: `var teamNames: [String] { availableTeams.map(\.name) }`.

### 2.5  `ManageUsersView.swift`

- Replace **Admin** toggle with a **Teams** multi-select / chip-picker.
- Chips sourced from `appState.availableTeams`.
- Lock `admin` chip: shows as always-present for admin users; removing it is
  blocked with an alert identical to the web app's last-admin guard.

### 2.6  `EditProjectsView.swift`

- Add a **Teams** multi-select row to the project detail screen.
- On save: call `updateProjectTeams`.

### 2.7  New `ManageTeamsView.swift`

Mirrors the web app's two-screen manage-teams overlay using SwiftUI
`NavigationStack`:

- **Team list screen**: `List` of `Team` objects showing name and a one-line
  description preview; lock icon (SF Symbol `lock.fill`) on `admin` and `any`.
  Swipe-to-delete enabled for non-reserved teams (with confirmation alert).
  **+ New Team** toolbar button pushes the detail screen in create mode.
  Tapping a row pushes the detail screen in edit mode.
- **Team detail screen**: `TextField` for **Name** (disabled for reserved
  teams) and `TextEditor` / `TextField` for **Description** (editable for all
  teams including reserved).  **Save** calls `createTeam` or `updateTeam`.
  **Delete** button (hidden for reserved teams) with confirmation alert calls
  `deleteTeam` then pops the screen.
- Accessible from the admin section of `SettingsView` or from a dedicated
  admin action sheet.

### 2.8  `NewIssueView.swift`

- Project picker is already populated from `appState.projects`.  After the
  server filters `GET /api/projects` by the user's teams, no client change is
  strictly required.  Verify the picker correctly shows only the filtered list.

### 2.9  `IssueDetailView.swift`

- Add a **Teams** row (read-only for non-admins, editable chip-picker for
  admins).

### 2.10  `MainAppView.swift` / navigation

- Add **Manage Teams** entry in the admin action sheet / settings navigation,
  pointing to `ManageTeamsView`.

---

## API Changes Summary

| Method | Path | Change |
|--------|------|--------|
| GET | `/api/teams` | **new** — list teams with name + description |
| POST | `/api/teams` | **new** — create team; body: `{ name, description }` (admin) |
| DELETE | `/api/teams/{name}` | **new** — delete team (admin); 400 if reserved name; 409 if team is still referenced by any user, project, or issue |
| PUT | `/api/teams/{name}` | **new** — rename and/or update description; body: `{ name?, description? }`; rename blocked for reserved names; description edit allowed for all (admin) |
| PUT | `/api/projects/{project}/teams` | **new** — set project teams (admin) |
| GET | `/api/projects` | changed — filtered by calling user's teams |
| POST | `/api/projects` | changed — body now accepts `teams` |
| POST | `/api/users` | changed — body now accepts `teams`; `is_admin` still accepted as alias |
| PUT | `/api/users/{username}` | changed — body now accepts `teams` |
| GET | `/api/users` | changed — response includes `teams` field |
| GET | `/api/issues` | changed — server filters by user teams |
| POST | `/api/issues` | changed — body now accepts `teams`; project validated against user teams |
| PUT | `/api/issues/{id}` | changed — body now accepts `teams` (admin-only change) |

---

## Backward Compatibility Notes

- Existing sessions remain valid after upgrade; the `is_admin` cookie-carried
  user struct is re-read from the DB on each request so the teams migration is
  immediately effective.
- Old web-app clients that send `is_admin: true` in user create/update bodies
  continue to work; the server maps it to `teams: ["admin"]`.
- Old iOS clients see the same project and issue lists they always did (because
  all migrated data defaults to `teams = "any"`), but they cannot set teams or
  see the teams field.  They will continue to function until updated.
- The `is_admin` column is kept in the DB and in `db.User` as a populated
  (not computed) field derived from teams at load time, to avoid a cascade of
  type-system changes across the codebase.  It should be removed in a future
  cleanup once all clients are updated.

---

## Design Decisions (resolved)

1. **Project filtering**: The project list returned by `GET /api/projects` is
   silently filtered to only the projects visible to the calling user given
   their team membership.  Inaccessible projects are not shown greyed-out;
   they are simply absent.  This applies to every consumer: the web-app
   dropdowns, the iOS pickers, and the issue-create validation.

2. **Issue teams take precedence over project teams**: If a project is visible
   to "platform" but a specific issue within it carries `teams = "database"`,
   a user whose only team is "platform" cannot see that issue.  The per-issue
   team override is the mechanism for fine-grained control within a project.

3. **Rename cascades; delete is blocked when the team is in use**: When a team
   is renamed, `UpdateTeam` cascades the new name to every row in
   `users.teams`, `projects.teams`, and `issues.teams` in a single
   transaction.  Deleting a team is blocked (HTTP 409 / CLI error) when any
   user, project, or issue still carries that team name.  The error message
   lists what is using the team so the admin knows what to reassign first.
   Reserved names (`admin`, `any`) cannot be renamed or deleted regardless.

4. **`--admin` flag is kept as a convenience alias**: `--admin true` is
   equivalent to adding `admin` to the user's teams list; `--admin false`
   removes it.  Both `--admin` and `--teams` may be supplied simultaneously;
   they are merged before saving.  There is no current plan to remove the flag.

5. **Teams are loaded separately at login**: `GET /api/teams` is called once
   at login alongside `GET /api/projects` and `GET /api/users`.  The
   `GET /api/status` response is not changed.  This keeps the status probe
   lightweight and the team data cleanly scoped to authenticated sessions.
