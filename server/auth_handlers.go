package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tucats/idtrack/db"
)

// This file implements the handlers that govern the account lifecycle:
// version/status probing, first-run onboarding, login, and logout. Together
// these are the entry points that establish (or tear down) the session
// cookie that every other authenticated handler in this package relies on.
//
// Session lifecycle in one paragraph: a browser calls handleLogin (or
// handleOnboarding for the very first user) with credentials; on success the
// server mints a random token, stores it server-side in the in-memory
// sessionStore (see sessions.go) keyed to a username and an expiry time, and
// hands the token back to the browser as an HttpOnly cookie. Every later
// request automatically includes that cookie; the auth() middleware in
// middleware.go looks the token up in the sessionStore, loads the matching
// *db.User from the database, and attaches it to the request context so
// handlers can call currentUser(r) to find out who is asking. handleLogout
// deletes the sessionStore entry, which is what actually revokes the token —
// see the comment on handleLogout below for why that matters.

// duration is a local alias so sessionTTL can return the named type without
// importing the time package name at the call site.
type duration = time.Duration

// statusCacheTTL caps how often GET /api/status queries the database. A
// 5-second window is short enough that normal usage (login, onboarding) sees
// fresh data within one cache cycle, while sustained unauthenticated polling
// hits the DB at most once every 5 seconds rather than on every request (S-09).
const statusCacheTTL = 5 * time.Second

// hasUsersCached returns the most recent HasUsers result if it is no older
// than statusCacheTTL; otherwise it queries the DB and refreshes the cache.
// The result is stored on the srv struct under s.mu so the write is safe for
// concurrent callers.
func (s *srv) hasUsersCached() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.statusCachedAt.IsZero() && time.Since(s.statusCachedAt) < statusCacheTTL {
		return s.statusHasUsers, nil
	}

	result, err := db.HasUsers(s.database)
	if err != nil {
		return false, err
	}

	s.statusHasUsers = result
	s.statusCachedAt = time.Now()

	return result, nil
}

// handleVersion serves GET /api/version. It is a public (no-auth) endpoint —
// it is registered in server.go without the s.auth() wrapper — that returns
// the server's version string and build timestamp. Useful for health checks,
// debugging, and for the frontend's "About" dialog. Note that "handleVersion"
// starts with a lowercase letter, which in Go makes it unexported: it is only
// visible within this package (server), and is reachable from the outside
// world solely through the HTTP route registered for it in server.go. This
// lowercase-means-package-private convention applies to every handler in this
// file and the rest of the package.
//
// Response (200 OK): {"version": "1.0-8", "build_time": "20260516120000"}.
func (s *srv) handleVersion(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{
		"version":    s.version,
		"build_time": s.buildTime,
	})
}

// handleStatus serves GET /api/status. It is a public (no-auth) endpoint
// polled by the frontend on every page load, before any session exists, so
// it cannot depend on the auth() middleware. It reports two independent
// pieces of information the client needs before it can decide what screen to
// show:
//
//  1. idle_timeout (seconds; 0 = disabled) and, when configured, app_name /
//     app_description for UI branding.
//  2. Whether the database currently has zero users. If so, the server is in
//     "first run" state: no login is possible yet because there is nobody to
//     log in as. In that case the response also includes onboarding=true and
//     a fresh one-time token the client must present to POST /api/onboarding
//     to create the first (admin) account. See handleOnboarding below for
//     the full token lifecycle.
//
// The underlying "does the users table have any rows" check is expensive to
// run on every request from an unauthenticated, potentially very chatty
// client, so it is memoized for statusCacheTTL (5s) by hasUsersCached above —
// this handler never queries the database directly.
//
// Response (200 OK) — no users yet:
//
//	{"idle_timeout": 0, "onboarding": true, "token": "<uuid>"}
//
// Response (200 OK) — users exist:
//
//	{"idle_timeout": 1800, "onboarding": false}
func (s *srv) handleStatus(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.hasUsersCached()
	if err != nil {
		internalError(w, err)

		return
	}

	resp := map[string]interface{}{
		"idle_timeout": s.idleTimeout,
	}
	if s.appName != "" {
		resp["app_name"] = s.appName
	}

	if s.appDescription != "" {
		resp["app_description"] = s.appDescription
	}

	if hasUsers {
		resp["onboarding"] = false
		jsonResponse(w, http.StatusOK, resp)

		return
	}

	// Lazily generate the one-time onboarding token the first time this
	// branch is reached with an empty users table, and reuse it on every
	// subsequent status probe until onboarding completes (see
	// handleOnboarding) or the server restarts. s.mu guards this field
	// because handleStatus and handleOnboarding can run concurrently on
	// different goroutines (net/http serves each request on its own
	// goroutine) and both read/write s.onboardingToken.
	s.mu.Lock()
	if s.onboardingToken == "" {
		s.onboardingToken = uuid.New().String()
	}

	token := s.onboardingToken
	s.mu.Unlock()

	resp["onboarding"] = true
	resp["token"] = token
	jsonResponse(w, http.StatusOK, resp)
}

// handleOnboarding serves POST /api/onboarding — creating the very first user
// account (always an admin) when the database is otherwise empty. It is a
// public route (registered in server.go without s.auth()) because, by
// definition, no session can exist yet — nobody has ever logged in.
//
// Auth requirement: instead of a session cookie, the caller must present the
// one-time UUID token that GET /api/status handed out (see handleStatus
// above), via HTTP Basic Auth: "Authorization: Basic base64("onboarding:<uuid>")".
// r.BasicAuth() decodes that header for us and returns the username portion
// ("onboarding", used here purely as a fixed marker string, not a real
// account) and the password portion (the token itself). This is a convenient
// way to smuggle a single secret value in a standard header without inventing
// a bespoke one; it has nothing to do with a real username/password pair.
//
// Why an in-memory-only token is safe: the token is generated by handleStatus
// and stored solely in the srv struct's onboardingToken field (see s.mu
// below), never persisted to disk or the database. If the server restarts
// before onboarding completes, the token is simply lost and the next call to
// GET /api/status mints a fresh one — there is no stale-token security
// concern because the token only ever grants one privilege ("create the
// first admin user"), and that action is itself gated on the users table
// being empty. Once any user exists, onboarding is permanently closed
// (below) regardless of whether someone still holds a copy of an old token.
//
// Request: JSON body {"username", "display_name", "password"} (plaintext
// password; hashed server-side with bcrypt, matching the same scheme used by
// handleLogin). display_name defaults to username when blank.
//
// Response (201 Created): {"username", "display_name", "is_admin": true} —
// same shape as a successful handleLogin response. A session cookie is also
// set, so the new admin is immediately logged in without a separate login
// step.
//
// Errors: 401 if the Basic Auth header is missing/malformed or the token
// doesn't match the current onboardingToken; 409 "onboarding already
// complete" if a user already exists (e.g. a second browser tab racing the
// first submission); 400 for a missing username/password.
func (s *srv) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	marker, token, ok := r.BasicAuth()
	if !ok || marker != "onboarding" {
		jsonError(w, "invalid token", http.StatusUnauthorized)

		return
	}

	s.mu.Lock()
	valid := s.onboardingToken != "" && s.onboardingToken == token
	s.mu.Unlock()

	if !valid {
		jsonError(w, "invalid or expired onboarding token", http.StatusUnauthorized)

		return
	}

	// Re-check for an empty users table even though handleStatus already
	// implied it was empty when it issued a token: two onboarding requests
	// could race (e.g. two browser tabs both completing the first-run form),
	// and this guard ensures only the first one to reach here actually
	// creates a user.
	hasUsers, err := db.HasUsers(s.database)
	if err != nil {
		internalError(w, err)

		return
	}

	if hasUsers {
		s.mu.Lock()
		s.onboardingToken = ""
		s.mu.Unlock()
		jsonError(w, "onboarding already complete", http.StatusConflict)

		return
	}

	// This anonymous struct describes the exact JSON shape we expect in the
	// request body. json.NewDecoder(r.Body).Decode(&body) streams the body
	// and fills in the struct fields whose `json:"..."` tags match the JSON
	// object's keys (case-sensitively matching the tag, not the Go field
	// name) — this is the standard Go idiom for parsing a JSON request body,
	// and the same pattern (decode into a locally-declared struct, bail out
	// on error) recurs in nearly every handler in this package.
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	body.Username = strings.ToLower(strings.TrimSpace(body.Username))
	if body.Username == "" {
		jsonError(w, "username is required", http.StatusBadRequest)

		return
	}

	if body.Password == "" {
		jsonError(w, "password is required", http.StatusBadRequest)

		return
	}

	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" {
		displayName = body.Username
	}

	// db.AddUser hashes the plaintext password with bcrypt before it ever
	// touches the database — the caller here never sees or stores a raw
	// password. The new user is given only the reserved "admin" team, which
	// grants full visibility/admin rights (see db/teams.go for how team
	// membership drives authorization).
	if err := db.AddUser(s.database, body.Username, displayName, body.Password, []string{db.TeamAdmin}); err != nil {
		internalError(w, err)

		return
	}

	s.mu.Lock()
	s.onboardingToken = ""
	s.statusCachedAt = time.Time{} // invalidate HasUsers cache so next status call sees the new user
	s.mu.Unlock()

	db.RecordLogin(s.database, body.Username)

	// Log that someone new was created and logged in.
	if body.DisplayName != "" {
		log.Printf("onboarding user %s (%s)", body.Username, body.DisplayName)
	} else {
		log.Printf("onboarding user %s", body.Username)
	}

	sessToken := s.sessions.create(body.Username, defaultSessionTTL)
	http.SetCookie(w, sessionCookie(sessToken, false))

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"username":     body.Username,
		"display_name": displayName,
		"is_admin":     true,
	})
}

// handleLogin serves POST /api/login. It is a public route (no s.auth()
// wrapper in server.go — a caller obviously cannot present a session cookie
// before they have logged in) that validates credentials and, on success,
// establishes a new session.
//
// Request body (JSON): {"username", "password", "keep_logged_in"}.
// keep_logged_in=true sets a 30-day Max-Age on the response cookie ("Keep me
// logged in"); false (or omitted) issues a session-lifetime cookie that most
// browsers discard when the browser closes, and the server additionally
// expires after defaultSessionTTL (24h) regardless of browser behavior — see
// sessionTTL below.
//
// Password verification flow: db.FindUser loads the row by username, then
// db.VerifyPassword compares the supplied plaintext against the stored hash.
// Two hash formats are supported so that accounts created before the bcrypt
// migration keep working: current hashes are bcrypt (prefixed "$2..."),
// while legacy hashes are a bare 64-character SHA-256 hex digest inherited
// from the old client-side hashing scheme. db.IsLegacyHash detects the
// legacy format, and immediately after a legacy hash verifies successfully,
// db.UpgradePasswordHash re-hashes the same plaintext with bcrypt and
// overwrites the stored hash — so each legacy account is silently migrated
// to bcrypt the first time its owner logs in after the upgrade, with no
// user-visible password reset required. A failure to upgrade is logged but
// does not fail the login (the user already proved they know the password).
//
// Rate limiting: login attempts are throttled per client IP by
// s.loginLimiter (see server/ratelimit.go) to blunt password-guessing
// attacks. Note the ordering: s.loginLimiter.allow(ip) is checked BEFORE
// password verification (so a locked-out IP never even reaches the bcrypt
// comparison, which is deliberately slow), while recordFailure/clear are
// only called AFTER verification — recordFailure on a wrong password,
// clear on success. This means the limiter counts actual failed attempts,
// not merely "requests received," and a successful login resets an IP's
// prior failure history.
//
// Cookie attributes (see sessionCookie below): HttpOnly prevents any
// JavaScript running on the page — including injected via an XSS bug — from
// reading the token via document.cookie, since only the browser's HTTP layer
// can access it; Secure means the browser will only ever send the cookie
// over an HTTPS connection, never plaintext HTTP; SameSite=Strict means the
// browser withholds the cookie on requests originating from another site
// (e.g. an <img> or fetch() on a malicious page targeting this API), which
// is what actually prevents cross-site request forgery (CSRF) using a
// stolen or guessed session — without SameSite, a hostile page could make
// the victim's browser issue authenticated requests on their behalf simply
// because the cookie would be attached automatically.
//
// Response (200 OK): {"username", "display_name", "is_admin"} — the same
// shape returned by handleOnboarding.
//
// Errors: 429 (with Retry-After: 60) if the IP is currently rate-limited;
// 401 "invalid credentials" for an unknown username or wrong password
// (deliberately the same message for both cases, so a caller cannot use the
// error to enumerate valid usernames); 400 for a malformed JSON body.
func (s *srv) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		KeepLoggedIn bool   `json:"keep_logged_in"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	ip := clientIP(r)
	if !s.loginLimiter.allow(ip) {
		w.Header().Set("Retry-After", "60")
		jsonError(w, "too many failed login attempts — try again later", http.StatusTooManyRequests)

		return
	}

	username := strings.ToLower(strings.TrimSpace(body.Username))

	user, err := db.FindUser(s.database, username)
	if err != nil {
		internalError(w, err)

		return
	}

	if user == nil || !db.VerifyPassword(user.PasswordHash, body.Password) {
		s.loginLimiter.recordFailure(ip)
		jsonError(w, "invalid credentials", http.StatusUnauthorized)

		return
	}

	// Transparently upgrade the stored hash from legacy SHA-256 to bcrypt on
	// first successful login after the server is updated.
	if db.IsLegacyHash(user.PasswordHash) {
		if err := db.UpgradePasswordHash(s.database, username, body.Password); err != nil {
			log.Printf("password hash upgrade failed for %s: %v", username, err)
		}
	}

	s.loginLimiter.clear(ip)
	db.RecordLogin(s.database, user.Username)

	// Log that someone new logged in.
	if user.DisplayName != "" {
		log.Printf("login user %s (%s)", user.Username, user.DisplayName)
	} else {
		log.Printf("login user %s", user.Username)
	}

	sessToken := s.sessions.create(user.Username, sessionTTL(body.KeepLoggedIn))
	http.SetCookie(w, sessionCookie(sessToken, body.KeepLoggedIn))

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"username":     user.Username,
		"display_name": user.DisplayName,
		"is_admin":     user.IsAdmin,
	})
}

// handleLogout serves POST /api/logout. It is a public route in server.go
// (not wrapped with s.auth()) deliberately: logout must succeed even when
// the caller's session has already expired or was never valid, so a client
// can call it unconditionally on sign-out without first checking whether it
// is "still logged in."
//
// Why deleting the server-side session record is the real logout step (and
// clearing the browser cookie alone would not be enough): the session
// cookie only carries an opaque token string. If handleLogout merely told
// the browser to forget that cookie, the token itself would still be a live,
// valid key in s.sessions (see sessions.go) until it naturally expired.
// Anyone who had captured a copy of that cookie value beforehand — via a
// browser history/devtools inspection, a proxy log, a leaked HTTP request,
// etc. — could keep using it as a Bearer token indefinitely. Calling
// s.sessions.delete(token) removes the token from the in-memory store so
// the auth() middleware's lookup fails for every future request that
// presents it, from any client, immediately. Clearing the cookie on this
// browser is a courtesy for the current tab; invalidating the token
// server-side is what actually revokes access.
//
// Request: no body (registered without requireJSON in server.go).
// Response (200 OK): {"ok": true}, always, even if there was no session to
// delete.
func (s *srv) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		s.sessions.delete(token)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// sessionTTL returns the appropriate session lifetime.
func sessionTTL(keepLoggedIn bool) duration {
	if keepLoggedIn {
		return keepLoggedInTTL
	}

	return defaultSessionTTL
}

// sessionCookie constructs the Set-Cookie descriptor for a session token.
func sessionCookie(token string, keepLoggedIn bool) *http.Cookie {
	maxAge := 0
	if keepLoggedIn {
		maxAge = int(keepLoggedInTTL.Seconds())
	}

	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}
