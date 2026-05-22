# idtrack — Architectural Reference

This document describes the internal architecture, design decisions, and implementation details of idtrack for developers, administrators, and contributors. It is the engineering companion to the user-facing [MANUAL.md](../resources/MANUAL.md) and the [BACKUPS.md](BACKUPS.md) operations reference.

> **Historical note.** An earlier version of idtrack ran as a plugin on the Ego server, using the Ego `@sql` endpoint for all database operations and relying on Ego's user system for authentication. That version is completely replaced. The current codebase is a standalone Go binary with no dependency on Ego or any other runtime.

---

## Overview

idtrack is a self-hosted issue tracker delivered as a **single statically-linked Go binary**. The binary acts as both the HTTPS web server and the administrator CLI. There are no runtime dependencies beyond the SQLite database file.

Key characteristics:

- **Single binary deployment** — copy the binary to a server, run it. No installer, no runtime, no libraries.
- **HTTPS only** — a self-signed TLS certificate is embedded at compile time; external certificates can be configured.
- **Own user system** — bcrypt-hashed passwords, session-cookie auth, admin roles.
- **No framework** — `net/http` with Go 1.22+ method-prefixed route patterns.
- **SQLite** via `modernc.org/sqlite` (pure Go, no CGO required, no external `.so`).
- **Single-page frontend** — plain HTML/CSS/JavaScript, no build step, no npm.

---

## Repository Layout

```text
idtrack/
├── main.go               # Entry point; injects build vars, dispatches to commands.*
├── go.mod                # module: github.com/tucats/idtrack
├── Dockerfile            # Two-stage build: builder → alpine runtime
├── .dockerignore
├── tools/
│   ├── build             # Native build script (version injection)
│   ├── buildver.txt      # Current version string, e.g. "1.0-34"
│   ├── build-container.sh
│   ├── start-container.sh
│   ├── install-service-macos.sh  # launchd service install/remove
│   └── install-service-linux.sh  # systemd service install/remove
├── commands/             # One exported function per CLI verb
│   ├── common.go         # defaults struct, loadDefaults(), parseBackupSize(), Usage()
│   ├── serve.go          # Serve(), Stop(), Restart(), launchBackground(), pid helpers
│   ├── defaults.go       # Default() — read/write defaults.json; showDefaults()
│   ├── users.go          # User() — list/add/update/delete user accounts
│   ├── projects.go       # Define(), Delete() — project and component management
│   └── version.go        # Version() — print build version and timestamp
├── db/
│   ├── db.go             # Open(), schema init, addColumnIfMissing() migration helper
│   ├── users.go          # User CRUD, RecordLogin, UpdateUser, ListUsers
│   ├── issues.go         # Issue CRUD, buildWhereClause(), CountIssues(), ListIssues()
│   ├── comments.go       # Comment CRUD, DeleteComment
│   └── projects.go       # Project/Component CRUD
├── server/
│   ├── server.go         # srv struct, Start() — route wiring and TLS setup
│   ├── middleware.go     # contextKey, auth(), requireJSON(), currentUser()
│   ├── compress.go       # gzipHandler middleware, bufferingWriter
│   ├── helpers.go        # issueID(), jsonResponse(), jsonError()
│   ├── static.go         # static file handlers, handleManual()
│   ├── sessions.go       # sessionStore — create/lookup/delete session tokens
│   ├── backup.go         # startBackups(), doBackup(), quiesce(), sizeBackups(), ageBackups()
│   ├── auth_handlers.go  # handleVersion, handleStatus, handleOnboarding, handleLogin, handleLogout
│   ├── users.go          # user CRUD handlers
│   ├── projects.go       # project/component CRUD handlers
│   ├── issues.go         # issue CRUD handlers
│   └── comments.go       # handleCreateComment, handleDeleteComment
├── resources/            # Embedded at build time via //go:embed
│   ├── idtrack.html
│   ├── idtrack.css
│   ├── idtrack.js
│   ├── MANUAL.md         # User manual (rendered at /manual as HTML)
│   ├── https-server.crt  # Self-signed TLS certificate
│   └── https-server.key  # TLS private key
└── docs/
    ├── IDTRACK.md        # This file
    └── BACKUPS.md        # Backup strategy reference
```

---

## Build System

### Version injection

The binary version is injected at link time by the `tools/build` script. Two `ldflags` variables are set:

- `main.BuildVersion` — the version string read from `tools/buildver.txt` (format: `MAJOR.MINOR-BUILD`, e.g. `1.0-34`)
- `main.BuildTime` — UTC timestamp at build time (`YYYYMMDDHHmmSS`)

Both default to `"dev"` / `""` when built with plain `go build`. The `-i` flag on the build script increments the `BUILD` number and writes it back before building. Both variables must live in `package main` because `ldflags -X` can only target package-level variables.

### Static embedding

All web assets and TLS credentials are compiled into the binary using `//go:embed resources` in `main.go`. The `embed.FS` is passed to `commands.Serve` and on to `server.Start`. This means deployment is a single file copy — no companion directories needed.

The embedded self-signed TLS certificate is valid for local use. It will trigger browser security warnings; production deployments should configure an externally-signed certificate via `idtrack default --server-cert / --server-key`.

### Docker

The `Dockerfile` is a two-stage build. The builder stage sets `CGO_ENABLED=0` (safe because `modernc.org/sqlite` is pure Go) to produce a fully static binary. The runtime stage is Alpine. The `tools/build-container.sh` script passes `--build-arg BUILD_VERSION` read from `tools/buildver.txt` so the image version matches the tag.

**Containers must use `--foreground`.** Without it, `idtrack serve` re-execs a child and exits, killing the container because PID 1 has ended. The Dockerfile `CMD` and `start-container.sh` always pass `--foreground`.

---

## Process Model

`idtrack serve` (without `--foreground`) works as follows:

1. Validates arguments.
2. Calls `launchBackground()` which spawns `exec.Command(os.Executable(), "serve", "--foreground", ...)` with `Setsid: true` (new session, survives terminal close).
3. Writes the child PID to `~/.idtrack/idtrack.pid`.
4. Redirects child stdout/stderr to `~/.idtrack/idtrack.log` (append mode).
5. Exits immediately. The background child blocks in the HTTP server loop.

With `--foreground`, `commands.Serve` blocks directly in `server.Start()`. This is required for Docker, launchd, and systemd — all service managers need the process to stay alive.

---

## Server Architecture

### Middleware stack

Every request passes through this chain (outermost first):

```text
gzipHandler → secureHeaders → limitBody → quiesce → mux
```

| Layer | Purpose |
| --- | --- |
| `gzipHandler` | Buffers response; compresses with gzip if body ≥ 1,400 bytes and client sent `Accept-Encoding: gzip` |
| `secureHeaders` | Sets `X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`, etc. |
| `limitBody` | Caps request body at 1 MB to prevent memory exhaustion |
| `quiesce` | Holds `backupMu.RLock()` for the request lifetime so backup writes can quiesce the server |
| `mux` | Go 1.22+ `http.ServeMux` with method-prefixed patterns |

Additionally, individual routes may be wrapped with:

- `auth()` — validates the session cookie or `Authorization: Bearer` token; stores `*db.User` in the request context
- `requireJSON()` — enforces `Content-Type: application/json` on endpoints with a request body

### Response compression

`server/compress.go` implements `gzipHandler` using a `bufferingWriter`. The buffer collects the full response body before the handler returns. If the body is ≥ 1,400 bytes (one Ethernet MTU payload) and the client supports gzip, the response is compressed with `compress/gzip` (stdlib). Otherwise, it is written uncompressed. `Content-Length` is removed when compressing because the compressed size differs from the original. `Vary: Accept-Encoding` is set on every response.

**Why 1,400 bytes?** A response that fits in one TCP segment arrives in one RTT regardless of bandwidth; compression cannot reduce that. Only responses that would spill into a second packet can benefit. 1,460 bytes is the Ethernet data payload (1,500 byte frame − 40 bytes TCP/IP), so 1,400 gives a comfortable margin. In practice this threshold compresses all static assets (≈80% reduction), all issue-list responses (≈75%), and large comment threads, while skipping login/status/polling responses where the CPU cost would exceed any gain.

**BREACH/CRIME security note.** These TLS-compression attacks require an attacker able to force authenticated requests and observe encrypted traffic. For a self-hosted internal tool whose API responses contain only data the authenticated user already sees, the practical risk is negligible. This is consistent with the industry consensus on JSON API compression.

### `srv` struct

All server state is on `*srv` (defined in `server/server.go`). Handler methods are attached to `*srv` rather than using package-level globals, keeping them easy to reason about and test in isolation.

Key fields:

| Field | Type | Purpose |
| --- | --- | --- |
| `database` | `*sql.DB` | Single SQLite connection (`MaxOpenConns(1)`) |
| `sessions` | `*sessionStore` | In-memory map of token → `*db.User` |
| `loginLimiter` | `*rateLimiter` | Per-IP rate limiting on `POST /api/login` |
| `backupMu` | `sync.RWMutex` | Quiesces requests during backup copies |
| `dbPath` | `string` | Absolute path to the SQLite file |
| `backupInterval` | `time.Duration` | 0 = backups disabled |
| `backupCount` | `int` | 0 = no count limit |
| `backupAge` | `time.Duration` | 0 = no age limit |
| `backupSize` | `int64` | 0 = no size limit (bytes) |
| `onboardingToken` | `string` | One-time UUID for first-user creation |
| `mu` | `sync.Mutex` | Guards `onboardingToken` and status cache |

---

## Authentication and Sessions

### Login flow

1. Browser POSTs `{ username, password }` (plaintext over TLS) to `POST /api/login`.
2. Server calls `db.VerifyPassword` — bcrypt compare against stored hash.
3. On success, `sessionStore.create()` generates a 64-hex-char cryptographically random token.
4. Token is set as an `HttpOnly; Secure; SameSite=Strict` cookie named `idtrack_session`.
5. With `keep_logged_in: true`, cookie `Max-Age=2592000` (30 days); otherwise, session cookie (expires on tab close).
6. Handler returns `{ username, display_name, is_admin }`.

### Session validation (per request)

The `auth()` middleware calls `sessionStore.lookup(token)`. If found, it stores the `*db.User` in the request context via `currentUser(r)`. If not found, returns 401.

Non-browser clients may use `Authorization: Bearer <token>` instead of a cookie.

### Password hashing

Passwords are stored as bcrypt hashes (Go `golang.org/x/crypto/bcrypt`, default cost). The browser sends the plaintext password over TLS; hashing is server-side only. Legacy SHA-256 hashes (from a previous client-side hashing scheme) are detected by format (`db.IsLegacyHash`) and transparently upgraded to bcrypt on the next successful login (`db.UpgradePasswordHash`).

### Onboarding

When no users exist in the database, `GET /api/status` returns `{ onboarding: true, token: "<uuid>" }`. The UUID is generated lazily, held in `srv.onboardingToken` behind a mutex, and cleared after `POST /api/onboarding` succeeds. `POST /api/onboarding` validates `Authorization: Basic base64("onboarding:<uuid>")`, creates the first user as an admin, records login, sets the session cookie, and returns the same shape as `/api/login`. Returns 409 if users already exist.

---

## Database

### Connection model

SQLite is opened with `MaxOpenConns(1)`. SQLite does not support concurrent writers; serializing all access via a single connection avoids `SQLITE_BUSY` errors without needing WAL mode or retry logic.

### Schema and migrations

The schema is created with `CREATE TABLE IF NOT EXISTS` on every startup. New columns added after the initial schema are applied by `addColumnIfMissing()` in `db/db.go`, which calls `ALTER TABLE ... ADD COLUMN` and treats "duplicate column name" as a success. This means the binary upgrades any existing database automatically on startup with no migration tool.

Columns added via migration: `last_login_at`, `is_admin` (users table); `project`, `component`, `resolved_at`, `dependent_issues` (issues table).

**`dependent_issues` column.** Stores a comma-separated list of issue IDs (e.g. `"7,12,33"`). `parseDependentIssues` converts this to `[]int64`; `formatDependentIssues` converts back. The empty string represents no dependencies. `scanIssue()` centralises all column scanning so every query function reuses the same scan logic. For Duplicate status the list holds exactly one ID; for Blocked it holds one or more; for all other statuses it is always empty (the handler clears it automatically).

**`resolved_at` backfill.** When `resolved_at` is first added to an existing database, a one-time UPDATE sets it for all Resolved issues that have at least one comment, using `MAX(comments.created_at)` as a proxy for the resolution time. Issues with no comments retain `resolved_at = ''`. The UPDATE is guarded by `WHERE resolved_at = ''` so it is a no-op on subsequent starts.

### Comment cascade delete

SQLite does not enforce foreign keys by default. `db.DeleteIssue()` manually deletes associated comments before deleting the issue row. There is no cascade delete on user removal — orphaned `reporter`/`assignee` strings in the issues table are acceptable.

### SQL injection prevention in the issue list

`GET /api/issues` accepts `sort` and `order` query parameters. Rather than interpolating them directly into SQL, `buildWhereClause` in `db/issues.go` uses a lookup table of hardcoded `"column ASC/DESC"` literals. Unknown column names fall back to `id DESC`. All other query parameters are passed as bind parameters, never string-interpolated.

---

## HTTP API

Full table: see [CLAUDE.md](../CLAUDE.md#http-api) or the MANUAL.

### Issue list pagination

`GET /api/issues` supports `limit`, `offset`, `sort`, `order`, `status`, `priority`, `project`, and `search` query parameters. When `limit > 0` the response is:

```json
{ "issues": [...], "total": N, "offset": N, "limit": N }
```

`total` is a `SELECT COUNT(*)` using the same WHERE clause as the data query, so the client can show "X of Y issues" without fetching all rows. `db.CountIssues` and `db.ListIssues` share `buildWhereClause()` to guarantee they always agree.

### Changes polling

`GET /api/issues/changes?since=<RFC3339>` returns all issues whose `updated_at` is strictly after `since`. The frontend polls this every 30 seconds to detect edits by other users and update the visible list in place. The `/changes` path is registered before the `/{id}` wildcard so the literal path takes priority over the pattern.

---

## Frontend Architecture

All JavaScript lives in `resources/idtrack.js` (single file, no build step). The app is a single-page application rendered entirely client-side after the initial HTML/CSS/JS load.

### Initialization sequence

```text
page load
  → applyBranding()         (inline <script> applies desktop-mode class to avoid FOUC)
  → init()
      → GET /api/status     (captures idle_timeout, onboarding state, app branding)
      → check sessionStorage (idtrack_session) → if found, launchApp()
      → check localStorage  (idtrack_persist)  → if found, launchApp()
      → show login screen   (or onboarding screen if status.onboarding == true)
```

### Session persistence (three layers)

1. **`sessionStorage` (`idtrack_session`)** — `{ user }` display object. Survives page refresh; cleared when the tab closes. Written on every successful login.
2. **`localStorage` (`idtrack_persist`)** — `{ user }` display object only. Written when **Keep me logged in** is checked. On the next browser session the frontend restores `_currentUser` from this object without a round-trip; if the 30-day session cookie has expired, the first API call returns 401 and the user is redirected to login.
3. **Login screen** — shown when neither store has a user object.

No credentials or tokens are stored in localStorage. The actual session credential is the `idtrack_session` HttpOnly cookie, which the browser manages automatically.

### Server-side pagination and live polling

The issue list uses an append-only window model (`_issueWindow`). `loadIssueWindow()` resets the window and fetches the first page. An `IntersectionObserver` on a bottom sentinel `<div>` triggers `loadNextPage()` as the user scrolls. All filtering and sorting happen server-side. A `_fetchGen` counter lets `loadIssueWindow()` discard stale responses when filters change rapidly. A 30-second `setInterval` calls `GET /api/issues/changes` to detect external mutations; affected rows update in place, new external changes show a toast with a manual "Refresh" button.

### Column visibility

Nine columns can be toggled via a "Columns ▾" dropdown. Visibility is stored in `_colVisibility` and persisted under `idtrack_prefs.colVisibility` in `localStorage`. `applyColVisibility()` toggles `html.hide-col-X` classes on `<html>`; the CSS rule `html.hide-col-project .col-project { display: none }` hides both `<th>` and `<td>` cells without touching the DOM. An inline `<script>` in `<head>` pre-applies these classes before first render to prevent a flash of all columns.

### Responsive layout

Two CSS breakpoints handle phone and tablet layouts. Both are gated on `html:not(.desktop-mode)`:

- **≤900px (tablet):** Stacked layout. Opening an issue takes the full screen (`has-detail` class on `#main-layout`). The filter bar is hidden at this breakpoint and re-exposed at ≤600px.
- **≤600px (phone):** Two-row header. Only #, Title, Priority, Status columns visible. Overlays become bottom drawers. Login/onboarding remain vertically centred.

**"Always show desktop version"** adds `desktop-mode` to `<html>`, making all responsive rules inert.

### Status-change dialogs

- **Open → Resolved:** Optional dialog with Fixed Version and Comment fields. An Assignee is required before this transition is allowed. If either field is filled, the comment is posted atomically with the status update.
- **Resolved → Open:** Required dialog with a mandatory Reason textarea. The reason is posted as a comment atomically.
- **Any → Duplicate** (`#duplicate-overlay`): Required dialog capturing exactly one target issue ID. Server auto-posts *"Duplicate of issue #N"* on transition. The `dependent_issues` field is stored and returned in all issue responses.
- **Any → Blocked** (`#blocked-overlay`): Required dialog capturing one or more blocking issue IDs plus an optional extra comment. The extra text is sent as `comment` in the PUT body; the server appends it to the auto-generated *"Blocked by issues #N, #M…"* system comment. The inline **Blocked By** chip list appears in the detail panel after saving — any editor can add IDs, only admins can remove them.
- **Blocked → Open:** No dialog. The server validates every `dependent_issues` ID has `status = 'Resolved'`; returns HTTP 409 otherwise.

### Issue status state machine

Valid statuses: `Open`, `Resolved`, `Blocked`, `Duplicate`. All transitions are possible (any status can change to any other), subject to these rules enforced in `handleUpdateIssue`:

| Transition | Requirement |
|---|---|
| Any → Duplicate | Exactly one valid, non-self target issue ID in `dependent_issues` |
| Any → Blocked | At least one valid, non-self issue ID in `dependent_issues`; non-admins can only add IDs to an already-Blocked issue |
| Blocked → Open | Every ID in `dependent_issues` must have status `Resolved` (HTTP 409 otherwise) |
| Any → Resolved or Duplicate | Sets `resolved_at` if currently empty |
| Any → Open or Blocked | Clears `resolved_at` |
| Any → Open or Resolved | Server clears `dependent_issues` automatically |

---

## Backup System

The backup system is implemented in `server/backup.go`. Backups run only when `backupInterval > 0`.

### Mechanism

`startBackups()` is called in `server.Start()` before `httpSrv.Serve()` so the first backup is written before any requests are served. It creates `idtrack-backups/` next to the database file, writes one synchronous startup backup, then launches a ticker goroutine.

`doBackup()` acquires `backupMu.Lock()` (write lock), blocking until all in-flight requests drain. It copies the database file using `io.Copy + fsync`, then releases the lock and calls `ageBackups()` in a goroutine.

Every request holds `backupMu.RLock()` via the `quiesce` middleware, so:

- In-flight requests always finish before a backup copy starts.
- New requests block (briefly, usually < 1 ms) while a backup copy is in progress.
- No 503 is ever returned; clients are unaware of the backup.

### Three-tier retention

`ageBackups()` runs three strategies in order after every backup:

1. **Size-based thinning** (`sizeBackups`) — Time Machine-style density algorithm. See [BACKUPS.md](BACKUPS.md) for full details.
2. **Count-based pruning** — keep only the most recent N files.
3. **Age-based pruning** — delete files whose filename-embedded UTC timestamp predates `now − backupAge`.

Backup filenames encode the UTC timestamp (`idtrack-20060102T150405.db`). Alphabetical order equals chronological order. The filename is the authoritative age source; filesystem mtime is never consulted.

### Size thinning algorithm

`sizeBackups()` categorizes all backup files into:

- **Last hour** (age < 1 h) — never deleted
- **Hourly buckets 1–23** (age 1–24 h) — keep most recent per 1-hour window
- **Daily buckets 1–N** (age ≥ 24 h) — keep most recent per 24-hour window

Deletion priority when total size exceeds limit:

1. Extras within each hourly bucket, newest bucket first
2. Extras within each daily bucket, newest day first
3. Hourly-23 keeper if daily-1 already exists (pre-emptive transition)
4. Oldest daily keeper, repeated until limit is met

---

## Adding New Server-Wide Configuration

Follow this pattern (all existing backup and timeout flags use it):

1. Add a `string` or `int` field to `defaults` struct in `commands/common.go` with `json:",omitempty"`.
2. Add parsing and validation in `commands/defaults.go` (flag case + `anySet` expression + save block + display in `showDefaults()`).
3. Parse the stored string to its runtime type in `commands/serve.go` and pass to `server.Start()`.
4. Add the parameter to `server.Start()` signature and add the field to the `srv` struct in `server/server.go`.
5. Use the field in the relevant server code.

Duration-type flags are stored as strings in `defaults.json` and parsed to `time.Duration`. Size-type flags are stored as strings (e.g. `"500mb"`) and parsed to `int64` bytes by `parseBackupSize()`. Bool flags use `"true"`/`"false"` strings or integers — use `*bool` for optional booleans to distinguish "not set" from `false` (`UpdateUser` is an example).

---

## Security Notes

| Concern | Approach |
| --- | --- |
| Password storage | bcrypt (default cost); legacy SHA-256 transparently upgraded on next login |
| Session tokens | 64-hex-char crypto/rand; HttpOnly Secure SameSite=Strict cookie |
| Content-Type enforcement | `requireJSON` middleware on all POST/PUT endpoints with a body |
| Body size limit | `limitBody` middleware caps at 1 MB |
| Issue authorization | Reporter, assignee, or admin may modify/delete; others receive 403 |
| Login rate limiting | Per-IP rate limiter on `POST /api/login` |
| Admin last-admin guard | `db.CountAdmins()` checked before delete/demotion; blocks lockout |
| SQL injection | Bind parameters everywhere; sort/order columns from a hardcoded allowlist |
| Security headers | `secureHeaders` middleware on every response |
| TLS | HTTPS only; self-signed embedded cert or externally-configured cert |
| BREACH/CRIME | Response compression only for data the authenticated user already sees; practical risk negligible for a self-hosted internal tool |

---

## Known Design Trade-offs

- **Idle timeout is client-enforced only.** The server communicates the timeout via `GET /api/status` but does not invalidate sessions server-side when it elapses. A determined client could suppress the frontend timer. For an internal tool this is an acceptable trade-off; a higher-security deployment would add server-side session expiry.
- **SQLite `MaxOpenConns(1)`.** Serializes all database access. This is correct for SQLite's writer model but means all reads and writes are serialized. For very high concurrency a WAL-mode SQLite with separate reader connections would be faster, but is unnecessary for a small-team tracker.
- **Status transitions post comments non-atomically.** `doSaveIssue` calls `updateIssue()` then `addComment()`. If the comment write fails after the issue update succeeds, the status change persists without the comment. This is acceptable for the Open→Resolved optional comment; for Resolved→Open the required comment failing would indicate a server error unlikely in practice.
- **No server-side session expiry.** Sessions live until `POST /api/logout` is called. The 30-day `Keep me logged in` cookie provides a natural expiry for persistent sessions, but non-persistent sessions (session cookies) theoretically live forever in the session store until server restart. A periodic cleanup goroutine could be added if session accumulation becomes a concern.
