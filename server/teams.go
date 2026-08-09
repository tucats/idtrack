package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleListTeams serves GET /api/teams. Auth: any authenticated user (no
// admin check) — every user needs the team list so the frontend can render
// team-chip pickers (e.g. when filtering, or for an admin editing another
// user's teams) and label existing team assignments by name.
//
// Response (200 OK): {"teams": [...]} — each entry has name + description,
// ordered by name. Includes the two reserved teams ("admin", "any") along
// with any operator-defined ones.
func (s *srv) handleListTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := db.ListTeams(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"teams": teams})
}

// handleCreateTeam serves POST /api/teams. Auth: authenticated session plus
// an in-handler admin-only check (currentUser(r).IsAdmin), same pattern used
// throughout this package for admin-only mutations.
//
// Request body (JSON): {"name", "description"}. name is lower-cased/trimmed
// and required; db.CreateTeam rejects reserved names ("admin"/"any") and
// duplicates.
//
// Response (201 Created): {"ok": true}.
//
// Errors: 403 not an admin; 400 missing name; 409 if db.CreateTeam reports
// the name is reserved or already exists (both surfaced as the same status
// here — handleDeleteTeam and handleUpdateTeam below distinguish "reserved"
// as 400 instead, see their comments).
func (s *srv) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	name := strings.ToLower(strings.TrimSpace(body.Name))
	if name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)

		return
	}

	if err := db.CreateTeam(s.database, name, body.Description); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleDeleteTeam serves DELETE /api/teams/{name}. Auth: authenticated
// session plus in-handler admin-only check. {name} is a path parameter,
// lower-cased before use.
//
// db.DeleteTeam refuses to delete a reserved team ("admin"/"any") or one
// still referenced by any user/project/issue; the handler distinguishes
// those two failure modes purely by sniffing the word "reserved" in the
// returned error's message (strings.Contains(msg, "reserved")) to pick the
// HTTP status — a reserved-name attempt is a client mistake (400 Bad
// Request), while "still in use" is a conflict with existing data (409
// Conflict, the default).
//
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 400 team name is reserved; 409 team is still
// referenced by a user, project, or issue (the error message lists counts).
func (s *srv) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	name := strings.ToLower(r.PathValue("name"))

	if err := db.DeleteTeam(s.database, name); err != nil {
		msg := err.Error()
		status := http.StatusConflict

		if strings.Contains(msg, "reserved") {
			status = http.StatusBadRequest
		}

		jsonError(w, msg, status)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUpdateTeam serves PUT /api/teams/{name}. Auth: authenticated session
// plus in-handler admin-only check. {name} (the current/old name) is a path
// parameter, lower-cased before use.
//
// Request body (JSON): {"name", "description"} — name is the desired new
// name (may be the same as the current one to only change the description);
// either field may be effectively omitted (empty description is allowed,
// and db.UpdateTeam treats an empty new name as "keep the current name").
// Renaming cascades inside db.UpdateTeam to every user/project/issue that
// referenced the old team name, all within a single transaction, so
// visibility rules stay consistent after the rename.
//
// Same reserved-name detection as handleDeleteTeam: db.UpdateTeam refuses to
// rename (but does allow describing) a reserved team, and the handler maps
// that specific failure to 400 by checking the error text for "reserved" or
// "required"; any other failure (e.g. name collision) is 409.
//
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 400 renaming a reserved team or a missing
// required field; 409 the new name collides with an existing team.
func (s *srv) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	currentName := strings.ToLower(r.PathValue("name"))

	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	newName := strings.ToLower(strings.TrimSpace(body.Name))

	if err := db.UpdateTeam(s.database, currentName, newName, body.Description); err != nil {
		msg := err.Error()
		status := http.StatusConflict

		if strings.Contains(msg, "reserved") || strings.Contains(msg, "required") {
			status = http.StatusBadRequest
		}

		jsonError(w, msg, status)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
