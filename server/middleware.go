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

// auth is a middleware constructor. It returns a new http.Handler that:
//  1. Reads Basic Auth credentials from the request.
//  2. Looks up the user in the database and checks the password hash.
//  3. On success, stores the *db.User in the request context and calls next.
//  4. On failure, responds with 401 Unauthorized.
//
// The password stored in the database (and sent by the browser) is already a
// SHA-256 hex hash — we compare hashes directly, never plaintext passwords.
func (s *srv) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, hash, ok := r.BasicAuth()
		if !ok {
			jsonError(w, "authentication required", http.StatusUnauthorized)

			return
		}

		username = strings.ToLower(username)

		user, err := db.FindUser(s.database, username)
		if err != nil || user == nil || user.PasswordHash != hash {
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
