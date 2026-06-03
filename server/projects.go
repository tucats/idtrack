package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleListProjects returns all projects visible to the calling user.
// Projects whose teams do not intersect with the user's teams are filtered out.
func (s *srv) handleListProjects(w http.ResponseWriter, r *http.Request) {
	all, err := db.ListProjects(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	userTeams := currentUser(r).Teams
	visible := make([]db.Project, 0, len(all))

	for _, p := range all {
		if db.ProjectMatchesUserTeams(p.Teams, userTeams) {
			visible = append(visible, p)
		}
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"projects": visible})
}

// handleCreateProject creates a new project. Admin-only.
// Body: { "name": "...", "teams": ["platform", "any"] }.
func (s *srv) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string   `json:"name"`
		Teams []string `json:"teams"`
	}

	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "name is required", http.StatusBadRequest)

		return
	}

	if err := db.CreateProject(s.database, body.Name, body.Teams); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleCreateComponent adds a named component to an existing project. Admin-only.
func (s *srv) handleCreateComponent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}

	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	project := r.PathValue("project")

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "name is required", http.StatusBadRequest)

		return
	}

	if err := db.AddComponent(s.database, project, body.Name); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleUpdateProjectTeams replaces the team list for a project. Admin-only.
// Body: { "teams": ["platform", "database"] }.
func (s *srv) handleUpdateProjectTeams(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	project := r.PathValue("project")

	var body struct {
		Teams []string `json:"teams"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if err := db.SetProjectTeams(s.database, project, body.Teams); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteProject removes a project and all its components. Admin-only.
func (s *srv) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	project := r.PathValue("project")
	if err := db.DeleteProject(s.database, project); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteComponent removes a single component from a project. Admin-only.
func (s *srv) handleDeleteComponent(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	project := r.PathValue("project")
	component := r.PathValue("component")

	if err := db.DeleteComponent(s.database, project, component); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
