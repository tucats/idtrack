package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// contextKey is a private type used as the key when storing values in a
// request context. Using a named type (rather than a raw string) prevents
// accidental collisions with keys set by other packages that also use strings.
type contextKey string

// ctxUser is the specific key under which the authenticated *db.User is stored
// in the request context by the auth middleware.
const ctxUser contextKey = "user"

// maxRequestBodyBytes caps the size of any POST or PUT body. Requests that
// exceed this limit are rejected with 413 before the body is decoded, preventing
// memory exhaustion from oversized payloads.
const maxRequestBodyBytes = 64 * 1024 // 64 KiB — plenty for any API call

// sessionToken extracts the session token from the request. It prefers the
// HttpOnly session cookie (set by handleLogin/handleOnboarding) over the
// Authorization: Bearer header (provided for non-browser API clients).
func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}

	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	return ""
}

// auth is a middleware constructor. It returns a new http.Handler that:
//  1. Extracts the session token from the request cookie (or Bearer header).
//  2. Looks up the token in the in-memory session store.
//  3. Loads the corresponding user from the database.
//  4. On success, stores the *db.User in the request context and calls next.
//  5. On failure, responds with 401 Unauthorized.
func (s *srv) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token == "" {
			jsonError(w, "authentication required", http.StatusUnauthorized)

			return
		}

		username, ok := s.sessions.lookup(token)
		if !ok {
			jsonError(w, "session expired or invalid", http.StatusUnauthorized)

			return
		}

		user, err := db.FindUser(s.database, username)
		if err != nil || user == nil {
			jsonError(w, "authentication required", http.StatusUnauthorized)

			return
		}

		// context.WithValue returns a new context derived from the request's
		// context with the user embedded under the ctxUser key. We replace the
		// request's context so the handler receives it via r.Context().
		ctx := context.WithValue(r.Context(), ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// currentUser retrieves the *db.User stored by the auth middleware from the
// request context. It returns nil for unauthenticated requests (though in
// practice only authenticated handlers call this). The type assertion
// ".(type)" with a second "ok" return would panic without the comma-ok form;
// using the two-value form here makes nil the safe zero value on failure.
func currentUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(ctxUser).(*db.User)

	return u
}

// limitBody is a middleware that caps the size of POST and PUT request bodies.
// Requests exceeding maxRequestBodyBytes are rejected with 413 Request Entity
// Too Large before json.Decoder ever reads the body.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}

		next.ServeHTTP(w, r)
	})
}

// requireJSON rejects any request whose Content-Type header does not begin
// with "application/json" with 415 Unsupported Media Type. Apply this
// middleware to every handler that decodes a JSON request body (S-11).
// Endpoints with no expected request body (e.g. DELETE, GET, POST /api/logout)
// are intentionally excluded from the route wiring so clients do not need to
// send a Content-Type header for bodyless requests.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			jsonError(w, "content-type must be application/json", http.StatusUnsupportedMediaType)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// secureHeaders is a middleware that adds defensive HTTP response headers to
// every response. It runs as the outermost middleware layer so headers are
// present on all responses, including error pages.
//
// CSP notes: 'unsafe-inline' is required for script-src and style-src because
// the HTML currently uses inline onclick attributes. A stricter CSP (nonce-based
// or hash-based) would require removing all inline event handlers from idtrack.html.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// Prevent browsers from guessing the MIME type of a response.
		h.Set("X-Content-Type-Options", "nosniff")

		// Block the app from being embedded in a frame — prevents clickjacking.
		// The frame-ancestors CSP directive is preferred over X-Frame-Options by
		// modern browsers; both are set for compatibility with older clients.
		h.Set("X-Frame-Options", "DENY")

		// Restrict what resources the browser can load for this page.
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'")

		// Tell the browser to always use HTTPS when connecting to this host.
		// max-age=3600 (1 hour) is conservative for a self-signed-cert deployment;
		// raise to 31536000 (1 year) in production with a trusted certificate.
		h.Set("Strict-Transport-Security", "max-age=3600")

		// Suppress the Referer header when navigating from this app to external
		// URLs (e.g. the GitHub link in the About dialog), preventing internal
		// paths from leaking to third-party servers.
		h.Set("Referrer-Policy", "same-origin")

		next.ServeHTTP(w, r)
	})
}
