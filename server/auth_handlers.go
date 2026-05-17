package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tucats/idtrack/db"
)

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
		jsonError(w, "server error", http.StatusInternalServerError)

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
		jsonError(w, "server error", http.StatusInternalServerError)

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
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		PasswordHash string `json:"password_hash"`
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

	if body.PasswordHash == "" {
		jsonError(w, "password is required", http.StatusBadRequest)

		return
	}

	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" {
		displayName = body.Username
	}

	if err := db.AddUser(s.database, body.Username, displayName, body.PasswordHash, true); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)

		return
	}

	s.mu.Lock()
	s.onboardingToken = ""
	s.mu.Unlock()

	db.RecordLogin(s.database, body.Username)

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"username":     body.Username,
		"display_name": displayName,
		"is_admin":     true,
	})
}

// handleLogin validates Basic Auth credentials, records the login timestamp,
// and returns the user's display name and admin flag so the browser can
// personalise the UI. It is the only endpoint that calls db.RecordLogin — we
// do not update last_login_at on every authenticated request to keep overhead low.
func (s *srv) handleLogin(w http.ResponseWriter, r *http.Request) {
	username, hash, ok := r.BasicAuth()
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)

		return
	}

	username = strings.ToLower(username)

	user, err := db.FindUser(s.database, username)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	if user == nil || user.PasswordHash != hash {
		jsonError(w, "invalid credentials", http.StatusUnauthorized)

		return
	}

	db.RecordLogin(s.database, user.Username)

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"username":     user.Username,
		"display_name": user.DisplayName,
		"is_admin":     user.IsAdmin,
	})
}
