# idtrack — Project Reference

## What It Is

`idtrack` is a self-contained Go binary that serves a web-based issue tracker over HTTPS. It replaces an earlier Ego-language backend. The binary handles both server duties and all administrative CLI operations (user management, configuration). There is no external dependency besides the SQLite database file.

## Repository Layout

```text
idtrack/
├── main.go               # CLI entry point — all verbs live here
├── go.mod                # module: github.com/tucats/idtrack
├── build                 # Build script (see Versioning section)
├── tools/
│   └── buildvers.txt     # Current version string, e.g. "1.0-8"
├── db/
│   ├── db.go             # Open(), schema init, migration helper
│   ├── users.go          # User CRUD + RecordLogin + UpdateUser + ListUsers
│   ├── issues.go         # Issue CRUD (list/get/create/update/delete)
│   ├── comments.go       # Comment CRUD + DeleteComment
│   └── projects.go       # Project/Component CRUD
├── server/
│   ├── server.go         # srv struct + Start() — route wiring and TLS setup
│   ├── middleware.go     # contextKey, auth(), currentUser()
│   ├── helpers.go        # issueID(), jsonResponse(), jsonError()
│   ├── static.go         # static file handlers + handleManual()
│   ├── auth_handlers.go  # handleVersion, handleStatus, handleOnboarding, handleLogin
│   ├── users.go          # user CRUD handlers
│   ├── projects.go       # project/component CRUD handlers
│   ├── issues.go         # issue CRUD handlers
│   └── comments.go       # handleCreateComment, handleDeleteComment
└── resources/            # Embedded at build time via //go:embed
    ├── idtrack.html
    ├── idtrack.css
    ├── idtrack.js
    ├── MANUAL.md         # User manual (rendered via /manual as HTML)
    ├── https-server.crt  # Self-signed TLS certificate
    └── https-server.key  # TLS private key
```

## Versioning

The binary version is injected at link time via the `build` script (never hardcoded in source):

```bash
./build            # normal build, version from tools/buildvers.txt
./build -i         # increment build number, then build
./build --all      # cross-compile for all platforms into builds/
./build --bin      # copy binary to ~/bin after build
```

`tools/buildvers.txt` holds the current version string (format: `MAJOR.MINOR-BUILD`, e.g. `1.0-8`). The `-i` flag increments the `BUILD` part and writes it back.

Two linker variables are injected:

- `main.BuildVersion` — the version string from `tools/buildvers.txt`
- `main.BuildTime` — UTC timestamp (`YYYYMMDDHHmmSS`) of the build

Both default to `"dev"` / `""` when built with plain `go build` (no flags).

## Technology Choices

- **Go 1.25**, single binary, no runtime dependencies
- **SQLite** via `modernc.org/sqlite` (pure-Go, no CGO required)
- **HTTPS only** — TLS cert/key embedded in the binary via `embed.FS`
- **Session-cookie auth** — browser sends plaintext password over TLS; server hashes with bcrypt (`golang.org/x/crypto/bcrypt`, default cost) and stores the hash in the DB. On login the server issues a cryptographically random 64-hex-char session token as an `HttpOnly; Secure; SameSite=Strict` cookie. The `auth` middleware validates the cookie against an in-memory `sessionStore` on each authenticated request. `POST /api/logout` deletes the server-side session and clears the cookie. Non-browser API clients may pass `Authorization: Bearer <token>` instead. Legacy SHA-256 hashes (from the old client-side scheme) are detected by format and transparently upgraded to bcrypt on first successful login.
- **No framework** — `net/http` mux with Go 1.22+ path patterns (`GET /api/issues/{id}`)

## Database Schema

```sql
CREATE TABLE users (
    username      TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    password_hash TEXT NOT NULL,   -- bcrypt hash (legacy: SHA-256 hex, upgraded on login)
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

| File | Contents |
| --- | --- |
| `defaults.json` | `{"port": N, "database": "path", "idle_timeout": N, "app_name": "...", "app_description": "..."}` — persisted defaults; all fields are omitempty |
| `idtrack.pid` | PID of the running server process |
| `idtrack.log` | Stdout/stderr of the background server |

## CLI Verbs

### `idtrack version`

Prints the version string and build timestamp (when available). Example: `idtrack version 1.0-8 (built 20260516120000)`.

### `idtrack default [--port n] [--database path] [--idle-timeout duration] [--app-name text] [--app-description text]`

Merges the given values into `~/.idtrack/defaults.json`. Unspecified keys are preserved. Requires at least one flag.

- `--idle-timeout` accepts any Go duration string (`30m`, `1h`, `90s`). Use `0` to disable. The server returns this value from `GET /api/status`; the frontend enforces it as an idle-logout timer.
- `--app-name` sets a custom application name shown in the header, login screen, onboarding screen, and About dialog (default: `idtrack`).
- `--app-description` sets a custom tagline shown under the name on the login screen and About dialog (default: `Issue Tracker`).
- Both branding values are returned by `GET /api/status` as `app_name` and `app_description` (omitted when not set). The frontend applies them immediately after the status probe via `applyBranding()`.

### `idtrack serve [--port n] [--database path]`

- **Does not block the terminal.** Re-execs itself with `--foreground` as a background process using `exec.Command` + `Setsid: true` (new session, survives terminal close).
- Checks for a stale/live PID file before starting; errors if a server is already running.
- Redirects child stdout/stderr to `~/.idtrack/idtrack.log` (append mode).
- Writes child PID to `~/.idtrack/idtrack.pid`.
- Default port: **8443**. Default database: `idtrack.db` in the working directory.
- The `--foreground` flag is **internal** — it tells the re-exec'd child to run the server directly. Do not expose it in docs.

### `idtrack stop`

Reads `~/.idtrack/idtrack.pid`, sends `SIGTERM`, removes the PID file.

### `idtrack user <subcommand> [--database path]`

All `user` subcommands accept an optional `--database path` flag. Actions are positional subcommands (not flags):

- `idtrack user list` — tabular output: USERNAME, DISPLAY NAME, ADMIN, LAST LOGIN.
- `idtrack user add username:password [--name text] [--admin true|false]` — display name defaults to username; admin defaults to false; upserts on existing username.
- `idtrack user update username [--name text] [--password text] [--admin true|false]` — only updates fields explicitly provided; user must already exist; `--admin` validated as `"true"` or `"false"`.
- `idtrack user delete username` — hard-deletes the row; does not cascade to issues/comments.

### `idtrack define <subcommand> [--database path]`

- `idtrack define project name` — creates a new project (idempotent — uses `INSERT OR IGNORE`).
- `idtrack define component project-name component-name` — adds a component to an existing project. Errors if the project does not exist. Idempotent (`INSERT OR IGNORE`).

### `idtrack delete <subcommand> [--database path]`

- `idtrack delete project name` — deletes the project and all its components. Errors (with issue list) if any issues reference that project.
- `idtrack delete component project-name component-name` — deletes a single component. Errors (with issue list) if any issues reference that project+component pair.

## HTTP API

All authenticated endpoints use Basic Auth where the password field carries the SHA-256 hex hash (not the plaintext password). The `auth` middleware validates on every request and stores the `*db.User` in the request context.

| Method | Path | Auth | Admin required |
| ------ | ---- | ---- | -------------- |
| GET | `/api/version` | no | no |
| GET | `/api/status` | no | no |
| POST | `/api/login` | JSON body (validates) | no |
| POST | `/api/logout` | no | no |
| POST | `/api/onboarding` | one-time token | no |
| GET | `/api/users` | yes | no |
| POST | `/api/users` | yes | **yes** |
| PUT | `/api/users/{username}` | yes | **yes** |
| DELETE | `/api/users/{username}` | yes | **yes** |
| GET | `/api/projects` | yes | no |
| POST | `/api/projects` | yes | **yes** |
| POST | `/api/projects/{project}/components` | yes | **yes** |
| DELETE | `/api/projects/{project}` | yes | **yes** |
| DELETE | `/api/projects/{project}/components/{component}` | yes | **yes** |
| GET | `/api/issues` | yes | no |
| POST | `/api/issues` | yes | no |
| GET | `/api/issues/{id}` | yes | no |
| PUT | `/api/issues/{id}` | yes | reporter/assignee/admin |
| DELETE | `/api/issues/{id}` | yes | reporter/assignee/admin |
| POST | `/api/issues/{id}/comments` | yes | no |
| DELETE | `/api/issues/{id}/comments/{cid}` | yes | **yes** |

### Status response (`GET /api/status`)

Always returns `idle_timeout` (seconds, 0 = disabled). When no users exist in the database, also returns `onboarding: true` and a one-time UUID `token`:

```json
{ "onboarding": false, "idle_timeout": 1800 }
{ "onboarding": true,  "idle_timeout": 0, "token": "<uuid>" }
```

The UUID is generated lazily on first status call when onboarding is needed and held in memory on the `srv` struct (protected by `sync.Mutex`). It is cleared after `POST /api/onboarding` succeeds or after any user is found in the DB.

### Onboarding request (`POST /api/onboarding`)

Authorization header: `Basic base64("onboarding:<uuid>")`. Body: `{ username, display_name, password }` (plaintext password — hashed server-side). Creates the first user as an admin, clears the token, calls `RecordLogin`, sets a session cookie, and returns the same shape as `/api/login`. The endpoint returns 409 if users already exist.

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
_currentUser      // { username, display_name, is_admin }
_userMap          // { username: display_name } — built from /api/users at login
_projectData      // [{name, components: [...]}] — built from /api/projects at login
_allIssues        // full issue list, filtered/sorted client-side
_currentId        // currently selected issue id
_keepLoggedIn     // bool — mirrors localStorage pref; controls PERSIST_KEY writes
_idleTimeoutSecs  // int from /api/status; 0 = no timeout
_idleTimer        // setTimeout handle; reset on any user activity
```

### Session persistence (three layers)

`init()` always fetches `GET /api/status` first to capture `idle_timeout` and onboarding state, then checks two stores in order:

1. **`sessionStorage` (`idtrack_session`)** — `{ user }`. Survives page refresh, cleared when the tab closes. Written on every successful login. The actual session credential is the server-issued `idtrack_session` HttpOnly cookie — `sessionStorage` only carries the user display object so the UI can be restored without an extra round-trip.
2. **`localStorage` (`idtrack_persist`)** — `{ user }` (non-sensitive display object only, **no credentials**). Written when **Keep me logged in** is enabled. On the next browser session `init()` restores `_currentUser` from this object and calls `launchApp()`; if the 30-day session cookie has expired, the first API call returns 401 and the user is redirected to the login screen. Cleared on explicit sign-out.
3. **Login screen** — shown if neither store has a user object and onboarding is not required.

Preferences (dark mode, keep-me-logged-in) are in `localStorage` under `idtrack_prefs`.

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
- Hamburger menu shows two additional admin-only items: **Edit Users…** and **Edit Projects…**.
- **Edit Users…** opens `manage-users-overlay`, which lists all users and provides add/edit/delete in a single place. See "Overlay navigation pattern" below.
- **Edit Projects…** opens a two-screen overlay (`ep-list-overlay` → `ep-detail-overlay`). The list screen shows all projects; clicking one opens the detail screen where components can be added/deleted inline and the project can be deleted. A **+ New Project** button on the list screen opens the detail screen in new-project mode (name as a text input, components staged before creation). Both screens handle duplicate name checks case-insensitively.
- Non-admin users never see these controls. The server enforces admin on all mutate endpoints (returns 403 Forbidden).

### Status-change dialogs

Changing an issue's status triggers a dialog before the save completes:

- **Open → Resolved**: optional dialog with **Fixed Version** (text) and **Comment** (textarea). If either is filled, a comment is posted atomically with the status update: `Fixed in <version>\n\n<comment>` (parts omitted when empty). An **Assignee** is required before this transition is allowed — `saveIssueChanges()` blocks with an error if the field is empty.
- **Resolved → Open**: required dialog with a **Reason** textarea. The comment is mandatory; the dialog will not confirm until it is non-empty. The reason is posted as a comment atomically with the status change.

State: `_originalStatus` is set when an issue loads and updated after each successful save. `_pendingStatusData` captures the form fields while the dialog is open.

### Overlay navigation pattern

The manage-users overlay is a **parent overlay**: `openAddUserFromManage()` and `openEditUserFromManage(username)` hide it before opening the child overlay. `hideAddUser()` and `hideEditUser()` always call `openManageUsers()` when they close — so every exit path (success, cancel, backdrop click) refreshes and re-displays the user list. The Edit Projects overlay follows the same pattern: `ep-list-overlay` is the parent, `ep-detail-overlay` is the child; `hideProjectDetail()` always re-opens the list. Follow this pattern for any future consolidated-management overlays.

## Important Implementation Decisions

**Password hashing is server-side (bcrypt).** The browser sends the plaintext password over TLS to `POST /api/login`. The server hashes it with `bcrypt.GenerateFromPassword` (default cost) and compares against the stored bcrypt hash with `bcrypt.CompareHashAndPassword`. The DB stores the bcrypt hash string (begins with `$2a$`). Legacy SHA-256 hashes (64 lowercase hex chars, from the old client-side scheme) are detected by format in `db.IsLegacyHash` and verified via a constant-time SHA-256 comparison in `db.VerifyPassword`; they are transparently upgraded to bcrypt on next successful login via `db.UpgradePasswordHash`.

**SQLite with `MaxOpenConns(1)`.** SQLite doesn't support concurrent writers. Setting max open connections to 1 serializes all access and avoids `SQLITE_BUSY` errors.

**No comment–issue foreign key constraint in SQLite.** SQLite doesn't enforce foreign keys by default (requires `PRAGMA foreign_keys = ON`). The code instead manually deletes associated comments before deleting an issue in `db.DeleteIssue()`. The same manual cleanup is not needed for `DeleteUser` — orphaned reporter/assignee strings are acceptable.

**`serve` re-execs itself rather than forking.** Go doesn't have a clean `fork()` equivalent. The approach is: parent validates args, spawns `exec.Command(os.Executable(), "serve", "--foreground", ...)`, writes the child PID, exits. The child runs the actual blocking server. `Setsid: true` detaches the child from the parent's process group so it survives terminal close.

**`UpdateUser` requires `*bool` for `isAdmin`.** An empty string signals "not specified" for string fields, but a `bool` has no natural sentinel. Using `*bool` (nil = leave unchanged, non-nil = set) keeps the logic explicit and avoids accidentally clearing admin status when only updating a display name.

**Schema migrations are additive only.** New columns are always added with `DEFAULT` values via `addColumnIfMissing`. Existing data is never altered. This keeps the migration path trivially safe.

**Static assets are embedded.** The TLS cert/key and all web assets are compiled into the binary with `//go:embed resources`. Deployment is a single file copy.

**Onboarding uses a one-time in-memory UUID.** When `GET /api/status` detects an empty users table it generates a UUID, stores it on the `srv` struct behind a `sync.Mutex`, and returns it in the response. `POST /api/onboarding` validates `Authorization: Basic base64("onboarding:<uuid>")`, creates the first admin user, then clears the token. Because the token lives only in process memory it is lost on server restart — in that case the client simply receives a fresh UUID on the next status probe.

**`server.Start()` signature pattern for server-wide config.** `idleTimeout`, `appName`, and `appDescription` are all examples of the same pattern: add the field to the `defaults` struct in `main.go` (with `omitempty`), accept it via `idtrack default --flag`, pass it through `server.Start()`, store it on the `srv` struct, and expose it in `GET /api/status`. Follow this pattern for any future server-wide configuration values.

**"Keep me logged in" issues a 30-day session cookie.** When `keep_logged_in: true` is sent in the login body, the server creates a session with a 30-day TTL and sets `Max-Age=2592000` on the `idtrack_session` cookie. `localStorage` (under `PERSIST_KEY`) stores only the non-sensitive user display object `{ user }` — no credentials. On the next browser session `init()` restores `_currentUser` from this object and calls `launchApp()`; the browser sends the long-lived cookie automatically. If the session has expired or been invalidated, the first API call returns 401 and the user sees the login screen.

**Idle timeout is enforced entirely client-side.** The server communicates the timeout value via `GET /api/status` but does not enforce it server-side. The frontend attaches passive event listeners for mouse, keyboard, touch, and scroll events and resets a `setTimeout` on each. If the timer fires, `doLogout()` is called. `startIdleTracking()` / `stopIdleTracking()` are called in `launchApp()` and `doLogout()` respectively; they are no-ops when `_idleTimeoutSecs` is 0.

**Usernames are always lower-cased.** The browser lowercases the username value before sending it in the login/onboarding/add-user JSON bodies. The server lowercases `body.Username` in `handleOnboarding`, `handleCreateUser`, and `handleLogin`, and `r.PathValue("username")` in `handleUpdateUser` and `handleDeleteUser`. Username input fields carry `autocapitalize="none" autocorrect="off" spellcheck="false"` to suppress mobile keyboard transforms.

**CLI commands use positional subcommands, not flags for actions.** The `user`, `define`, and `delete` top-level commands all take a positional subcommand word as their first argument (`user list`, `user add`, `define project`, `delete component`, etc.). Options (values like `--name`, `--database`) remain as named flags. This is consistent across all three commands.

**Resolving an issue requires an assignee.** `saveIssueChanges()` blocks with an error if `status === 'Resolved'` and the assignee field is empty, before the resolve dialog is shown. This prevents issues from being closed without ownership.

**Status transitions post comments atomically with the save.** `doSaveIssue(commentBody)` calls `updateIssue()` then `addComment()` in sequence. If the comment fails after the issue update succeeds the status is still changed (no rollback). For Open→Resolved this is acceptable since the comment is optional; for Resolved→Open the required comment failing would be a server error unlikely in practice.
