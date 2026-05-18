package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tucats/idtrack/db"
)

// newTestSrv creates a minimal *srv with an in-memory SQLite database.
// The database is closed automatically when the test ends.
func newTestSrv(t *testing.T) *srv {
	t.Helper()

	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	t.Cleanup(func() { d.Close() })

	return &srv{
		database:     d,
		loginLimiter: newRateLimiter(),
		sessions:     newSessionStore(),
		version:      "test",
	}
}

// addTestUser inserts a user into s.database and returns a fresh session token.
func addTestUser(t *testing.T, s *srv, username string, isAdmin bool) string {
	t.Helper()

	if err := db.AddUser(s.database, username, username, "password", isAdmin); err != nil {
		t.Fatalf("AddUser(%q): %v", username, err)
	}

	return s.sessions.create(username, 24*time.Hour)
}

// jsonReq builds an httptest.Request with an optional JSON body and an optional
// Bearer token. Set body="" for bodyless requests.
func jsonReq(t *testing.T, method, path, body, token string) *http.Request {
	t.Helper()

	var r *http.Request

	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}

	return r
}

// do calls handler through the auth middleware, records the response, and
// returns the recorder. Convenience wrapper for the common test pattern.
func do(s *srv, h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.auth(h).ServeHTTP(w, r)

	return w
}
