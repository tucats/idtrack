# idtrack Security Review

Findings from a manual review of `server/`, `db/`, and `resources/idtrack.js`.
Each item has a severity rating, affected files, a proposed fix, and — where
fixed — a record of what was changed and when.

Severity scale: **Critical** · **High** · **Medium** · **Low** · **Info**

---

## S-01 — No HTTP server timeouts (slow-loris DoS)

**Severity:** High  
**File:** `server/server.go`  
**Fixed:** 2026-05-17

`http.Serve(tlsLn, mux)` is called with Go's zero-value defaults, meaning there
are no read, write, or idle timeouts. A client that opens a connection and sends
bytes very slowly (slow-loris) holds a goroutine and connection slot open
indefinitely. With enough such clients the server's goroutine pool is exhausted
and legitimate requests stall.

**Fix applied:** Replaced the bare `http.Serve` call with a configured
`http.Server` in `server/server.go`:

```go
httpSrv := &http.Server{
    Handler:      handler,
    ReadTimeout:  15 * time.Second,
    WriteTimeout: 30 * time.Second,
    IdleTimeout:  120 * time.Second,
}
return httpSrv.Serve(tlsLn)
```

---

## S-02 — No request body size limit (memory exhaustion DoS)

**Severity:** High  
**Files:** `server/issues.go`, `server/users.go`, `server/comments.go`,
`server/auth_handlers.go`, `server/projects.go`  
**Fixed:** 2026-05-17

Every handler that accepts a POST or PUT body decodes it with
`json.NewDecoder(r.Body).Decode(...)` without first capping the body size.
An attacker can send a gigabyte body to exhaust the server's heap, triggering
an OOM kill or a minutes-long GC pause that blocks all other requests.

**Fix applied:** Added a `limitBody` middleware function in `server/middleware.go`
that wraps `r.Body` with `http.MaxBytesReader` (64 KiB cap) on every POST and
PUT request. The middleware is wired as the second outermost layer in `Start()`:

```go
handler := secureHeaders(limitBody(mux))
```

The 64 KiB limit is generous for any legitimate API call while remaining far
below anything that would stress the heap.

---

## S-03 — No login rate limiting (brute-force / credential stuffing)

**Severity:** High  
**File:** `server/auth_handlers.go` (`handleLogin`), `server/middleware.go` (`auth`)  
**Fixed:** 2026-05-17

There is no throttling on failed authentication attempts. `POST /api/login` had
no lockout or delay, allowing unlimited guessing. Because the stored credential
is the raw SHA-256 hex of the password, and SHA-256 is extremely fast to compute,
an attacker can make tens of thousands of guesses per second limited only by
network latency.

**Fix applied:** Added `server/ratelimit.go` containing a `rateLimiter` type
that tracks failed login attempts per client IP using a fixed one-minute window.
After 10 consecutive failures within the window, `handleLogin` returns
`429 Too Many Requests` with a `Retry-After: 60` header. The window resets to
zero on successful login. The limiter uses `RemoteAddr` (not
`X-Forwarded-For`) to prevent clients from spoofing their IP to bypass the limit.
Stale entries are evicted on each `allow()` call to bound memory use.

Rate limiting is applied only to `POST /api/login`; the `auth` middleware (which
runs on every authenticated request) is not rate-limited, as doing so would
penalize users with multiple active browser tabs.

---

## S-04 — Non-constant-time credential comparison (timing side-channel)

**Severity:** Medium  
**Files:** `server/middleware.go`, `server/auth_handlers.go`  
**Fixed:** 2026-05-17

Both the `auth` middleware and `handleLogin` compared password hashes with the
`!=` operator, which short-circuits on the first mismatching byte. In theory,
across many requests, timing differences can reveal how many leading characters
of a submitted hash match the stored hash, aiding dictionary attacks.

**Fix applied:** Replaced all credential comparisons with
`crypto/subtle.ConstantTimeCompare`:

```go
if subtle.ConstantTimeCompare([]byte(user.PasswordHash), []byte(hash)) != 1 {
    // reject
}
```

Applied in both `server/middleware.go` (`auth` middleware) and
`server/auth_handlers.go` (`handleLogin`).

---

## S-05 — Missing security response headers

**Severity:** Medium  
**Files:** `server/middleware.go` (new middleware), `server/server.go` (wiring),
`server/helpers.go` (`jsonResponse`)  
**Fixed:** 2026-05-17

No security headers were set on any response.

**Fix applied:** Added a `secureHeaders` middleware in `server/middleware.go`
that runs as the outermost handler layer and sets the following headers on every
response:

| Header | Value set |
| --- | --- |
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY` |
| `Content-Security-Policy` | `default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'` |
| `Strict-Transport-Security` | `max-age=3600` |
| `Referrer-Policy` | `same-origin` |

Additionally, `Cache-Control: no-store` is now set in `jsonResponse()` in
`server/helpers.go`, so all JSON API responses are excluded from browser and
proxy caches.

**Remaining limitation:** The CSP requires `'unsafe-inline'` for `script-src`
and `style-src` because `idtrack.html` uses inline `onclick` attributes
throughout. Removing these to allow a stricter (nonce-based or hash-based) CSP
would require a significant HTML refactor and is tracked as future work.

The HSTS `max-age` is set conservatively at 3600 seconds (1 hour) to avoid
browser-trust problems with the self-signed certificate. Operators using a
trusted certificate should raise this to 31536000 (1 year).

---

## S-06 — Internal database error strings returned to clients

**Severity:** Medium  
**Files:** `server/users.go`, `server/auth_handlers.go`, `server/projects.go`,
`server/helpers.go`  
**Fixed:** 2026-05-17

Several error paths called `jsonError(w, err.Error(), ...)` with a raw `error`
value from the `db` package, potentially exposing SQLite internals such as
constraint names, table names, and file paths.

**Fix applied:** Added an `internalError(w, err)` helper in `server/helpers.go`
that logs the full error text server-side (to the server log, where it is
available for debugging) and returns a generic `"server error"` string to the
client:

```go
func internalError(w http.ResponseWriter, err error) {
    log.Printf("internal error: %v", err)
    jsonError(w, "server error", http.StatusInternalServerError)
}
```

`internalError` was applied at these specific call sites:

- `server/users.go` — `db.AddUser` failure in `handleCreateUser`
- `server/users.go` — `db.DeleteUser` failure in `handleDeleteUser`
- `server/auth_handlers.go` — `db.AddUser` failure in `handleOnboarding`
- `server/auth_handlers.go` — `db.HasUsers` failure in `handleStatus` and `handleOnboarding`
- `server/auth_handlers.go` — `db.FindUser` failure in `handleLogin`
- `server/projects.go` — `db.CreateProject` failure in `handleCreateProject`
  (also corrected the HTTP status from 409 to 500, since `INSERT OR IGNORE`
  never produces a conflict — any error here is a genuine DB failure)

The intentional human-readable messages that the `db` package constructs
deliberately (e.g. `project "X" is referenced by issues: #1, #2`) are passed
through unchanged, as they are informative and contain no internal detail.
The `db.UpdateUser` "user not found" message and the project/component
referential-integrity messages fall into this category.

---

## S-07 — Unsalted SHA-256 password storage

**Severity:** Medium  
**Files:** `resources/idtrack.js`, `db/users.go`, `server/auth_handlers.go`,
`server/sessions.go` (new), `server/middleware.go`  
**Fixed:** 2026-05-17

Passwords were hashed client-side with plain SHA-256 (no salt) before being
transmitted and stored. The hash was the credential — stored in the database,
sent verbatim in every `Authorization` header, and saved to `localStorage`
when "Keep me logged in" was enabled.

**Fix applied:** Full server-side hashing with session-token authentication:

1. **bcrypt hashing in `db/users.go`:** `AddUser` and `UpdateUser` now hash the
   plaintext password with `bcrypt.GenerateFromPassword` (default cost) before
   storage. The client sends the plaintext password over TLS; the server never
   sees the plaintext stored or forwarded anywhere.

2. **`golang.org/x/crypto/bcrypt`** added as a dependency.

3. **`VerifyPassword(storedHash, plaintext)` in `db/users.go`:** Handles both
   current bcrypt hashes (detected by the `$2` prefix) and legacy SHA-256
   digests (64 lowercase hex characters) so existing accounts continue to work
   without a forced password reset. On next successful login, legacy hashes are
   transparently upgraded to bcrypt via `UpgradePasswordHash` — no admin
   action required.

4. **In-memory session store (`server/sessions.go`):** Login now creates a
   cryptographically random 32-byte (64 hex char) session token, stored in a
   server-side `sessionStore` map with a TTL (24 h default; 30 days when
   "Keep me logged in" is selected). The token is issued as an `HttpOnly;
   Secure; SameSite=Strict` cookie — inaccessible to JavaScript.

5. **`auth` middleware (`server/middleware.go`):** Replaced per-request
   Basic Auth with session token lookup. The token is read from the
   `idtrack_session` cookie (preferred) or an `Authorization: Bearer` header
   (for non-browser API clients).

6. **`POST /api/logout`** (`server/auth_handlers.go`): Deletes the server-side
   session and clears the cookie. Server restart also invalidates all sessions
   (in-memory store is not persisted).

---

## S-08 — Long-lived credentials in `localStorage` with no expiry

**Severity:** Medium  
**File:** `resources/idtrack.js`  
**Fixed:** 2026-05-17 (resolved by S-07 fix)

**Fix applied:** `localStorage` no longer stores any credential. When "Keep me
logged in" is enabled the server issues a 30-day `Max-Age` cookie (HttpOnly,
Secure, SameSite=Strict) and `localStorage` stores only the non-sensitive user
display object `{ user: {username, display_name, is_admin} }` so the UI can
restore its state on the next visit without a credential. The cookie itself
carries the session token; if it has expired the first authenticated API call
returns 401 and the user is shown the login screen.

Server-side session invalidation (`POST /api/logout`) ensures stolen cookies can
be revoked immediately by signing out on the compromised device.

---

## S-09 — `GET /api/status` unauthenticated DB hit (lightweight DoS)

**Severity:** Low  
**File:** `server/auth_handlers.go` (`handleStatus`), `db/users.go` (`HasUsers`)  
**Fixed:** 2026-05-17

`GET /api/status` requires no authentication and executed `SELECT COUNT(*) FROM
users` on every call, with no caching or rate limit. A sustained flood of status
requests forced repeated DB reads and JSON serialization.

**Fix applied:** Added `hasUsersCached()` to `server/auth_handlers.go`. The method
holds `s.mu` while reading or refreshing a cached `bool` + `time.Time` pair on the
`srv` struct (`statusHasUsers` / `statusCachedAt`). The DB is queried at most once
every 5 seconds (`statusCacheTTL`); subsequent calls within the window return the
cached value without touching SQLite. `handleStatus` now calls `s.hasUsersCached()`
instead of `db.HasUsers` directly. `handleOnboarding` zeroes `statusCachedAt`
under the same mutex after successfully creating the first user, so the next
status call immediately sees the updated state rather than waiting for the TTL
to expire.

---

## S-10 — Unbounded `search` query parameter (SQL amplification DoS)

**Severity:** Low  
**Files:** `server/issues.go`, `db/issues.go`  
**Fixed:** 2026-05-17

The `search` query parameter was passed to a `LIKE '%...%'` clause without any
length restriction, allowing arbitrarily long patterns that force a full table
scan on every indexed column.

**Fix applied:** Added `maxSearchLen = 200` in `server/issues.go`.
`handleListIssues` rejects any `search` value longer than 200 characters with
`400 Bad Request` before passing the parameter to `db.ListIssues`. The limit is
generous for any legitimate search while preventing intentionally overlong
patterns.

---

## S-11 — No `Content-Type` validation on incoming JSON requests

**Severity:** Low  
**Files:** `server/middleware.go` (new `requireJSON`), `server/server.go` (route wiring)  
**Fixed:** 2026-05-17

No handler verified that the incoming `Content-Type` was `application/json`
before decoding. This allowed form-encoded POST bodies to be silently treated
as an empty JSON object and weakened defense against certain CSRF-style attacks.

**Fix applied:** Added a `requireJSON` middleware in `server/middleware.go` that
rejects requests with a non-`application/json` Content-Type with
`415 Unsupported Media Type`. It is wired in `server/server.go` around every
route that decodes a JSON request body:

- `POST /api/login`, `POST /api/onboarding`
- `POST /api/users`, `PUT /api/users/{username}`
- `POST /api/projects`, `POST /api/projects/{project}/components`
- `POST /api/issues`, `PUT /api/issues/{id}`
- `POST /api/issues/{id}/comments`

Body-less endpoints (`GET`, `DELETE`, `POST /api/logout`) are intentionally
excluded so clients do not need to send a Content-Type header for requests
with no body.

Note: with `SameSite=Strict` session cookies (S-07) already providing strong
CSRF protection, this fix adds defense-in-depth rather than being the primary
CSRF control.

---

## S-12 — Comment creation does not verify the parent issue exists

**Severity:** Low  
**File:** `server/comments.go` (`handleCreateComment`)  
**Fixed:** 2026-05-17

`handleCreateComment` parsed and validated the issue ID format but did not
check whether an issue with that ID actually existed before inserting the
comment, allowing orphaned comment rows referencing deleted or non-existent
issues.

**Fix applied:** `handleCreateComment` now calls `db.GetIssue` immediately after
parsing the issue ID. If the issue is not found it returns `404 Not Found`
before decoding the request body or calling `db.CreateComment`. The issue
existence check was moved ahead of the body decode so that a 404 is returned
even when no body is provided.

---

## S-13 — `addColumnIfMissing` uses string interpolation for DDL (code pattern)

**Severity:** Info  
**File:** `db/db.go` line 115  
**Status:** Not fixed

```go
fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
```

Table and column names are interpolated directly into SQL. Currently all call
sites use string literals so there is no injection risk. However, the function's
signature accepts arbitrary strings; a future caller passing user-derived input
would create a SQL injection vulnerability.

**Fix:** No immediate action required. Add a comment to the function noting
that arguments must be compile-time constants (never user input). Optionally,
validate against a known-good list of table/column names before executing.

---

## S-14 — Last admin can demote themselves (administrative lockout)

**Severity:** Info  
**File:** `server/users.go` (`handleUpdateUser`, `handleDeleteUser`), `db/users.go`  
**Fixed:** 2026-05-17

The server prevented an admin from deleting their own account but had no guard
against operations that left the system with no admin account at all — either
by demoting the last admin via `PUT /api/users/{user}` with `is_admin: false`,
or by deleting an admin account when it was the only one remaining.

**Fix applied:**

Added `db.CountAdmins(database)` in `db/users.go` which executes
`SELECT COUNT(*) FROM users WHERE is_admin = 1` to count current admins.

Both mutation handlers now call this before committing a change that would
reduce the admin count to zero:

- **`handleUpdateUser`**: If `body.IsAdmin` is `false` and the target user is
  currently an admin, `CountAdmins` is called. If the result is ≤ 1 the
  request is rejected with `400 Bad Request` and the message:
  *"cannot leave the system with no admin account — use the idtrack CLI to
  manage admin accounts"*

- **`handleDeleteUser`**: If the target user is an admin, `CountAdmins` is
  called before the self-deletion check. If the result is ≤ 1 the same error
  is returned. The last-admin guard runs first so the more informative message
  takes priority when both conditions apply (i.e. an admin trying to delete
  themselves when they are the only admin). A `FindUser` call was also added
  at the start of the handler so that deleting a non-existent account now
  returns `404` rather than silently succeeding.

The error message explicitly directs the operator to the CLI
(`idtrack user update --admin true`) because the web app has no mechanism to
bootstrap a new admin without an existing one.

---

## Summary Table

| ID | Title | Severity | Status |
| --- | --- | --- | --- |
| S-01 | No HTTP server timeouts | High | **Fixed 2026-05-17** |
| S-02 | No request body size limit | High | **Fixed 2026-05-17** |
| S-03 | No login rate limiting | High | **Fixed 2026-05-17** |
| S-04 | Non-constant-time credential comparison | Medium | **Fixed 2026-05-17** |
| S-05 | Missing security response headers | Medium | **Fixed 2026-05-17** |
| S-06 | Internal DB errors returned to clients | Medium | **Fixed 2026-05-17** |
| S-07 | Unsalted SHA-256 password storage | Medium | **Fixed 2026-05-17** |
| S-08 | Long-lived localStorage credentials | Medium | **Fixed 2026-05-17** |
| S-09 | Unauthenticated status endpoint hits DB | Low | **Fixed 2026-05-17** |
| S-10 | Unbounded search parameter | Low | **Fixed 2026-05-17** |
| S-11 | No Content-Type validation | Low | **Fixed 2026-05-17** |
| S-12 | Comment creation ignores missing issue | Low | **Fixed 2026-05-17** |
| S-13 | `addColumnIfMissing` DDL interpolation | Info | Open |
| S-14 | Last admin can self-demote | Info | **Fixed 2026-05-17** |
