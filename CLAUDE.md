# idtrack — Project Reference

## What It Is

`idtrack` is a self-contained Go binary that serves a web-based issue tracker over HTTPS. It replaces an earlier Ego-language backend. The binary handles both server duties and all administrative CLI operations (user management, configuration). There is no external dependency besides the SQLite database file.

## Repository Layout

```text
idtrack/
├── main.go               # Entry point: sets build vars, dispatches to commands.*
├── go.mod                # module: github.com/tucats/idtrack
├── Dockerfile            # Two-stage Docker build (builder → alpine runtime)
├── .dockerignore         # Files excluded from the Docker build context
├── tools/
│   ├── build             # Native build script (see Versioning section)
│   ├── buildver.txt      # Current version string, e.g. "1.0-34"
│   ├── build-container.sh       # Build the Docker image
│   ├── start-container.sh       # Start the container with all options
│   ├── install-service-macos.sh # Install/remove as a launchd service (macOS)
│   └── install-service-linux.sh # Install/remove as a systemd service (Linux)
├── commands/             # One exported function per CLI verb; main.go dispatches here
│   ├── common.go         # Shared: defaults struct, loadDefaults(), Usage(), package vars
│   ├── serve.go          # Serve(), Stop(), Restart(), launchBackground(), pid helpers
│   ├── defaults.go       # Default() — read/write defaults.json; showDefaults() table
│   ├── users.go          # User() — list/add/update/delete user accounts
│   ├── projects.go       # Define(), Delete() — project and component management
│   └── version.go        # Version() — print build version and timestamp
├── db/
│   ├── db.go             # Open(), schema init, migration helper
│   ├── users.go          # User CRUD + RecordLogin + UpdateUser + ListUsers
│   ├── issues.go         # Issue CRUD (list/get/create/update/delete)
│   ├── comments.go       # Comment CRUD + DeleteComment
│   └── projects.go       # Project/Component CRUD
├── server/
│   ├── server.go         # srv struct + Start() — route wiring and TLS setup
│   ├── middleware.go     # contextKey, auth(), requireJSON(), currentUser()
│   ├── helpers.go        # issueID(), jsonResponse(), jsonError()
│   ├── static.go         # static file handlers + handleManual()
│   ├── sessions.go       # sessionStore — create/lookup/delete session tokens
│   ├── backup.go         # startBackups(), doBackup(), quiesce(), ageBackups()
│   ├── auth_handlers.go  # handleVersion, handleStatus, handleOnboarding, handleLogin, handleLogout
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
- **HTTPS only** — TLS cert/key embedded in the binary via `embed.FS`; external cert/key files can be configured via `--server-cert`/`--server-key` to replace the built-in self-signed certificate
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
    component   TEXT NOT NULL DEFAULT '',
    resolved_at TEXT NOT NULL DEFAULT ''   -- set when status → Resolved; cleared when → Open
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

The schema is created fresh with `CREATE TABLE IF NOT EXISTS`. Columns added after the initial schema (like `last_login_at`, `is_admin`, `project`, `component`, `resolved_at`) are applied via `addColumnIfMissing()` in `db/db.go`, which runs `ALTER TABLE ... ADD COLUMN` and ignores "duplicate column name" errors. This means the binary upgrades existing databases automatically on startup with no migration tooling needed.

**`resolved_at` backfill migration.** When `resolved_at` is first added to an existing database, a one-time UPDATE sets it for all Resolved issues that have at least one comment, using `MAX(comments.created_at)` as the best available proxy for when the issue was actually closed. Issues with no comments keep `resolved_at = ''`. The UPDATE is guarded by `WHERE resolved_at = ''` so it is a no-op on subsequent startups.

## Runtime Files (`~/.idtrack/`)

All runtime state lives in `~/.idtrack/` (created with mode 0700):

| File | Contents |
| --- | --- |
| `defaults.json` | `{"port": N, "database": "path", "server_cert": "path", "server_key": "path", "idle_timeout": N, "app_name": "...", "app_description": "...", "backup_interval": "1h", "backup_count": N, "backup_age": "168h"}` — persisted defaults; all fields are omitempty |
| `idtrack.pid` | PID of the running server process |
| `idtrack.log` | Stdout/stderr of the background server |

## CLI Verbs

### `idtrack version`

Prints the version string and build timestamp (when available). Example: `idtrack version 1.0-8 (built 20260516120000)`.

### `idtrack default [--port n] [--database path] [--server-cert path] [--server-key path] [--idle-timeout duration] [--app-name text] [--app-description text] [--backup-interval duration] [--backup-count n] [--backup-age duration]`

Merges the given values into `~/.idtrack/defaults.json`. Unspecified keys are preserved. Requires at least one flag. Running with no flags prints a two-column table of the current defaults.

- `--server-cert` / `--cert` / `--cert-file` set an absolute path to a PEM TLS certificate file. The file must exist at save time; the path is resolved to absolute before storing. Use `off` to clear the setting and revert to the built-in self-signed certificate. Both `server_cert` and `server_key` must be set together for external TLS to work.
- `--server-key` / `--key` / `--key-file` set an absolute path to a PEM TLS private key file. Same validation and `off` semantics as `--server-cert`.
- `--idle-timeout` accepts any Go duration string (`30m`, `1h`, `90s`). Use `0` or `off` to disable. The server returns this value from `GET /api/status`; the frontend enforces it as an idle-logout timer.
- `--app-name` sets a custom application name shown in the header, login screen, onboarding screen, and About dialog (default: `idtrack`).
- `--app-description` sets a custom tagline shown under the name on the login screen and About dialog (default: `Issue Tracker`).
- Both branding values are returned by `GET /api/status` as `app_name` and `app_description` (omitted when not set). The frontend applies them immediately after the status probe via `applyBranding()`.
- `--backup-interval` accepts any Go duration string (`1h`, `30m`). Use `0` or `off` to disable backups. Stored as a string in `defaults.json` and parsed to `time.Duration` in `commands.Serve`.
- `--backup-count` is a non-negative integer. Use `0` or `off` for no count limit.
- `--backup-age` accepts any Go duration string. Use `0` or `off` to disable age-based pruning. Stored as a string in `defaults.json`.

### `idtrack serve [--port n] [--database path] [--server-cert path] [--server-key path]`

- **Does not block the terminal.** Re-execs itself with `--foreground` as a background process using `exec.Command` + `Setsid: true` (new session, survives terminal close).
- Checks for a stale/live PID file before starting; errors if a server is already running.
- Redirects child stdout/stderr to `~/.idtrack/idtrack.log` (append mode).
- Writes child PID to `~/.idtrack/idtrack.pid`.
- Default port: **8443**. Default database: `idtrack.db` in the working directory.
- `--server-cert` / `--cert` / `--cert-file` and `--server-key` / `--key` / `--key-file` override the TLS credentials for this run only (do not persist to `defaults.json`). When absent, values from `defaults.json` are used; if those are also absent, the built-in self-signed cert/key are used.
- The `--foreground` flag is **internal** for direct host usage — it tells the re-exec'd child to run the server directly. It is exposed and documented in the Docker section of MANUAL.md because containers require foreground operation (Docker manages the process lifecycle; the main process must not exit).

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

Authenticated endpoints require a valid session token delivered as an `HttpOnly; Secure; SameSite=Strict` cookie named `idtrack_session`, or via `Authorization: Bearer <token>`. The `auth` middleware validates the token against the in-memory `sessionStore` on every request and stores the `*db.User` in the request context. Sessions are created by `POST /api/login` and `POST /api/onboarding`; deleted by `POST /api/logout`. JSON-body endpoints additionally require `Content-Type: application/json` (enforced by the `requireJSON` middleware).

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
| GET | `/api/issues/changes` | yes | no |
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

`GET /api/issues?status=open|resolved&priority=High|Medium|Low&project=<name>&search=text&sort=col&order=asc|desc&limit=N&offset=N`

When `limit > 0` the response envelope is `{ issues: [...], total: N, offset: N, limit: N }` where `total` is the full count of matching rows (for displaying "N of M issues"). When `limit == 0` (legacy / return-all) `total` equals `len(issues)`. `sort` accepts: `id`, `title`, `priority`, `status`, `assignee`, `project`, `component`, `created_at`, `updated_at`. Unknown columns fall back to `id DESC`.

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

### Responsive Web Design

Two CSS breakpoints in `idtrack.css` handle phone and tablet layouts:

**Tablet (≤900px)** — stacked layout:

- `.main-layout` switches to `flex-direction: column`.
- The detail panel takes full width (`width: 100%; max-width: none`).
- When an issue is selected, `selectIssue()` adds the `has-detail` class to `#main-layout`; `closeDetail()` removes it. The CSS rule `.main-layout.has-detail .list-panel { display: none }` hides the list, and `.main-layout.has-detail .detail-panel { flex: 1 }` gives the detail panel the full remaining height. This gives a full-screen detail experience at tablet sizes instead of the cramped split-panel.
- The filter bar (`.header-center`) is hidden at this breakpoint and re-exposed at ≤600px.

**Phone (≤600px)** — compact layout:

- Header wraps to two rows: title + action buttons on row 1, filter strip on row 2. The filter strip is horizontally scrollable and label-free.
- The user badge is hidden (the username is accessible via the hamburger menu).
- Issues table shows only **#, Title, Priority, Status** — the Project, Component, Assignee, and Date columns are hidden via `display: none` on their respective `.col-*` classes.
- Sheets/overlays become full-width bottom drawers (rounded top corners, `align-items: flex-end` on `.overlay`). Login and onboarding sheets remain vertically centred since they are first-run flows, not contextual actions.
- `form-row` stacks vertically so password-pair fields don't side-by-side on narrow screens.
- The Last Login column in the manage-users table is hidden.

**Layout foundation** — `#app { display: flex; flex-direction: column; height: 100% }` makes the app a flex column so `.app-header` drives its own height and `.main-layout { flex: 1; min-height: 0 }` fills whatever remains. This replaces the old `calc(100vh - 52px)` approach and correctly handles a taller two-row header on mobile without any JavaScript measurement.

**"Always show desktop version" setting** — a toggle in Settings that adds the class `desktop-mode` to `<html>`. Every responsive CSS rule is gated on `html:not(.desktop-mode)`, so when the class is present all mobile/tablet overrides become inert and the app renders as a full desktop layout regardless of viewport width (the user will need to pinch-zoom or scroll horizontally). The preference is stored in `idtrack_prefs` under `desktopMode`. To prevent a flash of mobile layout on reload, a minified inline `<script>` in `<head>` reads `localStorage` and applies the class before the browser renders the first frame — the same class that `toggleDesktopMode()` and `loadPrefs()` manage at runtime.

## Important Implementation Decisions

**Service managers (launchd, systemd) also require `--foreground`.** Both `install-service-macos.sh` and `install-service-linux.sh` embed `--foreground` in the generated plist/unit file. The reasoning is identical to the Docker case: without it, `idtrack serve` forks a background child and exits, and the service manager concludes the service failed and immediately tries to restart it in a tight loop. With `--foreground`, the process blocks in the HTTP server loop and the service manager tracks it correctly. The macOS script generates a plist with `RunAtLoad=true` and `KeepAlive=true`; the Linux script generates a unit file with `Type=simple` and `Restart=on-failure`. Both install to the appropriate directory (LaunchAgent `~/Library/LaunchAgents/` or LaunchDaemon `/Library/LaunchDaemons/`; systemd `/etc/systemd/system/` or `~/.config/systemd/user/`) and accept the full set of idtrack server options.

**Docker containers require `--foreground` to stay alive.** `idtrack serve` without `--foreground` re-execs a background child and exits. In a container that exit kills the container because PID 1 has ended. The `Dockerfile` CMD and `tools/start-container.sh` always pass `--foreground`. The SQLite database and backup files are stored outside the container via a host bind mount at `/data`. The `tools/build-container.sh` script reads `tools/buildver.txt` and passes `--build-arg BUILD_VERSION` so the image's version output matches the tag. The binary is built with `CGO_ENABLED=0` inside the Docker builder stage (safe because `modernc.org/sqlite` is pure Go), producing a fully static binary that runs in the Alpine runtime image without any C runtime dependency.

**External TLS cert/key replaces the embedded self-signed certificate.** When `server_cert` and `server_key` are set in `defaults.json` (or passed directly to `idtrack serve`), `server.Start()` reads the PEM files from disk via `os.ReadFile` instead of from the embedded `embed.FS`. Both must be set together — the server will fail to start if only one is present (the cert and key must form a matching pair for `tls.X509KeyPair`). `idtrack default --server-cert` validates that the file exists and resolves it to an absolute path before saving, so a relative path at save time won't silently break after a working-directory change. Use `off` as the value to clear either setting and revert to the built-in certificate.

**`off` is a synonym for the zero/disabled value on duration and count flags.** `--idle-timeout off`, `--backup-interval off`, `--backup-count off`, and `--backup-age off` all behave identically to `0`. This makes the intent explicit in shell scripts or documentation where the word "off" reads more clearly than the number zero.

**Password hashing is server-side (bcrypt).** The browser sends the plaintext password over TLS to `POST /api/login`. The server hashes it with `bcrypt.GenerateFromPassword` (default cost) and compares against the stored bcrypt hash with `bcrypt.CompareHashAndPassword`. The DB stores the bcrypt hash string (begins with `$2a$`). Legacy SHA-256 hashes (64 lowercase hex chars, from the old client-side scheme) are detected by format in `db.IsLegacyHash` and verified via a constant-time SHA-256 comparison in `db.VerifyPassword`; they are transparently upgraded to bcrypt on next successful login via `db.UpgradePasswordHash`.

**SQLite with `MaxOpenConns(1)`.** SQLite doesn't support concurrent writers. Setting max open connections to 1 serializes all access and avoids `SQLITE_BUSY` errors.

**No comment–issue foreign key constraint in SQLite.** SQLite doesn't enforce foreign keys by default (requires `PRAGMA foreign_keys = ON`). The code instead manually deletes associated comments before deleting an issue in `db.DeleteIssue()`. The same manual cleanup is not needed for `DeleteUser` — orphaned reporter/assignee strings are acceptable.

**`serve` re-execs itself rather than forking.** Go doesn't have a clean `fork()` equivalent. The approach is: `commands.Serve` validates args, calls `launchBackground` which spawns `exec.Command(os.Executable(), "serve", "--foreground", ...)`, writes the child PID, and exits. The child runs `commands.Serve` again with `--foreground` set and blocks in the HTTP server loop. `Setsid: true` detaches the child from the parent's process group so it survives terminal close. All of this logic lives in `commands/serve.go`.

**`UpdateUser` requires `*bool` for `isAdmin`.** An empty string signals "not specified" for string fields, but a `bool` has no natural sentinel. Using `*bool` (nil = leave unchanged, non-nil = set) keeps the logic explicit and avoids accidentally clearing admin status when only updating a display name.

**Schema migrations are additive only.** New columns are always added with `DEFAULT` values via `addColumnIfMissing`. Existing data is never altered. This keeps the migration path trivially safe.

**Static assets are embedded.** The TLS cert/key and all web assets are compiled into the binary with `//go:embed resources`. Deployment is a single file copy.

**`main.go` owns the two things that cannot move.** `BuildVersion` and `BuildTime` must live in `package main` because the build script injects them via `-ldflags "-X main.BuildVersion=..."`. The embedded filesystem must also stay in `main` because `//go:embed resources` requires the `resources/` directory to be a sibling of the source file. `main()` copies the build vars into `commands.BuildVersion` / `commands.BuildTime` before dispatching, and passes `fs.FS(embedded)` directly to `commands.Serve`. Everything else lives in the `commands` package.

**Onboarding uses a one-time in-memory UUID.** When `GET /api/status` detects an empty users table it generates a UUID, stores it on the `srv` struct behind a `sync.Mutex`, and returns it in the response. `POST /api/onboarding` validates `Authorization: Basic base64("onboarding:<uuid>")`, creates the first admin user, then clears the token. Because the token lives only in process memory it is lost on server restart — in that case the client simply receives a fresh UUID on the next status probe.

**`server.Start()` signature pattern for server-wide config.** `idleTimeout`, `appName`, `appDescription`, and the backup params (`dbPath`, `backupInterval`, `backupCount`, `backupAge`) are all examples of the same pattern: add the field to the `defaults` struct in `commands/common.go` (with `omitempty`), add the flag to `commands.Default`, parse and pass the value through `server.Start()` from `commands.Serve`, and store it on the `srv` struct. Duration-type flags are stored as strings in `defaults.json` and parsed to `time.Duration` in `commands.Serve`. Follow this pattern for any future server-wide configuration values.

**"Keep me logged in" issues a 30-day session cookie.** When `keep_logged_in: true` is sent in the login body, the server creates a session with a 30-day TTL and sets `Max-Age=2592000` on the `idtrack_session` cookie. `localStorage` (under `PERSIST_KEY`) stores only the non-sensitive user display object `{ user }` — no credentials. On the next browser session `init()` restores `_currentUser` from this object and calls `launchApp()`; the browser sends the long-lived cookie automatically. If the session has expired or been invalidated, the first API call returns 401 and the user sees the login screen.

**Idle timeout is enforced entirely client-side.** The server communicates the timeout value via `GET /api/status` but does not enforce it server-side. The frontend attaches passive event listeners for mouse, keyboard, touch, and scroll events and resets a `setTimeout` on each. If the timer fires, `doLogout()` is called. `startIdleTracking()` / `stopIdleTracking()` are called in `launchApp()` and `doLogout()` respectively; they are no-ops when `_idleTimeoutSecs` is 0.

**Usernames are always lower-cased.** The browser lowercases the username value before sending it in the login/onboarding/add-user JSON bodies. The server lowercases `body.Username` in `handleOnboarding`, `handleCreateUser`, and `handleLogin`, and `r.PathValue("username")` in `handleUpdateUser` and `handleDeleteUser`. Username input fields carry `autocapitalize="none" autocorrect="off" spellcheck="false"` to suppress mobile keyboard transforms.

**CLI commands use positional subcommands, not flags for actions.** The `user`, `define`, and `delete` top-level commands all take a positional subcommand word as their first argument (`user list`, `user add`, `define project`, `delete component`, etc.). Options (values like `--name`, `--database`) remain as named flags. This is consistent across all three commands.

**Resolving an issue requires an assignee.** `saveIssueChanges()` blocks with an error if `status === 'Resolved'` and the assignee field is empty, before the resolve dialog is shown. This prevents issues from being closed without ownership.

**Status transitions post comments atomically with the save.** `doSaveIssue(commentBody)` calls `updateIssue()` then `addComment()` in sequence. If the comment fails after the issue update succeeds the status is still changed (no rollback). For Open→Resolved this is acceptable since the comment is optional; for Resolved→Open the required comment failing would be a server error unlikely in practice.

**`requireJSON` middleware enforces Content-Type on JSON-body endpoints (S-11).** Applied selectively at route-registration time (`mux.Handle("POST /api/...", requireJSON(http.HandlerFunc(...)))`), not globally, so endpoints with no body (logout, DELETE routes) are unaffected. Returns 415 Unsupported Media Type when the header is absent or wrong.

**Issue authorization: reporter, assignee, or admin may modify/delete (S-12 adjacent).** `issueModifier(u *db.User, issue *db.Issue) bool` checks `u.IsAdmin || u.Username == issue.Reporter || u.Username == issue.Assignee`. Both `handleUpdateIssue` and `handleDeleteIssue` fetch the current issue record first and call `issueModifier`; a third-party authenticated user receives 403. Any authenticated user may create a comment on any issue.

**Comment parent validation prevents orphaned comments (S-12).** `handleCreateComment` calls `db.GetIssue` before inserting the comment row. A non-existent issue ID returns 404 rather than creating a comment with a dangling `issue_id`.

**Last-admin guard blocks lockout (S-14).** `db.CountAdmins` counts rows with `is_admin = 1`. Both `handleDeleteUser` and `handleUpdateUser` call it when the operation would leave no admin: deletion of the last admin returns 400 with a message directing the operator to use the CLI; demotion of the last admin is blocked the same way. The last-admin check runs before the self-deletion check in `handleDeleteUser` so the more informative message takes priority when both conditions apply.

**Configurable column visibility uses CSS class-gating, not DOM rebuilding.** Nine optional columns can be toggled via a "Columns ▾" dropdown in the header. Visibility state is stored in `_colVisibility` (keyed by CSS class suffix, e.g. `"project"`, `"resolved"`) and persisted in `localStorage` under `idtrack_prefs.colVisibility`. `applyColVisibility()` toggles `html.hide-col-X` classes on `<html>`; the rule `html.hide-col-project .col-project { display: none }` hides both the `<th>` header and every `<td>` data cell without touching or re-rendering the DOM rows. The `<head>` inline script pre-applies these classes before first render to prevent a flash of all columns on load. `issueRow()` always emits all cells; CSS does all the hiding. Phone breakpoint (≤600px) adds a separate media-query hide for all optional columns except Priority and Status — the two visibility mechanisms compose additively. The "Columns" button itself is hidden on phone (column choice is irrelevant there). ID and Title are always visible and have no hide-col class. Default visibility: Project, Component, Status, Priority, Assignee, Created = on; Reporter, Resolved, Comments = off.

**`resolved_at` is set and cleared by `UpdateIssue` automatically.** The UPDATE uses a CASE expression: if the new status is `'Resolved'` and `resolved_at` is currently empty, it is set to `now`; if the new status is `'Open'`, it is cleared to `''` (so a later re-resolution gets a fresh timestamp); otherwise it is left unchanged (prevents re-saving a Resolved issue from overwriting the original resolved date). The `comment_count` field in the API response is a correlated subquery: `(SELECT COUNT(*) FROM comments WHERE issue_id = issues.id) AS comment_count`; the `idx_comments_issue_id` index keeps it fast.

**Full-screen detail panel on mobile uses a CSS class, not JS visibility logic.** At ≤900px, when an issue is selected the JS adds `has-detail` to `#main-layout`; closing removes it. The CSS rule `.main-layout.has-detail .list-panel { display: none }` handles the panel switch. This keeps the responsive behaviour entirely in CSS — the class is harmless above 900px where no matching media-query rule exists, so no viewport-width check is needed in JS.

**Server-side pagination, filtering, sorting, and background polling.** The issue list uses a server-driven append-only window model (`_issueWindow`) rather than loading all issues into memory. `loadIssueWindow()` resets the window and fetches the first page; an `IntersectionObserver` on a bottom sentinel `<div>` calls `loadNextPage()` to fetch subsequent pages as the user scrolls. All filtering (status, priority, project, search), sorting (column + direction), and pagination (LIMIT/OFFSET) are sent to the server as query parameters — the client does no in-memory filtering. The server runs a `SELECT COUNT(*)` with the same WHERE clause when `limit > 0` so the client knows the total without fetching all rows. A `_fetchGen` counter lets `loadIssueWindow()` discard the response of a superseded request when filters change rapidly. `_fetchLock` prevents concurrent `loadNextPage` calls. After a save, the matching `_issueWindow` entry and DOM row are updated in-place via `data-id` attribute lookup — no full re-fetch. A 30-second `setInterval` polling loop (`pollForChanges`) calls `GET /api/issues/changes?since=<timestamp>` to detect changes made by other users; issues already in the window are updated in-place, new external changes show a fixed-position toast (`#refresh-hint`) with a "Refresh" button that calls `loadIssueWindow()`. The page size (10/25/50/100/200, default 50) is configurable in Settings and persisted to `localStorage`. SQLite indexes on `status`, `(status, priority)`, `updated_at`, `assignee`, and `reporter` keep all filtered queries fast.

**`GET /api/issues/changes?since=<RFC3339>`** returns `{ issues: [...] }` — all issues whose `updated_at` is strictly after the given timestamp, ordered by `updated_at ASC`. Used by the polling loop to detect mutations without fetching the entire list. Registered before the `/{id}` wildcard pattern in `server/server.go` so the literal path `/changes` takes priority.

**`db.CountIssues` and `db.ListIssues` share a WHERE clause builder.** `buildWhereClause(status, priority, search, project string)` in `db/issues.go` returns a `(clause string, args []interface{})` pair that is reused by both functions, ensuring the count and the data query always agree. `ORDER BY` is constructed from a lookup table of hardcoded `"column ASC/DESC"` literals (keyed by column name) to prevent SQL injection via the `sort` and `order` query parameters.

**Backup strategy: filesystem copy with RWMutex quiescing.** When `backupInterval > 0`, `server/backup.go` manages all backup logic. `startBackups()` is called in `Start()` before `httpSrv.Serve` (so the first backup is written before any requests are served). It creates an `idtrack-backups/` directory next to the database file, writes an initial backup synchronously, then launches a goroutine that fires `doBackup()` every `backupInterval`. `doBackup` takes `s.backupMu.Lock()` (write lock) to quiesce the server, calls `copyFile` (io.Copy + fsync), releases the lock, then runs `ageBackups` in a separate goroutine. Every HTTP request holds `s.backupMu.RLock()` via the `quiesce` middleware, which wraps the entire mux. The RWMutex ensures: in-flight requests finish before the backup copy starts; new requests block (briefly) while the copy is in progress; no 503 is returned to clients. `ageBackups` enforces count pruning first (delete oldest beyond limit), then age pruning (delete files whose name-embedded timestamp is before `now − backupAge`). Backup filenames embed the UTC timestamp (`idtrack-20060102T150405.db`) so alphabetical and chronological order coincide and the age can be recovered from the name without touching the filesystem mtime.
