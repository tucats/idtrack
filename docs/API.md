# idtrack HTTP API

This document describes the JSON REST API served by `idtrack`, in enough
detail to write a new client (web, mobile, CLI, or script) against it without
reading the Go source. For an architectural overview of the server internals,
see [IDTRACK.md](IDTRACK.md); for team-based visibility rules in depth, see
[TEAMS.md](TEAMS.md).

## Base URL and transport

idtrack serves **HTTPS by default**, on the configured port (default `8443`).
By default the server presents a self-signed certificate, so a generic HTTP
client will need to be configured to accept it (or trust the operator's
externally-configured cert — see `idtrack default --server-cert`). There is no
separate API host/port — the API is served from the same origin as the web
app, under the `/api/` path prefix.

An operator can instead run the server with `--insecure`/`-k` (see
`idtrack serve`/`idtrack default` in the main docs), which switches the
listener to plain HTTP with no certificate at all. This is intended for
deployments sitting behind a reverse proxy that itself terminates TLS for
browser/client-facing traffic — clients should keep talking HTTPS to the
proxy; only the proxy-to-idtrack hop becomes plaintext HTTP.

All request and response bodies are JSON (`Content-Type: application/json`),
with the following exceptions: `GET` requests and `DELETE` requests never
send a body, and `POST /api/logout` accepts no body. Every JSON-body endpoint
**requires** the request to carry a `Content-Type: application/json` header
— its absence (or any other value) is rejected with `415 Unsupported Media
Type` before the body is even parsed.

Request bodies are capped at **64 KiB**; a larger body is rejected with `413
Request Entity Too Large`.

## Authentication

idtrack uses server-side session tokens, not stateless JWTs. A session is
established by `POST /api/login` (or, for the very first user, `POST
/api/onboarding`) and is presented on every subsequent request in one of two
ways:

1. **Cookie (browsers).** A successful login sets an `HttpOnly; Secure;
   SameSite=Strict` cookie named `idtrack_session` containing a 64-character
   hex token. Browsers attach this automatically; nothing else is required.
2. **Bearer token (non-browser clients).** Any client that isn't a browser —
   a CLI, a mobile app, a script — can instead send
   `Authorization: Bearer <token>`, using the same token value that would
   otherwise be delivered via cookie. There is no separate token-issuance
   endpoint: the token *is* the session cookie's value, so a non-browser
   client should read the `Set-Cookie` response header from `/api/login` (or
   `/api/onboarding`) and reuse that value as its Bearer token on later
   requests — it does not need to store or send the cookie itself.

If both a cookie and an `Authorization` header are present, the cookie takes
priority.

Sessions are held **in memory** on the server. They do not survive a server
restart (all sessions are invalidated at once), and there is no
server-enforced idle timeout — `GET /api/status` reports an `idle_timeout`
value for the *client* to enforce (e.g. auto-logout after N seconds of
inactivity), but the server will honor the token until it either expires on
its own TTL or is explicitly revoked by `POST /api/logout`.

**Session lifetime:** 24 hours by default, or 30 days if the login request
included `"keep_logged_in": true`. There is no refresh/renewal endpoint — a
client whose token has expired must call `POST /api/login` again.

### First-run onboarding

A freshly-initialized database has no users, and therefore nobody can log
in. `GET /api/status` detects this and, instead of `onboarding: false`,
returns `onboarding: true` plus a one-time UUID `token`. A client must
present that token via HTTP Basic Auth to `POST /api/onboarding` to create
the first account, which is always granted admin rights. Once any user
exists, onboarding is permanently closed — a client should check
`onboarding` on every `GET /api/status` response before deciding whether to
show a login form or a first-run "create account" form.

The onboarding token is process-lifetime only: if the server restarts before
onboarding completes, the client's next `GET /api/status` call simply mints
a new one.

## Team-based visibility

Every user belongs to one or more **teams** (see [TEAMS.md](TEAMS.md) for
the full model). Two team names are reserved: `admin` (full access to
everything, plus the ability to perform admin-only mutations) and `any`
(read visibility into everything, but no admin rights). Projects and issues
are each tagged with the teams allowed to see them; an item tagged `any` is
visible to every user regardless of their own teams.

This matters to a client author mainly in two ways:

- `GET /api/issues` and `GET /api/projects` only ever return items the
  *calling* user can see — there is no way to request "everything" as a
  non-admin, and no separate "forbidden" response for a filtered-out item
  simply because it never appears in the list.
- `GET /api/issues/{id}` does **not** re-check team visibility — any
  authenticated user who knows (or guesses) a valid numeric issue ID can
  fetch that single issue directly, even one they would not see in a list.
  Client authors should not treat "can list it" and "can fetch it directly"
  as equivalent guarantees.

## Conventions used throughout

**Error responses.** Every error, from every endpoint, has the same shape:

```json
{ "error": "human-readable message" }
```

The HTTP status code is the authoritative signal for programmatic branching;
the `error` string is meant for logs/debugging/display, not for
pattern-matching (though a few endpoints do document specific message
substrings below where no better signal exists).

**Success envelope.** There is no single universal success envelope — each
endpoint documents its own response shape below. Simple mutations that don't
return a resource respond with `{"ok": true}`.

**Timestamps** are ISO-8601 / RFC3339 strings in UTC (e.g.
`"2026-08-09T14:03:22Z"`), used for `created_at`, `updated_at`,
`resolved_at`, and `last_login_at`. `resolved_at` and `last_login_at` may be
the empty string `""` when not applicable (never resolved / never logged
in).

**Usernames** are always lower-case. Clients should lower-case a
user-entered username locally before comparing it to API responses, though
the server also normalizes on every write.

**IDs** (issue IDs, comment IDs) are positive integers, returned and
accepted as JSON numbers.

## Endpoint reference

### Public endpoints (no authentication)

#### `GET /api/version`

Returns the server's build identity. Useful for health checks and diagnostic
UI (e.g. an "About" dialog).

Response `200 OK`:

```json
{ "version": "1.0-34", "build_time": "20260516120000" }
```

#### `GET /api/status`

Poll this **first**, before deciding what screen to show — it tells the
client both the idle-timeout policy and whether the server needs first-run
onboarding.

Response `200 OK` (normal operation):

```json
{ "idle_timeout": 1800, "onboarding": false, "app_name": "idtrack", "app_description": "Issue Tracker" }
```

Response `200 OK` (empty database, needs onboarding):

```json
{ "idle_timeout": 0, "onboarding": true, "token": "3fa85f64-5717-4562-b3fc-2c963f66afa6" }
```

`idle_timeout` is in seconds; `0` means "no idle timeout configured — do not
auto-logout." `app_name` and `app_description` are omitted entirely (not
present as keys) when the operator hasn't configured custom branding — do
not assume they are always present.

#### `POST /api/login`

Request body:

```json
{ "username": "alice", "password": "hunter2", "keep_logged_in": false }
```

Response `200 OK` (also sets the `idtrack_session` cookie):

```json
{ "username": "alice", "display_name": "Alice Adams", "is_admin": false }
```

Errors:

| Status | Meaning |
| --- | --- |
| `400` | Malformed JSON body |
| `401` | `"invalid credentials"` — unknown username *or* wrong password. Deliberately identical for both cases so a client (or attacker) cannot use the error to enumerate valid usernames. |
| `429` | Too many failed attempts from this client IP (10 failures/minute). Response includes `Retry-After: 60`. |

#### `POST /api/onboarding`

Creates the first (admin) user. Requires `Authorization: Basic
base64("onboarding:<token>")` where `<token>` is the value `GET /api/status`
returned in its `token` field — note the fixed literal string `"onboarding"`
before the colon; it is not a real username.

Request body:

```json
{ "username": "alice", "display_name": "Alice Adams", "password": "hunter2" }
```

`display_name` defaults to `username` if omitted or blank.

Response `201 Created` (also sets the session cookie — the new admin is
immediately logged in):

```json
{ "username": "alice", "display_name": "Alice Adams", "is_admin": true }
```

Errors:

| Status | Meaning |
| --- | --- |
| `400` | Missing username or password |
| `401` | Basic Auth header missing/malformed, or the token doesn't match the current one (e.g. it expired because the server restarted) |
| `409` | `"onboarding already complete"` — a user already exists (e.g. two browser tabs raced to submit the onboarding form; only the first wins) |

#### `POST /api/logout`

No request body. Always succeeds, even if the caller's session was already
invalid or absent — a client can call this unconditionally on sign-out
without checking login state first.

Response `200 OK`: `{ "ok": true }`. Also clears the session cookie.

---

### Authenticated endpoints

All endpoints below require a valid session (cookie or Bearer token,
see [Authentication](#authentication)); an absent, expired, or invalid token
produces `401 Unauthorized` with `{"error": "authentication required"}` or
`{"error": "session expired or invalid"}`. Endpoints additionally marked
**Admin only** require the calling user's `is_admin` to be `true`; otherwise
they respond `403 Forbidden` with `{"error": "forbidden"}`.

#### Users

##### `GET /api/users`

Any authenticated user (not admin-only — every client needs this to resolve
usernames to display names, e.g. for populating assignee pickers).

Response `200 OK`:

```json
{
  "users": [
    {
      "username": "alice",
      "display_name": "Alice Adams",
      "teams": ["admin"],
      "is_admin": true,
      "last_login_at": "2026-08-09T14:03:22Z"
    }
  ]
}
```

Password hashes are never included in this or any other response.

##### `POST /api/users` — **Admin only**

Creates a user.

Request body:

```json
{
  "username": "bob",
  "display_name": "Bob Baker",
  "password": "hunter2",
  "teams": ["engineering"],
  "is_admin": false
}
```

`teams` is the current, preferred way to grant admin/visibility rights.
`is_admin: true` is kept as a backward-compatible shorthand equivalent to
adding the reserved `"admin"` team to `teams`; if neither `teams` nor
`is_admin` is supplied, the new user gets the reserved `"any"` team (can see
everything, admin of nothing — the default, pre-teams behavior).
`display_name` defaults to `username` when blank.

Response `201 Created`: `{ "ok": true }`.

Errors: `400` missing username/password; `403` not an admin; `409`
`"username already exists"`.

##### `PUT /api/users/{username}` — **Admin only**

Partial update — a JSON key that is omitted or empty leaves the
corresponding field unchanged. `{username}` in the path is the target
user's login name.

Request body (all fields optional):

```json
{ "display_name": "Bob B. Baker", "password": "newpassword", "teams": ["engineering", "admin"] }
```

`is_admin` is also accepted as a legacy alias, same semantics as in `POST
/api/users`.

Response `200 OK`: `{ "ok": true }`.

Errors: `400` malformed body, or the update would remove admin rights from
the **last remaining admin** in the system (message: `"cannot leave the
system with no admin account — use the idtrack CLI to manage admin
accounts"`); `403` not an admin; `404` username doesn't exist.

##### `DELETE /api/users/{username}` — **Admin only**

Hard-deletes the user row. This does **not** cascade to that user's issues
or comments — their username remains as a plain-text `reporter`/`assignee`/
`author` reference on existing rows.

Response `200 OK`: `{ "ok": true }`.

Errors: `400` last-admin lockout (see above), or attempting to delete your
own account (`"cannot delete your own account"` — checked *after* the
last-admin check, so if both apply, the last-admin message wins); `403` not
an admin; `404` user not found.

#### Teams

##### `GET /api/teams`

Any authenticated user.

Response `200 OK`:

```json
{
  "teams": [
    { "name": "admin", "description": "" },
    { "name": "any", "description": "" },
    { "name": "engineering", "description": "Core engineering team" }
  ]
}
```

`admin` and `any` are always present — they are reserved, built-in teams.

##### `POST /api/teams` — **Admin only**

Request body: `{ "name": "engineering", "description": "Core engineering team" }`

Response `201 Created`: `{ "ok": true }`.

Errors: `400` missing name; `403` not an admin; `409` name is reserved
(`admin`/`any`) or already exists.

##### `PUT /api/teams/{name}` — **Admin only**

`{name}` in the path is the team's *current* name. Renaming cascades to
every user, project, and issue that referenced the old name, in a single
database transaction — a client does not need to update those references
itself.

Request body: `{ "name": "new-name", "description": "Updated description" }`
— either field may be omitted; an empty `name` means "keep the current
name."

Response `200 OK`: `{ "ok": true }`.

Errors: `400` renaming a reserved team, or neither name nor description
supplied; `403` not an admin; `409` the new name collides with an existing
team.

##### `DELETE /api/teams/{name}` — **Admin only**

Fails if the team is still referenced by any user, project, or issue.

Response `200 OK`: `{ "ok": true }`.

Errors: `400` team is reserved; `403` not an admin; `409` team is still in
use — the error message includes counts, e.g. `"team \"engineering\" is
still in use: 2 user(s), 1 project(s), 0 issue(s) — reassign before
deleting"`.

#### Projects and components

##### `GET /api/projects`

Any authenticated user. Returns only the projects visible to the caller's
teams (see [Team-based visibility](#team-based-visibility)).

Response `200 OK`:

```json
{
  "projects": [
    { "name": "Backend", "components": ["API", "Database"], "teams": ["any"] }
  ]
}
```

##### `POST /api/projects` — **Admin only**

Request body: `{ "name": "Backend", "teams": ["any"] }` — `teams` is
optional; an empty/omitted list defaults to `["any"]` (visible to everyone).

Response `201 Created`: `{ "ok": true }`.

**Idempotent:** creating a project with a name that already exists is a
silent no-op — still `201`, not `409`.

Errors: `400` missing name; `403` not an admin.

##### `PUT /api/projects/{project}/teams` — **Admin only**

Replaces the project's team list outright (not a merge). `{project}` is the
project name (case-sensitive, matches the stored value exactly, not
lower-cased).

Request body: `{ "teams": ["engineering", "qa"] }` — an empty list resets
visibility to `["any"]`.

Response `200 OK`: `{ "ok": true }`.

Errors: `403` not an admin; `404` no project with that name exists.

##### `POST /api/projects/{project}/components` — **Admin only**

Request body: `{ "name": "Frontend" }`

Response `201 Created`: `{ "ok": true }`.

**Idempotent:** adding a component name that already exists under this
project is a silent no-op.

Errors: `400` missing name; `403` not an admin; `409` the named project
doesn't exist.

##### `DELETE /api/projects/{project}` — **Admin only**

Deletes the project and all its components. Refuses if any issue still
references this project.

Response `200 OK`: `{ "ok": true }`.

Errors: `403` not an admin; `409` issues still reference this project (error
message lists their IDs).

##### `DELETE /api/projects/{project}/components/{component}` — **Admin only**

Refuses if any issue still references this exact project+component pair.

Response `200 OK`: `{ "ok": true }`.

Errors: `403` not an admin; `409` issues still reference this component
(error message lists their IDs).

#### Issues

The `Issue` object, as returned by every endpoint below:

```json
{
  "id": 42,
  "title": "Login button unresponsive on Safari",
  "description": "Steps to reproduce:\n1. ...",
  "reporter": "alice",
  "assignee": "bob",
  "priority": "High",
  "status": "Open",
  "project": "Frontend",
  "component": "Auth",
  "created_at": "2026-08-01T09:00:00Z",
  "updated_at": "2026-08-09T14:03:22Z",
  "resolved_at": "",
  "dependent_issues": [],
  "teams": ["any"],
  "format": "markdown",
  "comment_count": 3,
  "description_html": "<p>Steps to reproduce:</p>..."
}
```

Field notes:

- `reporter` and `assignee` are **usernames**, not display names — resolve
  them against `GET /api/users` if you need a human-readable name.
- `priority` is one of `"High"`, `"Medium"`, `"Low"`.
- `status` is one of `"Open"`, `"Resolved"`, `"Blocked"`, `"Duplicate"`.
- `dependent_issues` is a list of issue IDs: exactly one for `Duplicate`
  (the issue this one duplicates), one or more for `Blocked` (the issues
  blocking this one); empty for `Open`/`Resolved`.
- `format` is one of `"text"`, `"markdown"`, `"html"` and controls how
  `description` (and comment `body`s) should be rendered for display.
- `description_html` is a **transient, server-rendered** field — present
  only in the response of `GET /api/issues/{id}` and `PUT /api/issues/{id}`
  (never in the `GET /api/issues` list, and never persisted). It is `""`
  when `format` is `"text"` (render `description` yourself with your own
  escaping); pre-rendered HTML when `format` is `"markdown"`; and a verbatim
  copy of `description` when `format` is `"html"`. See
  [`POST /api/render`](#post-apirender) for rendering arbitrary unsaved text
  the same way.
- `comment_count` is a live count of comments on the issue, included so a
  list view can show "3 comments" without a second request per issue.

##### `GET /api/issues`

Any authenticated user; results are filtered to what the caller's teams can
see. Supports filtering, full-text search, sorting, and pagination — **all
performed server-side**; a well-behaved client should never fetch the full
issue set into memory and filter/sort/paginate client-side, both for
performance and because team-restricted issues are simply absent from the
response, not something the client filters out itself.

Query parameters (all optional):

| Parameter | Values | Notes |
| --- | --- | --- |
| `status` | `open`, `resolved`, `blocked`, `duplicate` | Case-insensitive |
| `priority` | `High`, `Medium`, `Low` | |
| `project` | project name | Exact match |
| `search` | free text | Substring match against title/description; max 200 characters |
| `sort` | `id`, `title`, `priority`, `status`, `assignee`, `project`, `component`, `created_at`, `updated_at` | Unrecognized values fall back to `id` |
| `order` | `asc`, `desc` | |
| `limit` | non-negative integer | Page size; `0` (default) means "return everything, no pagination" |
| `offset` | non-negative integer | Rows to skip; only meaningful together with `limit` |

Response `200 OK`:

```json
{
  "issues": [ { "...": "Issue objects, see above" } ],
  "total": 137,
  "offset": 0,
  "limit": 50
}
```

`total` is the count of **all** matching rows across every page (for
rendering "N of M"), not just `len(issues)` — except when `limit` is `0`
(the no-pagination default), in which case `total` simply equals the number
of issues returned since every matching row was already included.

**Pagination recipe:** to page through a large issue list, pass a fixed
`limit` (e.g. 50) and increase `offset` by that amount each request until
`offset + len(issues) >= total`.

Errors: `400` `search` exceeds 200 characters, or `limit`/`offset` are not
valid non-negative integers.

##### `GET /api/issues/changes?since=<RFC3339 timestamp>`

Polling endpoint for detecting issues that another user has created or
modified since a given point in time, without re-fetching or re-filtering
the whole list. `since` is **required**.

Deliberately **not** filtered by `status`/`priority`/`project`/`search` —
only by the caller's team visibility. This is intentional: a status-filtered
"what changed" query could never report an issue that just stopped matching
the filter (e.g. an Open→Resolved transition no longer matches
`status=open`, so it would silently vanish from a filtered client's view
instead of being reported as changed). Clients that maintain a filtered view
should fetch the full team-visible change set from this endpoint and apply
their own filter-relevance logic locally, so they can both add newly-matching
issues *and* remove issues that just stopped matching.

Response `200 OK`:

```json
{ "issues": [ { "...": "Issue objects, ordered by updated_at ascending" } ] }
```

Errors: `400` `since` missing.

**Suggested polling pattern:** remember a timestamp (e.g. the moment you
last loaded/refreshed), poll this endpoint periodically (idtrack's own
frontend uses 30 seconds) with that timestamp, and advance your stored
timestamp forward — but only ever forward, never backward, to guard against
clock skew between requests.

##### `POST /api/issues`

Any authenticated user may file an issue — there is no admin requirement.

Request body:

```json
{
  "title": "Login button unresponsive on Safari",
  "description": "Steps to reproduce:\n1. ...",
  "priority": "High",
  "assignee": "bob",
  "project": "Frontend",
  "component": "Auth",
  "format": "markdown"
}
```

`title` is the only required field. All others take database defaults when
omitted: `priority` → `"Medium"`, `status` always starts as `"Open"`,
`format` → `"text"`. `reporter` is **not** a request field — the server
always sets it to the authenticated caller's own username, so a client
cannot file an issue that appears to be reported by someone else.

If `project` is non-blank, it must name a project that both exists and is
visible to the caller's teams, or the request is rejected — this is checked
even though creating an issue has no admin requirement, to prevent filing
into a project the caller isn't otherwise allowed to see.

Response `201 Created`: `{ "issue": { "...": "the new Issue object" } }`

Errors: `400` missing title; `403` named `project` doesn't exist or isn't
visible to the caller.

##### `GET /api/issues/{id}`

Returns one issue **together with all its comments** in a single response,
so a detail view never needs a second round-trip.

Response `200 OK`:

```json
{
  "issue": { "...": "Issue object, including description_html" },
  "comments": [
    {
      "id": 7,
      "issue_id": 42,
      "author": "bob",
      "body": "Confirmed on Safari 17.",
      "created_at": "2026-08-02T10:15:00Z",
      "body_html": "<p>Confirmed on Safari 17.</p>"
    }
  ]
}
```

`body_html` on each comment follows the same rendering rule as
`description_html` on the issue, keyed by the **issue's** `format` (a
comment has no format of its own).

Errors: `400` invalid (non-numeric or non-positive) id; `404` no issue with
that id.

> **No team-visibility check on this endpoint.** Unlike `GET /api/issues`,
> fetching a single issue by ID does not verify the caller's teams can see
> it. Any authenticated user who knows or guesses a valid issue ID can
> retrieve it directly.

##### `PUT /api/issues/{id}`

Authorization: only the issue's **reporter**, its current **assignee**, or
an **admin** may update it; anyone else gets `403`.

This is a **full replacement (PUT semantics), not a partial update**: send
every editable field on every call, not just the ones that changed — an
omitted field is treated as an explicit empty value, not "leave unchanged."

Request body:

```json
{
  "title": "Login button unresponsive on Safari",
  "description": "Updated repro steps...",
  "priority": "High",
  "status": "Resolved",
  "assignee": "bob",
  "project": "Frontend",
  "component": "Auth",
  "format": "markdown",
  "dependent_issues": [],
  "teams": ["any"],
  "comment": ""
}
```

Notes on individual fields:

- `teams` — replaces the issue's team list, but **only admins may actually
  change it**. A non-admin may safely echo back the issue's current `teams`
  value unchanged; sending a *different* list as a non-admin is rejected
  with `403`.
- `comment` — accepted for symmetry with the reference web client's UI flow
  but **not used by this endpoint at all**. Status-change comments (e.g. "why
  I'm reopening this") must be posted separately via `POST
  /api/issues/{id}/comments` after this PUT succeeds — see
  [Status-transition rules](#status-transition-rules) below for the one
  exception (`Duplicate`, which the server comments on automatically).

###### Status-transition rules

Setting `status` to certain values triggers extra validation and,
occasionally, server-generated side effects:

| Transition | Rule |
| --- | --- |
| → `Duplicate` | `dependent_issues` must contain **exactly one** existing issue ID, and it cannot be the issue's own ID. The server automatically posts a comment `"Duplicate of issue #N"` on this issue. |
| → `Blocked` | `dependent_issues` must contain **at least one** existing issue ID, none equal to the issue's own ID. The server does **not** auto-comment for this transition — post your own explanatory comment separately if desired. If the issue is *already* `Blocked`, a non-admin caller may only **add** IDs to the list, never remove one (removal is admin-only). |
| `Blocked` → `Open` | Every ID currently in `dependent_issues` must belong to an issue whose `status` is `Resolved`, or the request is rejected with `409`. |
| → `Open` or → `Resolved` | `dependent_issues` is cleared automatically regardless of what the client sent. |

Response `200 OK`: `{ "issue": { "...": "the updated Issue object" } }`

Errors:

| Status | Meaning |
| --- | --- |
| `400` | Invalid id; missing title; invalid `dependent_issues` for the requested status (wrong count, self-reference, or an ID that doesn't exist) |
| `403` | Caller is not the reporter/assignee/admin; a non-admin tried to remove a `Blocked` dependency; a non-admin tried to change `teams` |
| `404` | The issue itself, or a referenced `dependent_issues` entry, doesn't exist |
| `409` | `Blocked`→`Open` attempted while a dependency is still unresolved (message names the offending issue and its current status) |

##### `DELETE /api/issues/{id}`

Same authorization rule as `PUT`: reporter, assignee, or admin only.
Permanently deletes the issue **and all its comments**.

Response `200 OK`: `{ "ok": true }`.

Errors: `400` invalid id; `403` caller is not the reporter/assignee/admin;
`404` no issue with that id.

#### Comments

##### `POST /api/issues/{id}/comments`

Any authenticated user may comment on any issue they can reach — there is no
reporter/assignee/admin restriction on commenting itself.

Request body: `{ "body": "Confirmed on Safari 17." }` — required, rejected
if blank after trimming.

`author` is never a request field — always set to the caller's own
username.

Response `201 Created`:

```json
{
  "comment": {
    "id": 7,
    "issue_id": 42,
    "author": "bob",
    "body": "Confirmed on Safari 17.",
    "created_at": "2026-08-02T10:15:00Z"
  }
}
```

Errors: `400` invalid issue id or blank body; `404` the issue does not
exist.

##### `DELETE /api/issues/{id}/comments/{cid}` — **Admin only**

`{id}` (the parent issue) is present in the path for a RESTful shape but is
not actually validated — deletion is keyed purely off `{cid}`, the comment
ID.

Response `200 OK`: `{ "ok": true }`.

Deleting an already-deleted or nonexistent (but well-formed) comment ID
still returns `200` — the server does not report whether a row actually
existed.

Errors: `400` invalid comment id; `403` not an admin.

#### Rendering

##### `POST /api/render`

Renders arbitrary text server-side using the same Markdown engine as
`description_html`/`body_html`, **without** requiring the text to belong to
a saved issue or comment. Intended for a live "preview while editing" UI —
render the user's in-progress, unsaved draft exactly as it will look once
saved.

Request body: `{ "format": "markdown", "text": "**bold** and a [link](https://example.com)" }`

`format` accepts the same three values as an issue's `format` field
(`"text"`, `"markdown"`, `"html"`), with the same rendering rule: `"text"`
returns an empty string (render it yourself), `"markdown"` returns rendered
HTML, `"html"` returns the input unchanged.

Response `200 OK`: `{ "html": "<p><strong>bold</strong> and a <a href=\"https://example.com\">link</a></p>" }`

Markdown rendering supports GitHub-Flavored Markdown extensions (tables,
strikethrough, autolinking, task lists) in addition to standard CommonMark.

No errors beyond `400` for a malformed JSON body — an unrecognized `format`
value simply renders as empty string, same as `"text"`.

## Quick-start client flow

A minimal client implementing login → browse → create → comment:

1. `GET /api/status` — read `idle_timeout`; if `onboarding: true`, show a
   "create the first admin account" form and `POST /api/onboarding` instead
   of steps 2–3.
2. `POST /api/login` with `{username, password}`. Store the response body
   (`username`, `display_name`, `is_admin`) for UI state. If not a browser,
   capture the `idtrack_session` cookie value from the response and reuse it
   as a Bearer token on every following request.
3. `GET /api/users` and `GET /api/projects` — cache these to resolve
   usernames to display names and to populate project/component pickers.
4. `GET /api/issues?status=open&limit=50&offset=0&sort=updated_at&order=desc`
   — first page of open issues, newest-updated first.
5. `POST /api/issues` with `{title, description, priority, project,
   component, format}` to file a new issue.
6. `POST /api/issues/{id}/comments` with `{body}` to comment on it.
7. `POST /api/logout` when the user signs out.

For a background "live updates" experience, poll `GET
/api/issues/changes?since=<timestamp>` on an interval and merge results into
your local issue cache per the guidance in that endpoint's section above.
