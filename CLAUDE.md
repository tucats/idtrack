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
│   ├── teams.go          # Teams() — list/add/update/delete teams
│   ├── projects.go       # Define(), Delete() — project and component management
│   ├── ingest.go         # Ingest() — bulk-create issues from files, transaction-wrapped
│   ├── ingest_parse.go   # Pure parsing/inference helpers used by Ingest (no DB access)
│   └── version.go        # Version() — print build version and timestamp
├── db/
│   ├── db.go             # Open(), schema init, migration helper
│   ├── users.go          # User CRUD + RecordLogin + UpdateUser + ListUsers
│   ├── teams.go          # Team CRUD + ParseTeams/FormatTeams + Issue/ProjectMatchesUserTeams
│   ├── issues.go         # Issue CRUD (list/get/create/update/delete); Querier interface
│   ├── comments.go       # Comment CRUD + DeleteComment + GetComment
│   ├── attachments.go    # Attachment CRUD (metadata + blob fetch) + cascade-delete helpers
│   └── projects.go       # Project/Component CRUD
├── server/
│   ├── server.go         # srv struct + Start() — route wiring and TLS setup
│   ├── middleware.go     # contextKey, auth(), requireJSON(), requireMultipart(), currentUser()
│   ├── ratelimit.go      # per-IP login rate limiting (rateLimiter)
│   ├── helpers.go        # issueID(), jsonResponse(), jsonError()
│   ├── static.go         # static file handlers + handleManual()
│   ├── minify.go         # JavaScript minifier used when serving idtrack.js
│   ├── sessions.go       # sessionStore — create/lookup/delete session tokens
│   ├── compress.go       # gzipHandler middleware + bufferingWriter
│   ├── backup.go         # startBackups(), doBackup(), quiesce(), sizeBackups(), ageBackups()
│   ├── render.go         # renderFormatted() — goldmark Markdown rendering for issue/comment bodies
│   ├── auth_handlers.go  # handleVersion, handleStatus, handleOnboarding, handleLogin, handleLogout
│   ├── users.go          # user CRUD handlers
│   ├── teams.go          # team CRUD handlers
│   ├── projects.go       # project/component CRUD handlers
│   ├── issues.go         # issue CRUD handlers
│   ├── comments.go       # handleCreateComment, handleDeleteComment
│   ├── attachments.go    # attachment upload/list/fetch/delete handlers
│   └── images.go         # processUploadedImage() — decode/validate/convert-to-PNG/thumbnail
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
- **HTTPS by default** — TLS cert/key embedded in the binary via `embed.FS`; external cert/key files can be configured via `--server-cert`/`--server-key` to replace the built-in self-signed certificate. `--insecure`/`-k` switches the listener to plain HTTP with no cert/key at all, for deployments behind a TLS-terminating reverse proxy (see "Insecure mode" below)
- **Session-cookie auth** — browser sends plaintext password over TLS; server hashes with bcrypt (`golang.org/x/crypto/bcrypt`, default cost) and stores the hash in the DB. On login the server issues a cryptographically random 64-hex-char session token as an `HttpOnly; Secure; SameSite=Strict` cookie. The `auth` middleware validates the cookie against an in-memory `sessionStore` on each authenticated request. `POST /api/logout` deletes the server-side session and clears the cookie. Non-browser API clients may pass `Authorization: Bearer <token>` instead. Legacy SHA-256 hashes (from the old client-side scheme) are detected by format and transparently upgraded to bcrypt on first successful login.
- **No framework** — `net/http` mux with Go 1.22+ path patterns (`GET /api/issues/{id}`)
- **Markdown rendering** — `github.com/yuin/goldmark` renders issue/comment bodies server-side when an issue's `format` is `"markdown"` (see `server/render.go`); the only other non-stdlib runtime dependencies are `modernc.org/sqlite` and `golang.org/x/crypto`

## Database Schema

```sql
CREATE TABLE users (
    username      TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    password_hash TEXT NOT NULL,   -- bcrypt hash (legacy: SHA-256 hex, upgraded on login)
    created_at    TEXT NOT NULL,   -- RFC3339 UTC
    -- added via migration:
    last_login_at TEXT NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0,  -- 0=false, 1=true; kept in sync with teams below for compatibility
    teams         TEXT NOT NULL DEFAULT ''     -- comma-separated, lower-case team names (see teams table)
);

CREATE TABLE issues (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    reporter    TEXT NOT NULL,     -- username (login name, not display name)
    assignee    TEXT NOT NULL DEFAULT '',
    priority    TEXT NOT NULL DEFAULT 'Medium',  -- High/Medium/Low
    status      TEXT NOT NULL DEFAULT 'Open',    -- Open/Resolved/Blocked/Duplicate
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    -- added via migration:
    project          TEXT NOT NULL DEFAULT '',
    component        TEXT NOT NULL DEFAULT '',
    resolved_at      TEXT NOT NULL DEFAULT '',    -- set when status → Resolved/Duplicate; cleared when → Open/Blocked
    dependent_issues TEXT NOT NULL DEFAULT '',    -- comma-separated issue IDs: one for Duplicate, one+ for Blocked
    teams            TEXT NOT NULL DEFAULT '',    -- comma-separated team names that can see this issue
    format           TEXT NOT NULL DEFAULT 'text' -- "text" | "markdown" | "html" — see server/render.go
);

CREATE TABLE comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id   INTEGER NOT NULL,
    author     TEXT NOT NULL,      -- username
    body       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE projects (
    name  TEXT PRIMARY KEY,
    -- added via migration:
    teams TEXT NOT NULL DEFAULT ''  -- comma-separated team names that can see this project
);

CREATE TABLE components (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL,
    name    TEXT NOT NULL,
    UNIQUE(project, name)
);

CREATE TABLE teams (
    name        TEXT PRIMARY KEY,      -- lower-case; "admin" and "any" are reserved (db.TeamAdmin/db.TeamAny)
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE webauthn_credentials (
    id            TEXT PRIMARY KEY,     -- base64url WebAuthn credential ID
    username      TEXT NOT NULL,        -- owning user
    public_key    BLOB NOT NULL,        -- COSE public key
    sign_count    INTEGER NOT NULL DEFAULT 0,  -- clone-detection counter; updated on every login
    transports    TEXT NOT NULL DEFAULT '',    -- comma-separated
    name          TEXT NOT NULL DEFAULT '',    -- user-supplied label, e.g. "MacBook Touch ID"
    created_at    TEXT NOT NULL,
    last_used_at  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE attachments (
    id          TEXT PRIMARY KEY,          -- UUID
    issue_id    INTEGER NOT NULL,
    comment_id  INTEGER NOT NULL DEFAULT 0, -- 0 = attached to the issue description; >0 = attached to that comment
    uploader    TEXT NOT NULL,             -- username
    filename    TEXT NOT NULL DEFAULT '',  -- original filename, display only
    width       INTEGER NOT NULL DEFAULT 0, -- of the stored (converted) PNG
    height      INTEGER NOT NULL DEFAULT 0,
    size        INTEGER NOT NULL DEFAULT 0, -- byte length of the stored PNG
    image       BLOB NOT NULL,             -- full-size PNG
    thumbnail   BLOB NOT NULL,             -- thumbnail PNG
    created_at  TEXT NOT NULL
);
```

### Schema Migrations

The schema is created fresh with `CREATE TABLE IF NOT EXISTS`. Columns added after the initial schema (`last_login_at`, `is_admin`, `project`, `component`, `resolved_at`, `dependent_issues`, `teams` on users/projects/issues, `format`) are applied via `addColumnIfMissing()` in `db/db.go`, which runs `ALTER TABLE ... ADD COLUMN` and ignores "duplicate column name" errors. This means the binary upgrades existing databases automatically on startup with no migration tooling needed.

**`resolved_at` backfill migration.** When `resolved_at` is first added to an existing database, a one-time UPDATE sets it for all Resolved issues that have at least one comment, using `MAX(comments.created_at)` as the best available proxy for when the issue was actually closed. Issues with no comments keep `resolved_at = ''`. The UPDATE is guarded by `WHERE resolved_at = ''` so it is a no-op on subsequent startups.

**Teams backfill migration.** `initSchema` seeds the two reserved teams (`admin`, `any`) with `INSERT OR IGNORE`, then backfills the new `teams` columns: existing admin users get `'admin'`, everyone else gets `'any'`; existing projects and issues all get `'any'` (visible to everyone, preserving pre-teams behavior). Each backfill is guarded by `WHERE teams = ''`, so it is a no-op on subsequent startups and never overwrites an operator's explicit team assignment.

**`webauthn_credentials` needed no migration step at all.** Unlike every column listed above, it is a brand-new table, not a column added to an existing one — `CREATE TABLE IF NOT EXISTS` in the same block as every other table already creates it for free the first time an old database is opened with a binary that knows about it, with nothing to backfill since it starts (and stays) empty until a user registers a passkey.

**`attachments` likewise needed no migration step.** Same reasoning as `webauthn_credentials` above: it's a brand-new table, so `CREATE TABLE IF NOT EXISTS` alone upgrades an old database for free, with nothing to backfill since it starts (and stays) empty until someone uploads an image.

## Runtime Files (`~/.idtrack/`)

All runtime state lives in `~/.idtrack/` (created with mode 0700):

| File | Contents |
| --- | --- |
| `defaults.json` | `{"port": N, "database": "path", "server_cert": "path", "server_key": "path", "idle_timeout": N, "app_name": "...", "app_description": "...", "backup_interval": "1h", "backup_count": N, "backup_age": "168h", "backup_size": "500mb", "insecure": true, "base_path": "/idtrack", "webauthn_enabled": true, "webauthn_rp_id": "issues.example.com", "webauthn_rp_origin": "https://issues.example.com"}` — persisted defaults; all fields are omitempty |
| `idtrack.pid` | PID of the running server process |
| `idtrack.log` | Stdout/stderr of the background server |

## CLI Verbs

### `idtrack version`

Prints the version string and build timestamp (when available). Example: `idtrack version 1.0-8 (built 20260516120000)`.

### `idtrack default [--port n] [--database path] [--server-cert path] [--server-key path] [--idle-timeout duration] [--app-name text] [--app-description text] [--backup-interval duration] [--backup-count n] [--backup-age duration] [--backup-size size] [--insecure | -k [true|false]] [--base-path path | off] [--webauthn [true|false]] [--webauthn-rp-id domain] [--webauthn-rp-origin origin]`

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
- `--backup-size` accepts a number with optional unit suffix (`b`, `kb`, `mb`, `gb`, `tb`, case-insensitive); decimal values like `.5gb` are accepted. Use `0` or `off` to disable size-based thinning. Stored as a raw string in `defaults.json` (e.g. `"500mb"`) and parsed to `int64` bytes by `parseBackupSize()` in `commands/common.go` before being passed to `server.Start()`.
- `--insecure` / `-k` sets the server to listen with plain HTTP instead of TLS, requiring no cert/key at all — see "Insecure mode" below. A bare `--insecure` sets it to `true`; an explicit following value of `true`/`on` or `false`/`off` sets it accordingly, which is how a stored `true` default is turned back off (e.g. `idtrack default --insecure false`).
- `--base-path` mounts the whole app — page, static assets, and every `/api/*` route — under a URL prefix instead of the origin root, e.g. `/idtrack`. Validated by `validateBasePath()` in `commands/common.go`: must start with `/` and must not end with `/` or be exactly `/`. Use `""` or `off` to clear it and revert to origin-root mounting. See "Configurable base path for reverse-proxy mounting" below.
- `--webauthn` turns passkey (Touch ID/Face ID/security key) login on or off for the whole instance — same `[true|false]` bare-flag-defaults-to-true parsing shape as `--insecure`. Turning it on requires `--webauthn-rp-id` and `--webauthn-rp-origin` to already be set or supplied in the same command; `Default` exits with an error otherwise (mirrors the cert/key pairing check). See "Passkey (WebAuthn) login" below.
- `--webauthn-rp-id` sets the WebAuthn Relying Party ID: the bare domain the browser sees, e.g. `issues.example.com` (no scheme, no port).
- `--webauthn-rp-origin` sets the WebAuthn Relying Party Origin: the full browser-facing origin, e.g. `https://issues.example.com`. Both RP values are stored as plain strings in `defaults.json` with no `off` synonym (there is nothing to validate beyond "non-empty" — the operator supplies whatever their reverse proxy/DNS actually presents to browsers).

### `idtrack serve [--port n] [--database path] [--server-cert path] [--server-key path] [--insecure | -k [true|false]] [--base-path path]`

- **Does not block the terminal.** Re-execs itself with `--foreground` as a background process using `exec.Command` + `Setsid: true` (new session, survives terminal close).
- Checks for a stale/live PID file before starting; errors if a server is already running.
- Redirects child stdout/stderr to `~/.idtrack/idtrack.log` (append mode).
- Writes child PID to `~/.idtrack/idtrack.pid`.
- Default port: **8443**. Default database: `idtrack.db` in the working directory.
- `--server-cert` / `--cert` / `--cert-file` and `--server-key` / `--key` / `--key-file` override the TLS credentials for this run only (do not persist to `defaults.json`). When absent, values from `defaults.json` are used; if those are also absent, the built-in self-signed cert/key are used.
- `--insecure` / `-k` overrides the stored `insecure` default for this run only. When set, `server.Start()` skips reading/parsing any TLS cert or key (embedded, defaults-configured, or flag-overridden) entirely and binds a plain `net.Listener` instead of wrapping it with `tls.NewListener`. Forwarded into `passArgs` (as `--insecure` or `--insecure false`) so `Restart` relaunches with the same setting.
- `--base-path` overrides the stored `base_path` default for this run only, validated the same way as in `idtrack default`. Forwarded into `passArgs` (always as its normalized value, so `off`/`""` become an explicit empty-string arg) so `Restart` relaunches with the same setting. Also changes the printed startup URL's path to match.
- The `--foreground` flag is **internal** for direct host usage — it tells the re-exec'd child to run the server directly. It is exposed and documented in the Docker section of MANUAL.md because containers require foreground operation (Docker manages the process lifecycle; the main process must not exit).
- Passkey (WebAuthn) settings have **no per-invocation `serve` flags** — like `--app-name`/`--app-description`/the backup params, they are read straight from `defaults.json` (`defs.WebAuthn`, `defs.WebAuthnRPID`, `defs.WebAuthnRPOrigin`) and passed through to `server.Start()`; use `idtrack default --webauthn ...` to change them.

### `idtrack stop`

Reads `~/.idtrack/idtrack.pid`, sends `SIGTERM`, removes the PID file.

### `idtrack user <subcommand> [--database path]`

All `user` subcommands accept an optional `--database path` flag. Actions are positional subcommands (not flags):

- `idtrack user list` — tabular output: USERNAME, DISPLAY NAME, ADMIN, LAST LOGIN.
- `idtrack user add username:password [--name text] [--admin true|false]` — display name defaults to username; admin defaults to false; upserts on existing username.
- `idtrack user update username [--name text] [--password text] [--admin true|false]` — only updates fields explicitly provided; user must already exist; `--admin` validated as `"true"` or `"false"`.
- `idtrack user delete username` — hard-deletes the row; does not cascade to issues/comments.
- `idtrack user passkeys username list` — tabular output: ID, NAME, LAST USED for that user's registered passkeys.
- `idtrack user passkeys username revoke credential-id` — deletes one passkey. This is the admin escape hatch for a user who has lost their device and can't reach Settings themselves; it calls the same `db.DeleteCredential(owner, id)` the self-service API uses, just with an admin-supplied username instead of the caller's own.
- `--admin` is a convenience alias for team membership: `--admin true` adds the reserved `admin` team, `--admin false` removes it. See `idtrack teams` and "Team-based access control" below.

### `idtrack teams <subcommand> [--database path]`

Manage teams, which partition project/issue visibility (see "Team-based access control" in Important Implementation Decisions). Positional subcommands:

- `idtrack teams list` — tabular output: NAME, DESCRIPTION.
- `idtrack teams add name [--description text]` — creates a team. Errors if the name is reserved (`admin`/`any`) or already exists.
- `idtrack teams update name [--name new-name] [--description text]` — renames and/or redescribes a team; renaming cascades to every user/project/issue that references the old name (`db.UpdateTeam`, single transaction). Reserved teams cannot be renamed, but their description can still be updated.
- `idtrack teams delete name` — errors if the team is reserved, or still referenced by any user, project, or issue (the error lists how many of each).

### `idtrack define <subcommand> [--database path]`

- `idtrack define project name` — creates a new project (idempotent — uses `INSERT OR IGNORE`).
- `idtrack define component project-name component-name` — adds a component to an existing project. Errors if the project does not exist. Idempotent (`INSERT OR IGNORE`).

### `idtrack delete <subcommand> [--database path]`

- `idtrack delete project name` — deletes the project and all its components. Errors (with issue list) if any issues reference that project.
- `idtrack delete component project-name component-name` — deletes a single component. Errors (with issue list) if any issues reference that project+component pair.

### `idtrack ingest <file> [file...] [options]`

Bulk-creates one issue per input file, for importing an existing corpus of bug reports (`commands/ingest.go`, parsing/inference logic in `commands/ingest_parse.go`). All files are parsed and validated before any database write; every issue/comment insert for the whole batch runs inside a single `*sql.Tx` (via the `db.Querier` interface — see below), so a failure partway through the batch leaves the database completely unchanged.

- `--author name` and `--default-owner name` (both required) — must be existing users; become every created issue's reporter and assignee respectively.
- `--default-project name` and `--default-component name` (both required) — must be an existing project/component pair; used when a file's project/component can't be confidently inferred.
- `--default-status open|resolved` (default `open`) and `--default-priority High|Medium|Low` (default `Medium`) — used when a file has no explicit status/priority signal.
- `--test` — prints a per-file report (title, project/component/status/priority each tagged with its source and, for inferred fields, a confidence score) instead of writing to the database. Intended for validating the heuristics against real input before a live run.
- **Title:** the first line if it's a markdown `#` heading, else the first sentence of the text.
- **Description/comments:** the file is split on section boundaries — markdown `##` headers, `**Bold Label:**`/`***Bold Label:***` lines, or plain `Label:` lines (ignored inside fenced code blocks). An explicitly labeled "Description" section is preferred as the issue description over free text before the first boundary (common in the corpus this was built against, where metadata like Severity precedes the real description); every other section becomes a comment, in file order.
- **Status/priority:** detected from a "Resolution"/"Status" section (resolved/fixed/closed) or a "Severity"/"Risk" section (High/Critical, Medium, Low/Minor); falls back to the `--default-*` flags when no signal is found.
- **Project/component:** weighted keyword scoring (`inferProjectComponent` in `commands/ingest_parse.go`) of every known (project, component) pair against the file name, title, and body text — file name matches weigh most, then title, then capped body-occurrence count. Falls back to the defaults independently for project and component when no pair scores above threshold.
- `.md` files are stored with `format = "markdown"`; anything else is stored as `"text"`.

## HTTP API

Authenticated endpoints require a valid session token delivered as an `HttpOnly; Secure; SameSite=Strict` cookie named `idtrack_session`, or via `Authorization: Bearer <token>`. The `auth` middleware validates the token against the in-memory `sessionStore` on every request and stores the `*db.User` in the request context. Sessions are created by `POST /api/login` and `POST /api/onboarding`; deleted by `POST /api/logout`. JSON-body endpoints additionally require `Content-Type: application/json` (enforced by the `requireJSON` middleware).

For full user-facing documentation of every endpoint — request/response JSON shapes, error conditions, pagination, team-visibility rules — intended for writing a new client, see [docs/API.md](docs/API.md). The table below is a quick-reference summary only.

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
| GET | `/api/teams` | yes | no |
| POST | `/api/teams` | yes | **yes** |
| PUT | `/api/teams/{name}` | yes | **yes** |
| DELETE | `/api/teams/{name}` | yes | **yes** |
| GET | `/api/projects` | yes | no |
| POST | `/api/projects` | yes | **yes** |
| PUT | `/api/projects/{project}/teams` | yes | **yes** |
| POST | `/api/projects/{project}/components` | yes | **yes** |
| DELETE | `/api/projects/{project}` | yes | **yes** |
| DELETE | `/api/projects/{project}/components/{component}` | yes | **yes** |
| POST | `/api/render` | yes | no |
| GET | `/api/issues` | yes | no |
| GET | `/api/issues/changes` | yes | no |
| POST | `/api/issues` | yes | no |
| GET | `/api/issues/{id}` | yes | no |
| PUT | `/api/issues/{id}` | yes | reporter/assignee/admin |
| DELETE | `/api/issues/{id}` | yes | reporter/assignee/admin |
| POST | `/api/issues/{id}/comments` | yes | no |
| DELETE | `/api/issues/{id}/comments/{cid}` | yes | **yes** |
| POST | `/api/issues/{id}/attachments` | yes | no |
| POST | `/api/issues/{id}/comments/{cid}/attachments` | yes | no |
| GET | `/api/issues/{id}/attachments` | yes | no |
| GET | `/api/attachments/{aid}` | yes | no |
| GET | `/api/attachments/{aid}/thumbnail` | yes | no |
| DELETE | `/api/attachments/{aid}` | yes | uploader/admin |
| POST | `/api/webauthn/login/begin` † | no | no |
| POST | `/api/webauthn/login/finish` † | no | no |
| POST | `/api/webauthn/register/begin` † | yes | no |
| POST | `/api/webauthn/register/finish` † | yes | no |
| GET | `/api/webauthn/credentials` † | yes | no |
| DELETE | `/api/webauthn/credentials/{id}` † | yes | no (self-service only) |

† Only registered on the mux at all when `webauthn_enabled` is true (see
`idtrack default --webauthn` below) — on an instance where the feature is
off, these routes don't exist rather than returning some "disabled" error.

### Status response (`GET /api/status`)

Always returns `idle_timeout` (seconds, 0 = disabled) and `webauthn_enabled` (whether this instance has passkey login turned on — see "Passkey (WebAuthn) login" below). When no users exist in the database, also returns `onboarding: true` and a one-time UUID `token`:

```json
{ "onboarding": false, "idle_timeout": 1800, "webauthn_enabled": false }
{ "onboarding": true,  "idle_timeout": 0, "webauthn_enabled": false, "token": "<uuid>" }
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
_webauthnEnabled  // bool from /api/status — whether THIS server instance has passkey login on at all
_usePasskeys      // bool — client-side "Use passkeys" preference; mirrors idtrack_prefs, default true
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
- **New Issue** form: a "Project" dropdown must be selected first; selecting it enables a cascaded "Component" dropdown. Both are required — the form will not submit without a valid project and component. A "Format" dropdown (`#ni-format`) selects `text`/`markdown`/`html` for the description.
- **Issue Detail** panel: Project and Component are editable `<select>` elements. Changing the Project resets the Component to "Choose…" and refills the component dropdown. Both are required to save. A "Format" dropdown (`#detail-format`) is editable the same way; changing it re-renders the description/comment preview per `renderFormatted()`'s server-side HTML (fetched with the issue).
- `populateProjectDropdowns()` fills both `ni-project` and `detail-project` from `_projectData`.
- `populateComponentDropdown(selectId, projectName, selected)` cascades from a selected project.

### Admin UI

- **Delete Issue** button appears in the detail panel header only when `_currentUser.is_admin` is true. Requires a `confirm()` dialog before calling `DELETE /api/issues/{id}`.
- **Trash icon** (🗑) appears on each comment only for admins. Requires a `confirm()` dialog before calling `DELETE /api/issues/{id}/comments/{cid}`.
- Hamburger menu shows three additional admin-only items: **Edit Users…**, **Edit Teams…**, and **Edit Projects…**.
- **Edit Users…** opens `manage-users-overlay`, which lists all users and provides add/edit/delete in a single place. See "Overlay navigation pattern" below. The add/edit user forms include a team-chip picker (`renderTeamChips`/`addTeamChip`, autocompleting against `#team-names-dl`) instead of a plain admin toggle.
- **Edit Teams…** opens `mt-list-overlay` → `mt-detail-overlay` (`openManageTeams()`/`openTeamDetail()`), following the same list/detail pattern as Edit Projects: the list screen shows all teams, clicking one opens the detail screen to rename/redescribe/delete it, and **+ New Team** opens the detail screen in create mode.
- **Edit Projects…** opens a two-screen overlay (`ep-list-overlay` → `ep-detail-overlay`). The list screen shows all projects; clicking one opens the detail screen where components can be added/deleted inline, the project's team visibility can be edited via a team-chip picker (`epSaveTeams()`), and the project can be deleted. A **+ New Project** button on the list screen opens the detail screen in new-project mode (name as a text input, components staged before creation). Both screens handle duplicate name checks case-insensitively.
- Non-admin users never see these controls. The server enforces admin on all mutate endpoints (returns 403 Forbidden).

### Status-change dialogs

Changing an issue's status triggers a dialog before the save completes:

- **Open → Resolved**: optional dialog with **Fixed Version** (text) and **Comment** (textarea). If either is filled, a comment is posted atomically with the status update: `Fixed in <version>\n\n<comment>` (parts omitted when empty). An **Assignee** is required before this transition is allowed — `saveIssueChanges()` blocks with an error if the field is empty.
- **Resolved → Open**: required dialog with a **Reason** textarea. The comment is mandatory; the dialog will not confirm until it is non-empty. The reason is posted as a comment atomically with the status change.
- **Any → Duplicate** (`#duplicate-overlay`): required dialog capturing exactly one target issue ID (`#dup-id-input`). The server auto-posts *"Duplicate of issue #N"* on transition. `dependent_issues` is stored as a comma-separated string in SQLite and parsed to `[]int64` in Go.
- **Any → Blocked** (`#blocked-overlay`): required dialog capturing one or more blocking issue IDs (chip list with add/remove) plus an editable comment textarea. When the dialog opens it pre-populates `_pendingBlockedIds` from `_dependentIssues` (any IDs already entered inline) and seeds the textarea with *"Blocked by issues #N, #M…\n\n"* so the user sees the auto-generated prefix and can append to it. When chip IDs are added/removed, `renderBlockedDialogList()` updates the textarea prefix in-place, preserving any user-typed text after the `\n\n` separator. On confirm, the comment is posted client-side (same as Resolve/Reopen) — the server does not auto-post a comment for Blocked transitions. After confirming, the server stores the IDs in `dependent_issues`.
- **Blocked → Open**: no dialog. The server validates that every issue in `dependent_issues` has `status = 'Resolved'` and returns HTTP 409 if any are still open; the error message is shown in `#detail-error`.

State: `_originalStatus` is set when an issue loads and updated after each successful save. `_pendingStatusData` captures the form fields while a dialog is open. `_dependentIssues` mirrors the `dependent_issues` field of the current issue and is kept in sync after every save. `_pendingBlockedIds` is a staging list built inside the Blocked dialog before the user confirms.

**Inline dependent issues section** (`#dependent-issues-section`): shown automatically when `status` is Blocked or Duplicate. For Duplicate it shows a read-only chip; for Blocked it shows an editable chip list. Any editor can add blocking issues inline via `#dep-add-input` / `addBlockingIssue()`; only admins see the × remove button (`removeBlockingIssue(id)`). `renderDependentIssues(status, canEdit)` rebuilds this section — called from `selectIssue()`, `doSaveIssue()`, `onDetailStatusChange()`, and the add/remove helpers. `onDetailStatusChange()` replaces the old `markDetailDirty()` onchange handler on `#detail-status` so it can also update the section visibility and clear `_dependentIssues` when moving away from Blocked/Duplicate.

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

**Blocked and Duplicate statuses extend the issue state machine.** `dependent_issues TEXT NOT NULL DEFAULT ''` is a comma-separated list of issue IDs stored in SQLite. `parseDependentIssues` / `formatDependentIssues` in `db/issues.go` convert between `[]int64` and the stored string. The `issueColumns` constant and `scanIssue` helper in `db/issues.go` centralise all column scanning so adding the new field required only one change to the SELECT list. `buildWhereClause` in `db/issues.go` handles `blocked` and `duplicate` filter tokens the same way it handles `open` and `resolved`. The `UpdateIssue` CASE expression treats Duplicate like Resolved (sets `resolved_at` when first closing) and Blocked like Open (clears `resolved_at`).

**Server-side validation for Blocked/Duplicate transitions.** `handleUpdateIssue` in `server/issues.go` enforces: Duplicate → exactly one `dependent_issues` ID that exists and is not self; Blocked → at least one ID, all existing, none self; non-admins cannot *remove* IDs from an already-Blocked issue's list (they can only add); Blocked→Open requires every dependent issue to have `status = 'Resolved'` (HTTP 409 otherwise). On transition TO Duplicate the server auto-posts *"Duplicate of issue #N"* via `db.CreateComment` — errors from that call are ignored so the status change is never rolled back by a comment failure. Blocked transitions do NOT auto-post server-side; the client posts the comment (the seeded "Blocked by issues…" text plus any user additions) after a successful PUT. For Open/Resolved the handler clears `dependent_issues` automatically regardless of what the client sends.

**Service managers (launchd, systemd) also require `--foreground`.** Both `install-service-macos.sh` and `install-service-linux.sh` embed `--foreground` in the generated plist/unit file. The reasoning is identical to the Docker case: without it, `idtrack serve` forks a background child and exits, and the service manager concludes the service failed and immediately tries to restart it in a tight loop. With `--foreground`, the process blocks in the HTTP server loop and the service manager tracks it correctly. The macOS script generates a plist with `RunAtLoad=true` and `KeepAlive=true`; the Linux script generates a unit file with `Type=simple` and `Restart=on-failure`. Both install to the appropriate directory (LaunchAgent `~/Library/LaunchAgents/` or LaunchDaemon `/Library/LaunchDaemons/`; systemd `/etc/systemd/system/` or `~/.config/systemd/user/`) and accept the full set of idtrack server options.

**Docker containers require `--foreground` to stay alive.** `idtrack serve` without `--foreground` re-execs a background child and exits. In a container that exit kills the container because PID 1 has ended. The `Dockerfile` CMD and `tools/start-container.sh` always pass `--foreground`. The SQLite database and backup files are stored outside the container via a host bind mount at `/data`. The `tools/build-container.sh` script reads `tools/buildver.txt` and passes `--build-arg BUILD_VERSION` so the image's version output matches the tag. The binary is built with `CGO_ENABLED=0` inside the Docker builder stage (safe because `modernc.org/sqlite` is pure Go), producing a fully static binary that runs in the Alpine runtime image without any C runtime dependency.

**External TLS cert/key replaces the embedded self-signed certificate.** When `server_cert` and `server_key` are set in `defaults.json` (or passed directly to `idtrack serve`), `server.Start()` reads the PEM files from disk via `os.ReadFile` instead of from the embedded `embed.FS`. Both must be set together — the server will fail to start if only one is present (the cert and key must form a matching pair for `tls.X509KeyPair`). `idtrack default --server-cert` validates that the file exists and resolves it to an absolute path before saving, so a relative path at save time won't silently break after a working-directory change. Use `off` as the value to clear either setting and revert to the built-in certificate.

**`--insecure`/`-k` bypasses TLS entirely for reverse-proxy deployments.** `server.Start()` opens a plain `net.Listen("tcp", addr)` and, when `insecure` is true, serves directly off that listener — the cert/key-loading branch (embedded, defaults-configured, or flag-overridden) is skipped completely, so no cert or key file needs to exist on disk at all in this mode. When `insecure` is false (the default), the same raw listener is instead wrapped with `tls.NewListener` after loading a cert/key pair exactly as before. This is for operators who already terminate TLS at a front-end reverse proxy (e.g. nginx) and want idtrack to speak plain HTTP on a private network or loopback interface to that proxy. Session cookies still set `Secure` unconditionally (`server/sessions.go`, `server/auth_handlers.go`) because the intended topology keeps HTTPS on the browser-facing hop — the proxy presents the real certificate; only the proxy-to-idtrack hop drops TLS. Operators who instead expose `--insecure` mode directly to browsers get no session cookie (browsers refuse to send a `Secure` cookie over plain HTTP) and cannot log in; this is intentional, not a bug — insecure mode is only correct when something in front of idtrack is providing the HTTPS layer. `commands.Serve` computes the printed URL's scheme (`http://` vs `https://`) from the same flag. Follows the same `defaults` struct → CLI flag → `server.Start()` parameter → `srv` field pattern as every other server-wide setting (see "`server.Start()` signature pattern" below), except it is a bool rather than a string/duration/count, parsed with an optional trailing `true|false`/`on|off` value (default `true` when the flag is present with no value) rather than requiring one.

**Configurable base path (`--base-path`) mounts the whole app under a URL prefix, for reverse-proxy deployments that don't own the whole domain.** Motivating case: an operator running idtrack and another app behind the same nginx host, wanting idtrack reachable at `https://host/idtrack/*` via a single `location /idtrack { proxy_pass http://127.0.0.1:PORT; }` block that forwards the full request URI unmodified (no path-stripping). Before this feature every idtrack route was a hardcoded absolute root path (`/idtrack`, `/assets/idtrack/idtrack.css`, `/api/*`, `/manual`), which only worked when idtrack owned the entire origin — mounting it under a reverse-proxy sub-path caused an infinite redirect loop (the proxy stripped the prefix, idtrack's root handler redirected back to the very path the proxy had just stripped).

- `server.srv.basePath` holds the configured prefix (`""` = origin root, today's original behavior). `server.srv.appPath()` (`server/server.go`) returns where the SPA page itself is served: `"/idtrack"` when `basePath` is unset (preserving the original hardcoded path exactly), or `basePath` itself when set — deliberately *not* `basePath+"/idtrack"`, so that setting `--base-path /idtrack` puts the page at exactly `/idtrack`, not the doubled `/idtrack/idtrack`.
- In `server.Start()`, a `route(pattern string) string` closure splices `basePath` between the method and path of every `"METHOD /path"` mux pattern (e.g. `route("GET /api/version")` → `"GET "+basePath+"/api/version"`) — every `/api/*`, `/manual`, and the two `/assets/idtrack/*` routes are registered through it. The page route is registered directly at `"GET "+s.appPath()` (bypassing `route`, since `appPath()` already equals `basePath` when one is set — passing it through `route` too would double-prefix it). `"GET /"` (`serveRoot`) is never prefixed; it exists solely so a bare hit on the origin root 302s to `s.appPath()`, and remains reachable regardless of where the app is actually mounted.
- The embedded `resources/idtrack.html` hardcodes its CSS/JS/manual links as root-absolute strings (`href="/assets/idtrack/idtrack.css"`, `src="/assets/idtrack/idtrack.js"`, `href="/manual"`) because the base path isn't known until the server starts, long after the HTML was compiled into the binary. `serveHTML` (`server/static.go`) does a targeted `strings.Replace` on those three literal substrings when `basePath != ""`, splicing the prefix in; when `basePath == ""` this is skipped entirely and the response is byte-for-byte identical to before the feature existed.
- The frontend (`resources/idtrack.js`) declares `const BASE_PATH = '';` as a source-level sentinel near the top of the file. Every API call in the file is written as `BASE_PATH + '/api/...'` rather than a bare `'/api/...'` literal — including `apiFetch`, the single choke point all of `apiGet`/`apiPost`/`apiPut`/`apiDelete` funnel through (`fetch(BASE_PATH + url, options)`), which transparently covers roughly two dozen call sites, plus the handful of endpoints that call `fetch()` directly (login, logout, onboarding, status, version, teams) which were each updated individually. `serveJS` (`server/static.go`) does a `bytes.Replace` of the literal source text `const BASE_PATH = '';` → `const BASE_PATH = '<basePath>';` **before** calling `Minify` — Minify only guarantees preserving existing string-literal tokens byte-for-byte, not that arbitrary source text passed to it still reads the same way, so the substitution has to happen against the raw embedded source.
- `validateBasePath()` (`commands/common.go`) is shared by `commands.Default` and `commands.Serve`: `""`/`"off"` both normalize to `""` (disabled); otherwise the value must start with `/` and must not end with `/` or be exactly `/`. `commands.Serve` computes the same `appPath` logic locally (duplicated, not imported, to keep `commands` and `server` decoupled) to print the correct startup URL, and forwards the normalized value through `passArgs` so `idtrack restart` preserves it.
- Session cookie `Path` is deliberately left at `/` regardless of `basePath` (not narrowed to scope the cookie under the base path) — narrowing it would require the exact same `Path` value on both the `Set-Cookie` in `sessionCookie()` and the clearing cookie in `handleLogout`, and getting that pair out of sync would silently break logout. The cookie name (`idtrack_session`) is already unique, so the broader default scope costs nothing in a shared-domain deployment.

**`off` is a synonym for the zero/disabled value on duration, count, and size flags.** `--idle-timeout off`, `--backup-interval off`, `--backup-count off`, `--backup-age off`, and `--backup-size off` all behave identically to `0`. This makes the intent explicit in shell scripts or documentation where the word "off" reads more clearly than the number zero.

**Password hashing is server-side (bcrypt).** The browser sends the plaintext password over TLS to `POST /api/login`. The server hashes it with `bcrypt.GenerateFromPassword` (default cost) and compares against the stored bcrypt hash with `bcrypt.CompareHashAndPassword`. The DB stores the bcrypt hash string (begins with `$2a$`). Legacy SHA-256 hashes (64 lowercase hex chars, from the old client-side scheme) are detected by format in `db.IsLegacyHash` and verified via a constant-time SHA-256 comparison in `db.VerifyPassword`; they are transparently upgraded to bcrypt on next successful login via `db.UpgradePasswordHash`.

**SQLite with `MaxOpenConns(1)`.** SQLite doesn't support concurrent writers. Setting max open connections to 1 serializes all access and avoids `SQLITE_BUSY` errors.

**No comment–issue foreign key constraint in SQLite.** SQLite doesn't enforce foreign keys by default (requires `PRAGMA foreign_keys = ON`). The code instead manually deletes associated comments before deleting an issue in `db.DeleteIssue()`. The same manual cleanup is not needed for `DeleteUser` — orphaned reporter/assignee strings are acceptable.

**`serve` re-execs itself rather than forking.** Go doesn't have a clean `fork()` equivalent. The approach is: `commands.Serve` validates args, calls `launchBackground` which spawns `exec.Command(os.Executable(), "serve", "--foreground", ...)`, writes the child PID, and exits. The child runs `commands.Serve` again with `--foreground` set and blocks in the HTTP server loop. `Setsid: true` detaches the child from the parent's process group so it survives terminal close. All of this logic lives in `commands/serve.go`.

**`UpdateUser` requires `*bool` for `isAdmin`.** An empty string signals "not specified" for string fields, but a `bool` has no natural sentinel. Using `*bool` (nil = leave unchanged, non-nil = set) keeps the logic explicit and avoids accidentally clearing admin status when only updating a display name.

**Schema migrations are additive only.** New columns are always added with `DEFAULT` values via `addColumnIfMissing`. Existing data is never altered. This keeps the migration path trivially safe.

**Static assets are embedded.** The TLS cert/key and all web assets are compiled into the binary with `//go:embed resources`. Deployment is a single file copy.

**`main.go` owns the two things that cannot move.** `BuildVersion` and `BuildTime` must live in `package main` because the build script injects them via `-ldflags "-X main.BuildVersion=..."`. The embedded filesystem must also stay in `main` because `//go:embed resources` requires the `resources/` directory to be a sibling of the source file. `main()` copies the build vars into `commands.BuildVersion` / `commands.BuildTime` before dispatching, and passes `fs.FS(embedded)` directly to `commands.Serve`. Everything else lives in the `commands` package.

**Onboarding uses a one-time in-memory UUID.** When `GET /api/status` detects an empty users table it generates a UUID, stores it on the `srv` struct behind a `sync.Mutex`, and returns it in the response. `POST /api/onboarding` validates `Authorization: Basic base64("onboarding:<uuid>")`, creates the first admin user, then clears the token. Because the token lives only in process memory it is lost on server restart — in that case the client simply receives a fresh UUID on the next status probe.

**`server.Start()` signature pattern for server-wide config.** `idleTimeout`, `appName`, `appDescription`, and the backup params (`dbPath`, `backupInterval`, `backupCount`, `backupAge`, `backupSize`) are all examples of the same pattern: add the field to the `defaults` struct in `commands/common.go` (with `omitempty`), add the flag to `commands.Default`, parse and pass the value through `server.Start()` from `commands.Serve`, and store it on the `srv` struct. Duration-type flags are stored as strings in `defaults.json` and parsed to `time.Duration` in `commands.Serve`; size-type flags are stored as strings in `defaults.json` and parsed to `int64` bytes by `parseBackupSize()` in `commands.Serve`. Follow this pattern for any future server-wide configuration values.

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

**`GET /api/issues/changes?since=<RFC3339>`** returns `{ issues: [...] }` — issues whose `updated_at` is strictly after the given timestamp, ordered by `updated_at ASC`, restricted only by the caller's team visibility. Registered before the `/{id}` wildcard pattern in `server/server.go` so the literal path `/changes` takes priority.

**`/api/issues/changes` is deliberately NOT filtered by status/priority/project/search, unlike `/api/issues`.** Filtering by an issue's *current* state can only tell a poller about issues that *still* match a filter — it can never report an issue that just stopped matching (e.g. an Open→Resolved transition no longer matches `status=open`, so a status-filtered query silently omits it forever). An earlier version of this endpoint did apply those filters server-side and broke exactly that case: issues leaving a filtered view just went stale in the UI instead of being removed. The fix moved relevance filtering to the client: `pollForChanges()` in `resources/idtrack.js` fetches the full team-visible change set and calls `matchesCurrentFilters(issue)` (the same helper `doSaveIssue()` uses after the user's own edits) on each one — a match already in `_issueWindow` updates in place, a match not yet in the window counts toward the refresh-hint toast, a non-match still in the window is removed immediately (no need to wait for "Refresh" — the removal is already fully applied), and a non-match not in the window is ignored entirely (this is what keeps unrelated database activity elsewhere from popping the toast).

**The polling cursor (`_lastSeenAt`) is seeded to "now," not the loaded window's max `updated_at`.** `loadIssueWindow()` sets it via `_nowRFC3339()` (current time, formatted without fractional seconds to stay lexicographically comparable to the server's `time.RFC3339` timestamps) rather than the maximum `updated_at` among the freshly-fetched page. Deriving it from the loaded page was a real bug: that page is only the current filter/sort's first N rows, so its max timestamp is frequently much older than "now" (e.g. sorted by a column other than Updated, or excluding a recently-touched issue via a status filter), which caused `pollForChanges()` to report unrelated, already-existing issues as fresh "external changes" on the very first poll after every load. `_updateLastSeenAt()` still ratchets the cursor forward (never backward) as pages load, as a safety net against client/server clock skew.

**`db.CountIssues` and `db.ListIssues` share a WHERE clause builder.** `buildWhereClause(status, priority, search, project string, userTeams []string)` in `db/issues.go` returns a `(clause string, args []interface{})` pair that is reused by both functions, ensuring the count and the data query always agree. The `userTeams` argument folds in the team-visibility filter (see "Team-based access control" below); pass `nil` to skip it. `ORDER BY` is constructed from a lookup table of hardcoded `"column ASC/DESC"` literals (keyed by column name) to prevent SQL injection via the `sort` and `order` query parameters.

**Backup strategy: filesystem copy with RWMutex quiescing.** When `backupInterval > 0`, `server/backup.go` manages all backup logic. `startBackups()` is called in `Start()` before `httpSrv.Serve` (so the first backup is written before any requests are served). It creates an `idtrack-backups/` directory next to the database file, writes an initial backup synchronously, then launches a goroutine that fires `doBackup()` every `backupInterval`. `doBackup` takes `s.backupMu.Lock()` (write lock) to quiesce the server, calls `copyFile` (io.Copy + fsync), releases the lock, then runs `ageBackups` in a separate goroutine. Every HTTP request holds `s.backupMu.RLock()` via the `quiesce` middleware, which wraps the entire mux. The RWMutex ensures: in-flight requests finish before the backup copy starts; new requests block (briefly) while the copy is in progress; no 503 is returned to clients. `ageBackups` runs three thinning strategies in order: (1) `sizeBackups` — Time Machine-style density thinning to a total byte limit; (2) count pruning — delete oldest beyond the count limit; (3) age pruning — delete files whose embedded timestamp is before `now − backupAge`. Backup filenames embed the UTC timestamp (`idtrack-20060102T150405.db`) so alphabetical and chronological order coincide and the age can be recovered from the name without touching the filesystem mtime.

**Size-based thinning uses four deletion phases.** `sizeBackups()` in `server/backup.go` categorises all backup files into hourly buckets (ages 1–23 h) and daily buckets (ages ≥ 24 h). When total size exceeds `s.backupSize` it deletes in order: (1) extras within hourly buckets, newest bucket first; (2) extras within daily buckets, newest day first; (3) the hourly-23 keeper if daily-1 already exists (pre-emptive aging); (4) the oldest daily keeper. Last-hour files (age < 1 h) are never touched. `parseBackupSize()` in `commands/common.go` converts human-readable strings (`500mb`, `.5gb`, `1tb`) to `int64` bytes; it is shared by `commands/defaults.go` (validation) and `commands/serve.go` (parsing for `server.Start()`).

**HTTP response compression uses a buffering middleware with a 1,400-byte threshold.** `gzipHandler` in `server/compress.go` wraps the outermost layer of the middleware chain (outside `secureHeaders`, `limitBody`, and `quiesce`). It checks `Accept-Encoding: gzip`; if absent, it passes through unchanged. If present, it wraps the `ResponseWriter` with a `bufferingWriter` that captures the response body. After the handler returns, `bufferingWriter.flush()` checks the body size: if ≥ 1,400 bytes (one Ethernet MTU payload), it compresses with `compress/gzip` (stdlib, no new dependencies), sets `Content-Encoding: gzip`, removes `Content-Length`, and writes the compressed bytes; smaller responses are written uncompressed. Every response gets `Vary: Accept-Encoding` regardless of whether compression was applied. The 1,400-byte threshold is chosen because a payload that fits in a single TCP segment cannot arrive faster via compression — any savings require avoiding a second packet. In practice this threshold compresses all static assets (JS ≈ 80% reduction, CSS ≈ 75%), all multi-issue API responses, and large comment threads, while skipping login/status/polling responses where CPU overhead would exceed any gain.

**Team-based access control uses a four-way visibility rule.** `db.IssueMatchesUserTeams(issueTeams, userTeams)` and its project counterpart `ProjectMatchesUserTeams` (`db/teams.go`) implement: (1) a user with the reserved `admin` team sees everything; (2) a user with the reserved `any` team sees everything; (3) an issue/project tagged `any` is visible to everyone; (4) otherwise, visibility requires a non-empty intersection between the user's teams and the issue's/project's teams. `handleListIssues` passes `currentUser(r).Teams` through to `db.ListIssues`/`CountIssues`, which fold it into the SQL `WHERE` clause built by `buildWhereClause`; `handleListProjects` filters client-side with `ProjectMatchesUserTeams` after fetching all projects. Passing `nil` for `userTeams` skips filtering entirely (used where the caller already knows the user can see everything). `users.is_admin` is now *derived* from team membership (`ContainsTeam(teams, TeamAdmin)`) rather than being authoritative, but is still stored and kept in sync for code that queries it directly. New users/projects/issues default to team `any` (`FormatTeams` returns `"any"` for a nil/empty slice), so the pre-teams "everyone sees everything" behavior is the default unless an operator explicitly restricts a project.

**Markdown/HTML issue formatting renders server-side, once, per response.** The `format` column on `issues` (`text`/`markdown`/`html`) is set via the `POST /api/issues` and `PUT /api/issues/{id}` request bodies. `renderFormatted(format, text)` in `server/render.go` converts `markdown` to HTML using a package-level, concurrency-safe `goldmark.New()` instance; `html` passes through verbatim (an authenticated user choosing that format is an explicit, trusted opt-in — the same trust boundary as every other write in this single-tenant tool); `text` returns `""` so the frontend keeps doing its own escaping. `handleGetIssue` and `handleUpdateIssue` populate the transient `Issue.DescriptionHTML`/`Comment.BodyHTML` fields (`json:"...,omitempty"`, never persisted) so the client can render without a second round-trip.

**`idtrack ingest` atomicity is built on a `db.Querier` interface, not ad hoc transaction handling.** `db.CreateIssue`, `GetIssue`, `UpdateIssue` (`db/issues.go`), and `CreateComment` (`db/comments.go`) take a `Querier` (`Exec`/`QueryRow`/`Query`) instead of `*sql.DB`, satisfied by both `*sql.DB` and `*sql.Tx`. Every existing HTTP handler call site is unaffected since it already passes `*sql.DB`. `commands.runIngestTx` begins one `*sql.Tx` for the whole file batch, calls these functions with it, and rolls back on the first error, so a failure on file N leaves zero issues from files 1..N-1 committed either. **Reuse this pattern** — widen the relevant `db.*` function signatures to `Querier` — for any future feature that needs multiple DB writes to succeed or fail together, rather than duplicating INSERT/UPDATE SQL inline.

**Login attempts are rate-limited per client IP.** `server/ratelimit.go`'s `rateLimiter` (in-memory, sliding one-minute window, 10 failed attempts before lockout) is checked in `handleLogin` before password verification; a locked-out IP gets `429 Too Many Requests` with `Retry-After: 60`. `recordFailure`/`clear` are only called after the credential check, so a lockout counts failed attempts, not successful or as-yet-unattempted logins.

**Passkey (WebAuthn) login is a passwordless *alternative*, not a second factor, gated by an explicit server-side switch independent of its own configuration.** Implemented with `github.com/go-webauthn/webauthn` — the first genuine departure from idtrack's otherwise minimal-dependency posture (goldmark, modernc.org/sqlite, golang.org/x/crypto, google/uuid). Three `defaults.json` fields control it: `webauthn_enabled` (the on/off switch an operator flips — when false, `server.Start()` never registers a single `/api/webauthn/*` route, so a client hits the mux's ordinary not-found/method-not-allowed response, not a bespoke "feature disabled" error) and `webauthn_rp_id`/`webauthn_rp_origin` (the Relying Party identity, required together whenever `webauthn_enabled` is true — `server.Start()` fails fast at startup otherwise). RP ID/origin are deliberately **explicit operator configuration**, never derived from the request `Host` header — same reasoning as `--server-cert`/`--base-path` being explicit rather than auto-detected: it avoids trusting a spoofable header for a security-sensitive value and needs no per-request dynamic-RPID library support. `GET /api/status`'s `webauthn_enabled` field is the single source of truth the frontend gates every bit of passkey UI on (the login button, the Settings "Passkeys" section) — a client never shows the option unless the instance has actually opted in, regardless of what the browser supports.

Credentials live in their own `webauthn_credentials` table (`db/webauthn.go`), not columns on `users`, since a user has zero-to-many passkeys. Only the fields needed to verify a future assertion and let a user manage their own list are persisted (public key, clone-detection sign counter, a label, timestamps) — the library's own richer `Credential` struct carries per-tenant attestation/trust metadata (AAGUID, attestation type/format) that doesn't matter for a single-RP self-hosted tool and is deliberately not stored; see `db.WebAuthnCredential`'s doc comment. The WebAuthn "user handle" (`webauthnUser.WebAuthnID()` in `server/webauthn.go`) is simply the username's raw bytes rather than a separately-generated random opaque ID, because idtrack usernames are already the stable, unique, un-renameable per-user key everywhere else in this codebase — this also means the discoverable-login user-resolution callback (`webauthnDiscoverableUserHandler`) needs no separate handle-to-user mapping table, just `db.FindUser(string(userHandle))`.

Login uses go-webauthn's **discoverable/passkey flow** (`BeginDiscoverableLogin`/`FinishPasskeyLogin`), and registration requests a resident key (`WithResidentKeyRequirement(ResidentKeyRequirementRequired)`), so the browser's "Sign in with a passkey" prompt never needs a username typed first. A successful assertion produces a normal session exactly like password login does — `finishLogin(w, s, user, keepLoggedIn, event)` in `server/auth_handlers.go` is the shared tail (session create, cookie, `RecordLogin`, standard response body) both `handleLogin` and `handleWebAuthnLoginFinish` call, so there is exactly one place that mints a session from a verified identity.

Because go-webauthn's `Finish*` calls read the raw WebAuthn response JSON directly from the request body, nothing else can ride along in that body on a `finish` call — a chosen passkey label (`register/finish?name=...`), whether to request a 30-day session (`login/finish?...&keep=true`), and which in-flight ceremony this is (`login/finish?ceremony=<id>`, since the caller isn't authenticated yet and can't be looked up by username the way `register/finish` is) all travel as query parameters instead. In-flight ceremony state (a `*webauthn.SessionData` from `Begin*`) lives in `webauthnCeremonyStore` (`server/webauthn.go`), an in-memory, single-use, TTL'd map mirroring `sessionStore`'s shape — registration ceremonies keyed by username (one in flight per user at a time), login ceremonies keyed by a random ceremony ID handed to the client.

A user manages their own passkeys self-service from Settings (list/add/remove); there is no "last factor" guard because password login always remains available. `idtrack user passkeys <username> list|revoke <id>` is the admin escape hatch for a user who has lost their device and can't reach Settings — it calls the same `db.DeleteCredential(owner, id)` the self-service API uses, just with an admin-supplied username.

Finally, a **client-side `usePasskeys` preference** (`resources/idtrack.js`, stored in `idtrack_prefs`, default `true`) lets an individual user opt out of passkey prompts even on an instance where the server has the feature on; a Settings toggle controls it, and turning it off hides the login button and collapses the passkey-management section down to just that toggle. This preference can only ever narrow what the server already allows, never widen it — every passkey UI element checks both `_webauthnEnabled` (server) and `_usePasskeys` (client), with the server flag always taking precedence.

**Image attachments are stored as blobs inside the SQLite file, always converted to PNG, and HEIC is deliberately not supported yet.** Every uploaded image — regardless of source format — is decoded (`image.Decode`, stdlib), re-encoded to PNG, and stored alongside a generated thumbnail (`server/images.go`'s `processUploadedImage`, using `golang.org/x/image/draw` for high-quality scaling) so every stored/served image is byte-identical in format, and the frontend never needs per-format rendering logic. Only PNG and JPEG are accepted as input: HEIC (what modern iPhones capture by default) was evaluated during design and rejected for now because the only Go HEIC decoders (`jdeng/goheif`, `adrium/goheif`) bundle `libde265` via cgo, which would break idtrack's `CGO_ENABLED=0` static Docker build and complicate `./build --all` cross-compilation — the same build-portability reasoning applied elsewhere in this doc (e.g. why `modernc.org/sqlite` was chosen over a cgo SQLite driver). A HEIC upload gets a clear `415 Unsupported Media Type` rather than a silent failure; client-side conversion before upload, or a deliberate cgo/external-`libheif` decision later, remain open options. Acceptance is determined solely by whether the bytes actually decode as a registered `image.Decode` format — a client's declared `Content-Type` header is never trusted. Decoded images whose pixel count exceeds a fixed cap (40 megapixels) are rejected before any resize work, guarding against a decompression-bomb-style upload.

**`attachments` has no database-level foreign key, so both `db.DeleteIssue` and `db.DeleteComment` manually cascade — same reasoning as the existing comment cleanup.** `comment_id` uses `0` (not `NULL`) as the "attached to the description, not a comment" sentinel, since no comment row ever has id `0` (SQLite `AUTOINCREMENT` starts at 1) — this keeps the column a plain `NOT NULL INTEGER` consistent with the rest of the schema rather than introducing a nullable column. `db.DeleteAttachmentsByIssue`/`DeleteAttachmentsByComment` (`db/attachments.go`) are called from `DeleteIssue`/`DeleteComment` respectively before the parent row is removed, exactly mirroring `DeleteIssue`'s existing manual `DELETE FROM comments` cleanup.

**Attachment deletion is admin-or-uploader, not admin-only like comment deletion.** `handleDeleteAttachment` checks `user.IsAdmin || user.Username == attachment.Uploader` — closer to `issueModifier`'s reporter/assignee/admin pattern than to `handleDeleteComment`'s strict admin gate — so a user can remove their own mistaken upload without needing an admin.

**The "list images" endpoint returns metadata only; image bytes are always fetched by UUID from a dedicated route.** `GET /api/issues/{id}/attachments` never embeds thumbnail or full-image bytes in its JSON response — the frontend renders each entry via `<img src=".../api/attachments/{id}/thumbnail">`, letting the browser cache and lazy-load every image independently rather than paying for every thumbnail up front on issues with many attachments. Both `GET /api/attachments/{aid}` (full image) and its `/thumbnail` sibling set `Cache-Control: private, max-age=31536000, immutable`, since an attachment's bytes never change after upload — only a `DELETE` removes the row, which naturally invalidates any cached copy the next time it's requested.

**Attachment body-size and content-type middleware are carved out by path/route, not applied globally.** `limitBody` (`server/middleware.go`) caps every other POST/PUT body at 64 KiB (plenty for JSON) but grants `maxAttachmentBodyBytes` (12 MiB) to any POST whose path ends in `/attachments`, covering both upload routes regardless of `basePath` prefixing. A new `requireMultipart` middleware (mirroring `requireJSON`) wraps just those two routes instead of `requireJSON`, since they carry a multipart file upload, not a JSON body. Follow this pattern — a dedicated middleware/limit pair scoped by path, rather than changing the global default — for any future route whose body shape genuinely differs from the rest of the JSON API.

**Attachment GET/list endpoints require authentication but do not re-check team visibility, matching `handleGetIssue`'s existing behavior.** Team-based filtering (`db.IssueMatchesUserTeams`) is applied at the list-query level (`buildWhereClause`, used by `GET /api/issues`), not on a single-issue `GET /api/issues/{id}` — attachments follow that same precedent rather than introducing a new, inconsistent check.
