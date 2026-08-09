// This file implements idtrack's server-side session store — the piece of
// the authentication system that remembers which users are currently logged
// in. A full picture of the login lifecycle, spread across a few files:
//
//  1. The browser POSTs a plaintext username/password to /api/login over
//     TLS (handleLogin, in auth_handlers.go). The server verifies the
//     password with bcrypt (never storing or logging the plaintext) and, on
//     success, calls sessionStore.create below to mint a new session.
//  2. create generates 32 cryptographically random bytes (crypto/rand, not
//     math/rand — predictable tokens would let an attacker guess another
//     user's session) and hex-encodes them into a 64-character token. The
//     token is stored server-side in this store's in-memory map, keyed by
//     the token string itself and pointing at the username and an expiry
//     time.
//  3. The token is handed back to the browser as the value of an HTTP
//     cookie named idtrack_session (sessionCookieName). handleLogin sets
//     that cookie with the flags HttpOnly (client-side JavaScript cannot
//     read the cookie, which limits the damage an XSS bug could do —
//     malicious injected script cannot steal the token), Secure (the
//     browser will only ever send it over HTTPS), and SameSite=Strict (the
//     browser will not attach it to requests originating from another
//     site, which is idtrack's CSRF defense). Its Max-Age is
//     defaultSessionTTL normally, or keepLoggedInTTL (30 days) when the
//     login request asked to be remembered — see "keep me logged in" in the
//     project docs.
//  4. On every subsequent request to an authenticated endpoint, the auth
//     middleware (middleware.go) reads the token back out of the cookie (or
//     an "Authorization: Bearer <token>" header, for non-browser API
//     clients) via sessionToken, and calls sessionStore.lookup to turn it
//     back into a username and then a *db.User, which handlers access via
//     currentUser(r).
//  5. POST /api/logout (handleLogout, auth_handlers.go) calls
//     sessionStore.delete to remove the server-side entry and clears the
//     cookie in the response, so the token is unusable even if it leaked.
//
// Because sessions live only in this process's memory, restarting the
// server invalidates every session at once — logged-in users simply see a
// fresh login screen on their next request.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// sessionCookieName is the name of the browser cookie that carries the
	// session token. It is also accepted as a "Bearer" token in the
	// Authorization header for API clients that are not browsers.
	sessionCookieName = "idtrack_session"
	// defaultSessionTTL is how long a normal (not "keep me logged in")
	// session lasts before it must be re-established with a fresh login.
	defaultSessionTTL = 24 * time.Hour
	// keepLoggedInTTL is the longer expiry used when the login request opts
	// into "keep me logged in".
	keepLoggedInTTL = 30 * 24 * time.Hour
)

// session is the server-side record for one issued token: which user it
// belongs to and when it stops being valid.
type session struct {
	username  string
	expiresAt time.Time
}

// sessionStore is an in-memory, process-lifetime table of active session
// tokens. It holds no persistent state — nothing here survives a server
// restart — which is an intentional simplicity trade-off for a
// single-instance, self-contained tool like idtrack (no shared session
// store like Redis is needed, and there is nothing to clean up on shutdown).
// mu protects sessions because, as with every net/http server, each request
// is handled on its own goroutine and multiple logins/lookups/logouts can
// race to read and write the map concurrently without it.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session // keyed by the 64-hex-char session token
}

// newSessionStore returns an empty, ready-to-use sessionStore.
func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

// create generates a cryptographically random session token, stores it with
// the given username and TTL, and returns the token. The token is 32 random
// bytes encoded as 64 lowercase hex characters.
func (ss *sessionStore) create(username string, ttl time.Duration) string {
	b := make([]byte, 32)
	rand.Read(b) //nolint:errcheck // rand.Read never returns an error on supported platforms
	token := hex.EncodeToString(b)

	ss.mu.Lock()
	ss.sessions[token] = &session{username: username, expiresAt: time.Now().Add(ttl)}
	ss.mu.Unlock()

	return token
}

// lookup returns the username associated with token if it exists and has not
// expired. Expired entries are deleted on lookup so the map does not grow
// unboundedly.
func (ss *sessionStore) lookup(token string) (string, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	s, ok := ss.sessions[token]
	if !ok {
		return "", false
	}

	if time.Now().After(s.expiresAt) {
		delete(ss.sessions, token)

		return "", false
	}

	return s.username, true
}

// delete removes a session by token. It is a no-op if the token does not exist.
func (ss *sessionStore) delete(token string) {
	ss.mu.Lock()
	delete(ss.sessions, token)
	ss.mu.Unlock()
}
