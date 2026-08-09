package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleCreateUser serves POST /api/users. It is registered in server.go
// wrapped with both s.auth() (a valid session is required) and requireJSON
// (the request must carry Content-Type: application/json). Beyond that, the
// handler itself enforces an admin-only check as its very first step by
// reading the *db.User the auth middleware already attached to the request
// context via currentUser(r) — non-admins receive 403 Forbidden before any
// other work happens.
//
// Request body (JSON): {"username", "display_name", "password", "is_admin",
// "teams"}. username is lower-cased/trimmed and must be non-empty and not
// already taken; password is required (hashed server-side by db.AddUser,
// same bcrypt scheme as login); display_name defaults to username when
// blank. teams is the current, preferred way to grant access; is_admin is
// kept only as a backward-compatible convenience alias for older clients —
// setting it true is equivalent to including the reserved "admin" team in
// teams (see mergeAdminAlias below), and if neither is supplied the user
// gets the reserved "any" team (visible to everything, admin of nothing).
//
// Response (201 Created): {"ok": true}.
//
// Errors: 403 if the caller is not an admin; 400 for a missing
// username/password or malformed body; 409 "username already exists" if the
// (lower-cased) username is already taken.
func (s *srv) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	var body struct {
		Username    string   `json:"username"`
		DisplayName string   `json:"display_name"`
		Password    string   `json:"password"`
		IsAdmin     bool     `json:"is_admin"` // legacy alias: adds "admin" team
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

// handleUpdateUser serves PUT /api/users/{username}. Auth: any authenticated
// session plus an admin-only check inside the handler (same pattern as
// handleCreateUser). {username} is a path parameter extracted with
// r.PathValue("username") — the {username} segment in the route pattern
// registered in server.go — and is lower-cased before use, matching the
// lower-casing applied everywhere usernames are stored or compared.
//
// This is effectively a partial update: db.UpdateUser (see its doc comment
// in db/users.go) only changes the fields that were actually supplied —
// an empty display_name or password in the body leaves the existing value
// alone. teams/is_admin work the same way as in handleCreateUser: is_admin
// is a legacy alias merged into teams by mergeAdminAlias before the DB call,
// so db.UpdateUser is always given a concrete team list here (never nil).
//
// Last-admin-lockout guard: before writing anything, the handler figures
// out whether the update would leave the target user with no "admin" team
// membership (wouldBeAdmin). If it would strip admin rights from a user who
// currently has them, it calls db.CountAdmins and refuses the update with
// lastAdminError when that user is the only admin left — otherwise a
// well-meaning admin could accidentally demote themselves (or the last other
// admin) and lock everyone out of the admin UI. The CLI (`idtrack user
// update --admin`) is intentionally left as an escape hatch since it isn't
// subject to this guard.
//
// Response (200 OK): {"ok": true}.
//
// Errors: 403 if the caller is not an admin; 400 malformed body or
// last-admin lockout; 404 if db.UpdateUser reports the target username does
// not exist.
func (s *srv) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	username := strings.ToLower(r.PathValue("username"))

	var body struct {
		DisplayName string   `json:"display_name"`
		Password    string   `json:"password"`
		IsAdmin     bool     `json:"is_admin"` // legacy alias; applied on top of teams
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

// handleDeleteUser serves DELETE /api/users/{username}. Auth: authenticated
// session plus an in-handler admin-only check. {username} comes from the
// path (r.PathValue), lower-cased. This is a hard delete — db.DeleteUser
// removes the row outright; per CLAUDE.md this does not cascade to that
// user's issues/comments, so their username simply remains as a plain-text
// reporter/assignee/author reference on any existing rows (an intentional
// tradeoff to avoid destroying issue history when an account is removed).
//
// Two guards run before the delete: (1) the same last-admin check as
// handleUpdateUser — deleting the sole remaining admin is refused with
// lastAdminError; (2) an admin may not delete their own account (self is
// compared to currentUser(r).Username), which prevents an admin from locking
// themselves out mid-session even before the last-admin count would trigger.
// The last-admin check runs first so that, when both conditions apply (the
// caller is both the target and the last admin), the more informative
// lastAdminError message is what the client sees.
//
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 404 user not found; 400 last-admin lockout or
// self-deletion attempt.
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

// handleListUsers serves GET /api/users. Auth: any authenticated user — no
// admin check, unlike the other handlers in this file — because the
// frontend needs the full user list to populate assignee dropdowns and
// resolve usernames to display names (see _userMap in CLAUDE.md) for every
// signed-in user, not just admins. db.ListUsers deliberately omits the
// password_hash column, so this response never leaks credential material.
//
// Response (200 OK): {"users": [...]} — one entry per row in the users
// table, ordered by username.
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
