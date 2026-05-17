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

// duration is a local alias so sessionTTL can return the named type without
// importing the time package name at the call site.
type duration = time.Duration

// handleVersion is a public (no-auth) endpoint that returns the server's
// version string and build timestamp. Useful for health checks and debugging.
func (s *srv) handleVersion(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{
		"version":    s.version,
		"build_time": s.buildTime,
	})
}

func (s *srv) handleStatus(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := db.HasUsers(s.database)
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

// handleOnboarding creates the first admin account. The caller authenticates
// with a one-time UUID token in the Basic Auth header (marker "onboarding") to
// prove they received the token from GET /api/status. The password is accepted
// as plaintext and hashed server-side with bcrypt. On success a session cookie
// is set so the user is immediately logged in.
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

	if err := db.AddUser(s.database, body.Username, displayName, body.Password, true); err != nil {
		internalError(w, err)

		return
	}

	s.mu.Lock()
	s.onboardingToken = ""
	s.mu.Unlock()

	db.RecordLogin(s.database, body.Username)

	sessToken := s.sessions.create(body.Username, defaultSessionTTL)
	http.SetCookie(w, sessionCookie(sessToken, false))

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"username":     body.Username,
		"display_name": displayName,
		"is_admin":     true,
	})
}

// handleLogin validates credentials against the database, records the login
// timestamp, upgrades legacy SHA-256 hashes to bcrypt transparently, issues a
// session cookie, and returns the user's display name and admin flag.
//
// Request body (JSON): {"username", "password", "keep_logged_in"}.
// keep_logged_in=true sets a 30-day Max-Age on the cookie; false gives a
// session cookie (cleared when the browser closes).
//
// Login attempts are rate-limited per client IP (see server/ratelimit.go).
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

	sessToken := s.sessions.create(user.Username, sessionTTL(body.KeepLoggedIn))
	http.SetCookie(w, sessionCookie(sessToken, body.KeepLoggedIn))

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"username":     user.Username,
		"display_name": user.DisplayName,
		"is_admin":     user.IsAdmin,
	})
}

// handleLogout deletes the server-side session and clears the session cookie.
// It is a no-op if no valid session cookie is present so clients can call it
// unconditionally on sign-out.
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
