package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleCreateUser creates a new user account. Admin-only.
// Accepts a teams list; is_admin: true is still accepted as an alias that
// adds the "admin" team, for backward compatibility with older clients.
func (s *srv) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	var body struct {
		Username    string   `json:"username"`
		DisplayName string   `json:"display_name"`
		Password    string   `json:"password"`
		IsAdmin     bool     `json:"is_admin"`  // legacy alias: adds "admin" team
		Teams       []string `json:"teams"`
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
		displayName = body.Username
	}

	// Merge the is_admin alias into the teams list.
	teams := mergeAdminAlias(body.Teams, body.IsAdmin)

	if err := db.AddUser(s.database, body.Username, displayName, body.Password, teams); err != nil {
		internalError(w, err)

		return
	}

	if body.DisplayName != "" {
		log.Printf("added user %s (%s)", body.Username, body.DisplayName)
	} else {
		log.Printf("added user %s", body.Username)
	}

	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// lastAdminError is the message returned when an operation would leave the
// system with no admin account.
const lastAdminError = "cannot leave the system with no admin account — use the idtrack CLI to manage admin accounts"

// handleUpdateUser modifies an existing user. Admin-only.
// Accepts a teams list; is_admin is still accepted as a convenience alias.
func (s *srv) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	username := strings.ToLower(r.PathValue("username"))

	var body struct {
		DisplayName string   `json:"display_name"`
		Password    string   `json:"password"`
		IsAdmin     bool     `json:"is_admin"`  // legacy alias; applied on top of teams
		Teams       []string `json:"teams"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	// Determine the effective teams for the last-admin guard.
	// The guard fires when the update would leave no admin in the system.
	effectiveTeams := mergeAdminAlias(body.Teams, body.IsAdmin)
	wouldBeAdmin := db.ContainsTeam(effectiveTeams, db.TeamAdmin)

	if !wouldBeAdmin {
		target, err := db.FindUser(s.database, username)
		if err != nil {
			internalError(w, err)

			return
		}

		if target != nil && target.IsAdmin {
			n, err := db.CountAdmins(s.database)
			if err != nil {
				internalError(w, err)

				return
			}

			if n <= 1 {
				jsonError(w, lastAdminError, http.StatusBadRequest)

				return
			}
		}
	}

	// Pass teams directly (non-nil list) and isAdmin nil (already merged).
	if err := db.UpdateUser(s.database, username, body.DisplayName, body.Password, nil, effectiveTeams); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)

		return
	}

	if body.DisplayName != "" {
		log.Printf("modified user %s (%s)", username, body.DisplayName)
	} else {
		log.Printf("modified user %s", username)
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteUser removes a user account. Admin-only.
func (s *srv) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	username := strings.ToLower(r.PathValue("username"))

	target, err := db.FindUser(s.database, username)
	if err != nil {
		internalError(w, err)

		return
	}

	if target == nil {
		jsonError(w, "user not found", http.StatusNotFound)

		return
	}

	if target.IsAdmin {
		n, err := db.CountAdmins(s.database)
		if err != nil {
			internalError(w, err)

			return
		}

		if n <= 1 {
			jsonError(w, lastAdminError, http.StatusBadRequest)

			return
		}
	}

	if username == currentUser(r).Username {
		jsonError(w, "cannot delete your own account", http.StatusBadRequest)

		return
	}

	if err := db.DeleteUser(s.database, username); err != nil {
		internalError(w, err)

		return
	}

	if target.DisplayName != "" {
		log.Printf("deleted user %s (%s)", username, target.DisplayName)
	} else {
		log.Printf("deleted user %s", username)
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListUsers returns all users. Available to all authenticated users.
func (s *srv) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := db.ListUsers(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"users": users})
}

// mergeAdminAlias combines an explicit teams list with the legacy is_admin
// boolean alias.  When isAdmin is true, "admin" is added to the list if
// absent.  An empty or nil teams list defaults to ["any"].
func mergeAdminAlias(teams []string, isAdmin bool) []string {
	if len(teams) == 0 {
		if isAdmin {
			return []string{db.TeamAdmin}
		}

		return []string{db.TeamAny}
	}

	if isAdmin && !db.ContainsTeam(teams, db.TeamAdmin) {
		teams = append(teams, db.TeamAdmin)
	}

	return teams
}
