package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler is a trivial handler that writes 200 OK.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ---------------------------------------------------------------------------
// requireJSON
// ---------------------------------------------------------------------------

func TestRequireJSON_Accepts(t *testing.T) {
	h := requireJSON(okHandler)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireJSON_Rejects_WrongContentType(t *testing.T) {
	h := requireJSON(okHandler)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
}

func TestRequireJSON_Rejects_Missing(t *testing.T) {
	h := requireJSON(okHandler)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
}

func TestRequireJSON_AcceptsWithCharset(t *testing.T) {
	h := requireJSON(okHandler)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("application/json; charset=utf-8 should be accepted, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// secureHeaders
// ---------------------------------------------------------------------------

func TestSecureHeaders_Set(t *testing.T) {
	h := secureHeaders(okHandler)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	checks := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Strict-Transport-Security": "max-age=3600",
		"Referrer-Policy":           "same-origin",
	}

	for hdr, want := range checks {
		got := w.Header().Get(hdr)
		if got != want {
			t.Errorf("%s: got %q, want %q", hdr, got, want)
		}
	}
}

func TestSecureHeaders_CSP(t *testing.T) {
	h := secureHeaders(okHandler)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src") {
		t.Errorf("CSP header missing default-src: %q", csp)
	}

	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP header missing frame-ancestors: %q", csp)
	}
}

// ---------------------------------------------------------------------------
// limitBody
// ---------------------------------------------------------------------------

func TestLimitBody_PostIsLimited(t *testing.T) {
	// The handler tries to read the full body — MaxBytesReader limits it.
	var gotErr bool

	h := limitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			gotErr = true
		}
	}))

	// Send a body larger than maxRequestBodyBytes.
	oversized := strings.Repeat("x", maxRequestBodyBytes+1)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(oversized))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if !gotErr {
		t.Error("expected error reading oversized POST body")
	}
}

func TestLimitBody_GetNotLimited(t *testing.T) {
	// GET requests should pass through without body limitation.
	called := false

	h := limitBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if !called {
		t.Error("GET handler should be called")
	}
}

// ---------------------------------------------------------------------------
// auth middleware (requires a live *srv with an in-memory database)
// ---------------------------------------------------------------------------

func TestAuth_NoToken(t *testing.T) {
	s := newTestSrv(t)

	called := false
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}

	if called {
		t.Error("inner handler should not be called without token")
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	s := newTestSrv(t)

	h := s.auth(okHandler)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer invalid_token_value")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuth_ValidToken(t *testing.T) {
	s := newTestSrv(t)
	addTestUser(t, s, "alice", false)

	token := s.sessions.create("alice", defaultSessionTTL)

	var gotUser string
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u != nil {
			gotUser = u.Username
		}
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	if gotUser != "alice" {
		t.Errorf("user in context: got %q, want %q", gotUser, "alice")
	}
}

func TestAuth_CookieToken(t *testing.T) {
	s := newTestSrv(t)
	addTestUser(t, s, "bob", false)

	token := s.sessions.create("bob", defaultSessionTTL)

	called := false
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if !called {
		t.Error("handler should be called with valid cookie")
	}
}
