# idtrack — Project Reference

## What It Is

`idtrack` is a self-contained Go binary that serves a web-based issue tracker over HTTPS. It replaces an earlier Ego-language backend. The binary handles both server duties and all administrative CLI operations (user management, configuration). There is no external dependency besides the SQLite database file.

## Repository Layout

```
idtrack/
├── main.go               # CLI entry point — all verbs live here
├── go.mod                # module: github.com/tucats/idtrack
├── db/
│   ├── db.go             # Open(), schema init, migration helper
│   ├── users.go          # User CRUD + RecordLogin + UpdateUser + ListUsers
│   ├── issues.go         # Issue CRUD (list/get/create/update/delete)
│   ├── comments.go       # Comment CRUD + DeleteComment
│   └── projects.go       # Project/Component CRUD
├── server/
│   └── server.go         # HTTP mux, middleware, all handlers
└── resources/            # Embedded at build time via //go:embed
    ├── idtrack.html
    ├── idtrack.css
    ├── idtrack.js
    ├── https-server.crt  # Self-signed TLS certificate
    └── https-server.key  # TLS private key
```

## Technology Choices

- **Go 1.25**, single binary, no runtime dependencies
- **SQLite** via `modernc.org/sqlite` (pure-Go, no CGO required)
- **HTTPS only** — TLS cert/key embedded in the binary via `embed.FS`
- **Basic Auth** — password is SHA-256 hashed client-side (in JS) before transmission; the hash is stored directly in the DB (no salting — acceptable for an internal tool)
- **No framework** — `net/http` mux with Go 1.22+ path patterns (`GET /api/issues/{id}`)

## Database Schema

```sql
CREATE TABLE users (
    username      TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    password_hash TEXT NOT NULL,   -- SHA-256 hex of the password
    created_at    TEXT NOT NULL,   -- RFC3339 UTC
    -- added via migration:
    last_login_at TEXT NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0   -- 0=false, 1=true
);

CREATE TABLE issues (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    reporter    TEXT NOT NULL,     -- username (login name, not display name)
    assignee    TEXT NOT NULL DEFAULT '',
    priority    TEXT NOT NULL DEFAULT 'Medium',  -- High/Medium/Low
    status      TEXT NOT NULL DEFAULT 'Open',    -- Open/Resolved
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    -- added via migration:
    project     TEXT NOT NULL DEFAULT '',
    component   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id   INTEGER NOT NULL,
    author     TEXT NOT NULL,      -- username
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE projects (
    name TEXT PRIMARY KEY
);

CREATE TABLE components (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL,
    name    TEXT NOT NULL,
    UNIQUE(project, name)
);
```

### Schema Migrations

The schema is created fresh with `CREATE TABLE IF NOT EXISTS`. Columns added after the initial schema (like `last_login_at`, `is_admin`, `project`, `component`) are applied via `addColumnIfMissing()` in `db/db.go`, which runs `ALTER TABLE ... ADD COLUMN` and ignores "duplicate column name" errors. This means the binary upgrades existing databases automatically on startup with no migration tooling needed.

## Runtime Files (`~/.idtrack/`)

All runtime state lives in `~/.idtrack/` (created with mode 0700):

| File             | Contents                                      |
|------------------|-----------------------------------------------|
| `defaults.json`  | `{"port": N, "database": "path"}` — persisted defaults |
| `idtrack.pid`    | PID of the running server process             |
| `idtrack.log`    | Stdout/stderr of the background server        |

## CLI Verbs

### `idtrack default [--port n] [--database path]`
Merges the given values into `~/.idtrack/defaults.json`. Unspecified keys are preserved. Requires at least one flag.

### `idtrack serve [--port n] [--database path]`
- **Does not block the terminal.** Re-execs itself with `--foreground` as a background process using `exec.Command` + `Setsid: true` (new session, survives terminal close).
- Checks for a stale/live PID file before starting; errors if a server is already running.
- Redirects child stdout/stderr to `~/.idtrack/idtrack.log` (append mode).
- Writes child PID to `~/.idtrack/idtrack.pid`.
- Default port: **8443**. Default database: `idtrack.db` in the working directory.
- The `--foreground` flag is **internal** — it tells the re-exec'd child to run the server directly. Do not expose it in docs.

### `idtrack stop`
Reads `~/.idtrack/idtrack.pid`, sends `SIGTERM`, removes the PID file.

### `idtrack user --list [--database path]`
Tabular output: USERNAME, DISPLAY NAME, ADMIN, LAST LOGIN.

### `idtrack user --add username:password [--name text] [--admin true|false] [--database path]`
- Display name defaults to username if `--name` is omitted.
- Admin defaults to false if `--admin` is omitted.
- Uses an upsert (`ON CONFLICT DO UPDATE`) so re-adding a user updates their record.

### `idtrack user --update username [--name text] [--password text] [--admin true|false] [--database path]`
- Only updates fields that are explicitly provided; others are left unchanged.
- Fails with an error if the username does not already exist (cannot create via `--update`).
- `--admin` accepts only `"true"` or `"false"` (validated at parse time).

### `idtrack user --delete username [--database path]`
Hard-deletes the user row. Does not cascade to issues/comments (those store the username as a string).

### `idtrack define --project name [--component name] [--database path]`
- Without `--component`: creates a new project (idempotent — uses `INSERT OR IGNORE`).
- With `--component`: adds a component to an existing project. Errors if the project does not exist. Idempotent (`INSERT OR IGNORE`).

### `idtrack delete --project name [--component name] [--database path]`
- Without `--component`: deletes the project and all its components. Errors (with issue list) if any issues reference that project.
- With `--component`: deletes a single component from a project. Errors (with issue list) if any issues reference that project+component combination.

## HTTP API

All authenticated endpoints use Basic Auth where the password field carries the SHA-256 hex hash (not the plaintext password). The `auth` middleware validates on every request and stores the `*db.User` in the request context.

| Method | Path | Auth | Admin required |
|--------|------|------|----------------|
| POST | `/api/login` | Basic (validates) | no |
| GET | `/api/users` | yes | no |
| GET | `/api/projects` | yes | no |
| GET | `/api/issues` | yes | no |
| POST | `/api/issues` | yes | no |
| GET | `/api/issues/{id}` | yes | no |
| PUT | `/api/issues/{id}` | yes | no |
| DELETE | `/api/issues/{id}` | yes | **yes** |
| POST | `/api/issues/{id}/comments` | yes | no |
| DELETE | `/api/issues/{id}/comments/{cid}` | yes | **yes** |

### Login response
```json
{ "username": "...", "display_name": "...", "is_admin": true|false }
```
`RecordLogin` is called on the `users` table after a successful `/api/login` — not on every authenticated request.

### Issue list query params
`GET /api/issues?status=open|resolved&priority=High|Medium|Low&search=text&sort=col&order=asc|desc`

## Frontend Architecture

Single-page app. All JS is in one `idtrack.js` file; no build step, no framework.

### Key state variables
```js
_credentials  // 'Basic base64(user:sha256hash)' — sent on every API call
_currentUser  // { username, display_name, is_admin }
_userMap      // { username: display_name } — built from /api/users at login
_projectData  // [{name, components: [...]}] — built from /api/projects at login
_allIssues    // full issue list, filtered/sorted client-side
_currentId    // currently selected issue id
```

Session is persisted in `sessionStorage` (key: `idtrack_session`) so a page refresh doesn't require re-login. Dark mode preference is in `localStorage` (key: `idtrack_prefs`).

### Display name resolution
`reporter` and `assignee` in the issues table store the short **username** (login name). Display names are resolved client-side via `_userMap` using the `displayName(username)` helper. This map is populated (along with the assignee dropdowns) by `populateAssigneeDropdowns()` which calls `GET /api/users`. If a username isn't in the map, it falls back to the raw username.

### Project/Component UI
- Issues table shows **Project** and **Component** columns (reporter column removed from table; reporter remains visible as read-only in the issue detail panel).
- Sorting by project and component is supported in both the table headers and client-side sort.
- **New Issue** form: a "Project" dropdown must be selected first; selecting it enables a cascaded "Component" dropdown. Both are required — the form will not submit without a valid project and component.
- **Issue Detail** panel: Project and Component are editable `<select>` elements. Changing the Project resets the Component to "Choose…" and refills the component dropdown. Both are required to save.
- `populateProjectDropdowns()` fills both `ni-project` and `detail-project` from `_projectData`.
- `populateComponentDropdown(selectId, projectName, selected)` cascades from a selected project.

### Admin UI
- **Delete Issue** button appears in the detail panel header only when `_currentUser.is_admin` is true. Requires a `confirm()` dialog before calling `DELETE /api/issues/{id}`.
- **Trash icon** (🗑) appears on each comment only for admins. Requires a `confirm()` dialog before calling `DELETE /api/issues/{id}/comments/{cid}`.
- Non-admin users never see these controls. The server also enforces admin on both delete endpoints (returns 403 Forbidden), so the restriction is not purely UI-side.

## Important Implementation Decisions

**Password hashing is client-side.** The JS SHA-256 hashes the password before it leaves the browser. The hash is what's transmitted over Basic Auth and what's stored in the database. This means the server never sees the plaintext password. The tradeoff is that the hash effectively *is* the password credential — but for an internal tool over a private network this is acceptable.

**SQLite with `MaxOpenConns(1)`.** SQLite doesn't support concurrent writers. Setting max open connections to 1 serializes all access and avoids `SQLITE_BUSY` errors.

**No comment–issue foreign key constraint in SQLite.** SQLite doesn't enforce foreign keys by default (requires `PRAGMA foreign_keys = ON`). The code instead manually deletes associated comments before deleting an issue in `db.DeleteIssue()`. The same manual cleanup is not needed for `DeleteUser` — orphaned reporter/assignee strings are acceptable.

**`serve` re-execs itself rather than forking.** Go doesn't have a clean `fork()` equivalent. The approach is: parent validates args, spawns `exec.Command(os.Executable(), "serve", "--foreground", ...)`, writes the child PID, exits. The child runs the actual blocking server. `Setsid: true` detaches the child from the parent's process group so it survives terminal close.

**`UpdateUser` requires `*bool` for `isAdmin`.** An empty string signals "not specified" for string fields, but a `bool` has no natural sentinel. Using `*bool` (nil = leave unchanged, non-nil = set) keeps the logic explicit and avoids accidentally clearing admin status when only updating a display name.

**Schema migrations are additive only.** New columns are always added with `DEFAULT` values via `addColumnIfMissing`. Existing data is never altered. This keeps the migration path trivially safe.

**Static assets are embedded.** The TLS cert/key and all web assets are compiled into the binary with `//go:embed resources`. Deployment is a single file copy.
