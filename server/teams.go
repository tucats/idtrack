package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleListTeams returns all teams (name + description) ordered by name.
// Available to all authenticated users so the frontend can populate pickers.
func (s *srv) handleListTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := db.ListTeams(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"teams": teams})
}

// handleCreateTeam creates a new team. Admin-only.
// Body: { "name": "platform", "description": "..." }.
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

// handleDeleteTeam removes a team. Admin-only.
// Returns 400 if the name is reserved; 409 if the team is still in use.
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

// handleUpdateTeam renames a team and/or updates its description. Admin-only.
// Body: { "name": "new-name", "description": "..." } — either field may be omitted.
// Renaming a reserved team returns 400; updating the description of a reserved
// team is allowed.
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
