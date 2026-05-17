package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleListProjects returns all projects with their component lists. Available
// to all authenticated users (not admin-only) because the frontend needs the
// full project tree to populate dropdowns for every user.
func (s *srv) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := db.ListProjects(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"projects": projects})
}

// handleCreateProject creates a new project. Admin-only because project
// structure is considered global configuration that ordinary users shouldn't change.
func (s *srv) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	var body struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "name is required", http.StatusBadRequest)

		return
	}

	if err := db.CreateProject(s.database, body.Name); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleCreateComponent adds a named component to an existing project.
// The project name is extracted from the URL path using r.PathValue(), which
// is Go 1.22's built-in way to access named path parameters (e.g. {project}).
func (s *srv) handleCreateComponent(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	project := r.PathValue("project")

	var body struct {
		Name string `json:"name"`
	}

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

// handleDeleteProject removes a project and all its components. The db layer
// returns a 409-worthy error when issues still reference the project, which we
// surface to the client so the user knows which issues to reassign first.
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

// handleDeleteComponent removes a single component from a project. Returns 409
// when issues still reference the project/component combination.
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
