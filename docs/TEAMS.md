# Teams — Administrator Guide

Teams partition idtrack's data so that different groups of users see only the
projects and issues relevant to them. A member of the **platform** team sees
platform projects and issues; a member of the **database** team sees a separate
slice. Users and objects can belong to multiple teams, and two reserved team
names provide escape hatches for super-users and universally-visible data.

---

## Table of Contents

1. [Concepts](#1-concepts)
2. [Reserved Teams](#2-reserved-teams)
3. [Access-Control Rules](#3-access-control-rules)
4. [Managing Teams — CLI](#4-managing-teams--cli)
5. [Managing Teams — Web UI](#5-managing-teams--web-ui)
6. [Assigning Teams to Users](#6-assigning-teams-to-users)
7. [Assigning Teams to Projects](#7-assigning-teams-to-projects)
8. [Assigning Teams to Issues](#8-assigning-teams-to-issues)
9. [Upgrading an Existing Installation](#9-upgrading-an-existing-installation)
10. [Common Patterns](#10-common-patterns)

---

## 1. Concepts

Every user, project, and issue carries a **teams** list — a set of team names
that governs who can see it. The server evaluates team membership on every
request; no data is ever returned to a user whose teams do not overlap with the
object's teams.

Team names are **case-insensitive** and stored in lower-case. The names
`admin` and `any` are reserved (see §2).

### What teams control

| Object | Effect of team assignment |
| --- | --- |
| **User** | Which projects and issues the user can see and interact with |
| **Project** | Which users can see the project in dropdowns and create issues in it |
| **Issue** | Which users can see and edit the issue, regardless of its project's team |

A user must be able to see a **project** in order to create an issue in it. If
the server filters a project out of `GET /api/projects`, it will not appear in
any New Issue or Edit Issue dropdown.

An issue's team can be narrower than its project's team. For example, a project
visible to the `platform` and `database` teams can contain an issue that is
visible only to `platform`. Users on the `database` team will not see that
issue even though they can see the project.

---

## 2. Reserved Teams

Two team names have fixed meanings regardless of any database rows:

| Name | As a **user** team | As a **project or issue** team |
| --- | --- | --- |
| `admin` | Full superuser access — sees and can modify everything | Not used on objects |
| `any` | Matches every project and issue, regardless of their team | Visible to every user, regardless of their team |

**`admin`** — a user who has `admin` in their teams list is a super-user.
They see all projects and issues, may create and delete users, manage teams and
projects, and delete any issue or comment. Admin status is now derived entirely
from team membership; the legacy `is_admin` database column is kept for
backward compatibility but is no longer written.

**`any`** — both sides of the "any" escape hatch work independently:

- A **user** whose teams include `any` sees every project and issue,
  regardless of what teams those objects carry. This is the default for
  newly created non-admin users and for all data migrated from pre-teams
  versions of idtrack.
- A **project** or **issue** whose teams include `any` is visible to every
  authenticated user, regardless of what teams that user carries.

> **Neither `admin` nor `any` can be renamed or deleted.** Their descriptions
> may be edited for documentation purposes (for example, to explain their role
> to other administrators).

---

## 3. Access-Control Rules

A user **can see** a project or issue when **any** of the following is true:

1. The user has `admin` in their teams, **or**
2. The user has `any` in their teams, **or**
3. The object's teams list includes `any`, **or**
4. The intersection of the user's teams and the object's teams is non-empty.

The same rules apply to **editing** an issue, combined with the existing
reporter/assignee/admin restriction (only the reporter, assignee, or an admin
may edit or delete an issue).

A user **can create** an issue only for projects they can see under the same
rules applied to the project.

---

## 4. Managing Teams — CLI

The `idtrack teams` command manages the team registry. All subcommands accept
an optional `--database PATH` flag; if omitted, the configured default database
is used.

### List all teams

```sh
idtrack teams list
```

Prints a two-column table of NAME and DESCRIPTION for every team including the
two reserved teams.

### Create a team

```sh
idtrack teams add <name> [--description "text"]
```

The name is stored in lower-case. Reserved names (`admin`, `any`) cannot be
created because they already exist. `--description` is optional; it is free-form
text displayed in the UI and has no effect on access control.

```sh
idtrack teams add platform --description "Platform infrastructure team"
idtrack teams add database --description "Database and storage team"
idtrack teams add frontend
```

### Delete a team

```sh
idtrack teams delete <name>
```

Deletion is blocked when any user, project, or issue still references the team.
The error message lists how many rows in each table reference it so you know
what to reassign first. Reserved teams cannot be deleted.

```sh
idtrack teams delete frontend
# error if issues or users still reference "frontend"
```

### Rename a team or update its description

```sh
idtrack teams update <name> [--name <new-name>] [--description "text"]
```

At least one of `--name` or `--description` is required. Renaming cascades to
every user, project, and issue that references the team in a single database
transaction. Reserved teams cannot be renamed; their descriptions may be updated.

```sh
# Rename
idtrack teams update frontend --name web

# Update description only
idtrack teams update platform --description "Platform + SRE team"

# Rename and update description in one step
idtrack teams update web --name frontend --description "Frontend engineering"
```

---

## 5. Managing Teams — Web UI

Admin users see an **Edit Teams…** entry in the hamburger (⋯) menu in the top
toolbar.

### Team list screen

Opens a list of all teams. Each row shows the team name, a lock icon for
reserved teams, and the description (if set). Tapping a row opens the **Team
detail screen** in edit mode. The **+** button in the top-right corner opens
the detail screen in create mode.

### Team detail screen

| Field | Editable | Notes |
| --- | --- | --- |
| **Name** | Yes (non-reserved only) | Stored lower-case; rename cascades everywhere |
| **Description** | Yes (all teams) | Free-form; no effect on access control |

Tapping **Save** creates or updates the team. A **Delete Team** button appears
for non-reserved teams; it is blocked with an error if the team is still
referenced anywhere.

---

## 6. Assigning Teams to Users

A user's team list determines which projects and issues they can see. Every
user must belong to at least one team.

### CLI User Team Assignments

When adding or updating a user, use `--teams` to set the full team list as a
comma-separated string. The legacy `--admin` flag is still accepted and merges
with `--teams`.

```sh
# Create a user belonging to the platform team
idtrack user add alice:s3cr3t --name "Alice" --teams platform

# Create an admin user
idtrack user add bob:s3cr3t --name "Bob" --teams admin

# Create a user on multiple teams
idtrack user add carol:s3cr3t --name "Carol" --teams platform,database

# Promote an existing user to admin (--admin true adds "admin" to their teams)
idtrack user update alice --admin true

# Change Carol's teams entirely
idtrack user update carol --teams database

# Add the "any" catch-all (sees everything)
idtrack user update guest --teams any
```

The `list` subcommand shows the TEAMS column:

```sh
idtrack user list
```

### Web UI User Team ASsignments

In the **Edit Users…** overlay (admin menu → **Edit Users…**), each user's
row shows their team badges. Tapping a user row opens the edit form.

The **Teams** field displays the currently assigned teams and opens a **Select
Teams** sheet when tapped. Select or deselect teams from the list. At least one
team must remain selected.

The legacy **Admin** checkbox has been replaced by the `admin` team chip. To
grant admin privileges, add `admin` to the user's teams. To revoke them, remove
`admin` from their teams (you cannot remove admin from the last remaining admin
user — the server blocks this with an error).

---

## 7. Assigning Teams to Projects

A project's team list controls which users can see it in dropdowns and create
issues in it.

### CLI Project Team Assignments

Pass `--teams` to `idtrack define project` when creating the project:

```sh
# Visible only to the platform team
idtrack define project "Infrastructure" --teams platform

# Visible to platform and database teams
idtrack define project "Shared Services" --teams platform,database

# Visible to everyone (default)
idtrack define project "General" --teams any
```

> **Note:** There is currently no CLI command to change the teams of an existing
> project. Use the web UI to update an existing project's teams.

### Web UI Project Team Assignments

In the **Edit Projects…** overlay (admin menu → **Edit Projects…**), each
project row shows team badges. Tapping a project row opens the detail screen.

The **Teams** field shows the project's current teams and opens the team picker
when tapped. After changing the teams, tap the **Save Teams** button that appears
in the Teams section to commit the change. Other changes to the project
(components) take effect immediately without a separate save.

---

## 8. Assigning Teams to Issues

An issue's teams list provides fine-grained control within a project. An issue
can be restricted to a subset of the project's teams, or opened to teams that
can't see the project's other issues.

> **Most deployments do not need per-issue teams.** If all issues in a project
> should be visible to the same people who can see the project, leave the issue's
> teams as the default (`any`). Per-issue teams are for edge cases — a
> confidential security report inside a shared project, for example.

### Web UI (admin only)

In the **Issue Detail** panel, a **Teams** row appears below the Project and
Component fields.

- **Non-admin users** see a read-only display of the issue's current teams.
- **Admin users** see an editable teams picker (same chip-picker style as the
  user and project forms). Changing the teams marks the form as dirty; tap
  **Save** to commit.

### API

Admin clients may include a `teams` array in the `PUT /api/issues/{id}` request
body. Non-admin users receive HTTP 403 if they attempt to change the teams list.
The server silently ignores a `teams` value that is identical to the current
value regardless of who sends it.

---

## 9. Upgrading an Existing Installation

When a new binary with teams support starts against an older database, it
performs all schema changes automatically:

1. Creates the `teams` table and seeds the `admin` and `any` reserved rows.
2. Adds `teams` columns to `users`, `projects`, and `issues`.
3. **User backfill** — existing admin users (`is_admin = 1`) get `teams = 'admin'`;
   all other users get `teams = 'any'`.
4. **Project backfill** — every existing project gets `teams = 'any'` (visible
   to all users).
5. **Issue backfill** — every existing issue gets `teams = 'any'` (visible to
   all users).

The result is that **all existing data remains fully visible to all users** after
the upgrade. No manual reassignment is required unless you want to start
restricting access. The backfill is guarded by a `WHERE teams = ''` clause so
subsequent restarts are no-ops.

> **`is_admin` column**: The column is kept in the schema for backward
> compatibility and continues to be read by old clients. The new binary
> derives admin status from `users.teams` and no longer writes to `is_admin`.
> The column will be removed in a future cleanup release.

---

## 10. Common Patterns

### Fully open (single-team organization)

Leave everything at the default. All users get `any` in their teams, all
projects get `any`, all issues get `any`. Everyone sees everything. This is
identical to how idtrack worked before teams were introduced.

### Simple two-team split

```sh
# Create the teams
idtrack teams add engineering --description "Engineering"
idtrack teams add support     --description "Customer Support"

# Assign users
idtrack user update alice  --teams engineering
idtrack user update bob    --teams support
idtrack user update carol  --teams engineering,support   # works both queues

# Assign projects
idtrack define project "Product Issues"  --teams engineering
idtrack define project "Support Tickets" --teams support
```

Alice sees only Product Issues; Bob sees only Support Tickets; Carol sees both.
The admin user (whoever has `admin` in their teams) sees everything.

### Shared project with team-scoped issues

A bug-tracking project is visible to all teams, but security vulnerability
reports are restricted to the `security` team:

1. Create the `security` team and assign it to relevant users.
2. Create the project with `--teams any` (or set via the web UI).
3. When filing a security-sensitive issue, an admin changes its teams to
   `security` in the Issue Detail panel. The issue then disappears from the
   lists of users who are not on the `security` team, even though they can
   see the project.

### Contractors with limited visibility

```sh
# Contractor sees only issues tagged "external"
idtrack teams add external --description "External contractors"
idtrack user update contractor1 --teams external

# Tag the issues/projects the contractor should see
# (done via the web UI or API — set teams to "external" or "external,engineering")
```

### Granting temporary admin access

```sh
# Add admin to an existing user's teams without touching their other memberships
idtrack user update carol --admin true
# carol now has: engineering,admin (her existing teams plus admin)

# Revoke admin later — use --teams to set the complete list
idtrack user update carol --teams engineering
```

> **`--admin true` adds `admin` to the user's existing teams.**
> **`--admin false` with no `--teams` sets teams to `any`.**
> To set the teams list precisely, always use `--teams`.

---

*For the full API reference and server configuration options, see the
[idtrack User Manual](../resources/MANUAL.md).*
