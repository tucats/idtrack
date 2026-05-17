package server

import (
	"context"
	"crypto/subtle"
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

// auth is a middleware constructor. It returns a new http.Handler that:
//  1. Reads Basic Auth credentials from the request.
//  2. Looks up the user in the database and checks the password hash.
//  3. On success, stores the *db.User in the request context and calls next.
//  4. On failure, responds with 401 Unauthorized.
//
// The password stored in the database (and sent by the browser) is already a
// SHA-256 hex hash — we compare hashes with crypto/subtle.ConstantTimeCompare
// to avoid leaking timing information about partial hash matches.
func (s *srv) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, hash, ok := r.BasicAuth()
		if !ok {
			jsonError(w, "authentication required", http.StatusUnauthorized)

			return
		}

		username = strings.ToLower(username)

		user, err := db.FindUser(s.database, username)
		if err != nil || user == nil || subtle.ConstantTimeCompare([]byte(user.PasswordHash), []byte(hash)) != 1 {
			jsonError(w, "invalid credentials", http.StatusUnauthorized)

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
