package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleCreateUser creates a new user account. Admin-only. Returns 409 Conflict
// if the username is already taken (unlike the CLI "add" which upserts). The
// password is accepted as plaintext and hashed server-side with bcrypt.
func (s *srv) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		IsAdmin     bool   `json:"is_admin"`
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

	// Prevent duplicate usernames via the API (the DB layer's upsert is only
	// used by the CLI; the API enforces strict create semantics).
	existing, err := db.FindUser(s.database, body.Username)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	if existing != nil {
		jsonError(w, "username already exists", http.StatusConflict)

		return
	}

	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" {
		displayName = body.Username // default display name to login name
	}

	if err := db.AddUser(s.database, body.Username, displayName, body.Password, body.IsAdmin); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleUpdateUser modifies an existing user. Admin-only. The is_admin flag is
// always passed through even if the client didn't intend to change it, because
// the JSON body always includes a zero-value bool for unset fields. This is
// intentional — the admin UI always sends the current value. When a non-empty
// password is provided it is hashed server-side with bcrypt.
func (s *srv) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	username := strings.ToLower(r.PathValue("username"))

	var body struct {
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		IsAdmin     bool   `json:"is_admin"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	isAdmin := body.IsAdmin
	if err := db.UpdateUser(s.database, username, body.DisplayName, body.Password, &isAdmin); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteUser removes a user account. Admin-only. An admin cannot delete
// their own account to prevent accidentally locking everyone out.
func (s *srv) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	username := strings.ToLower(r.PathValue("username"))
	if username == currentUser(r).Username {
		jsonError(w, "cannot delete your own account", http.StatusBadRequest)

		return
	}

	if err := db.DeleteUser(s.database, username); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListUsers returns all users. Available to all authenticated users (not
// admin-only) because the frontend needs the user list to populate assignee
// dropdowns and resolve display names for all logged-in users.
func (s *srv) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := db.ListUsers(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"users": users})
}
