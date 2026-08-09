package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleListProjects serves GET /api/projects. Auth: any authenticated user
// (no admin check) — every user needs the visible project list to populate
// the "New Issue" project/component dropdowns and the issue list's project
// filter.
//
// Unlike the issue list (handleListIssues in issues.go), which pushes team
// filtering down into the SQL WHERE clause, this handler filters in Go: it
// fetches every project with db.ListProjects and then keeps only the ones
// db.ProjectMatchesUserTeams says are visible to the caller's teams (an
// admin or a member of the reserved "any" team sees everything; otherwise a
// project is visible if it's tagged "any" or shares at least one team with
// the caller — see the "Team-based access control" note in CLAUDE.md for
// the full rule). This is acceptable here because the number of projects is
// expected to be small; issues, which can be numerous, use the SQL-side
// approach instead.
//
// Response (200 OK): {"projects": [...]} — only the projects visible to the
// caller.
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

// handleCreateProject serves POST /api/projects. Auth: authenticated session
// plus in-handler admin-only check.
//
// Request body (JSON): {"name", "teams"}. name is required (non-blank after
// trimming); teams is the list of team names allowed to see this project —
// an empty/omitted list defaults to the reserved "any" team inside
// db.CreateProject, meaning the project is visible to everyone (matching the
// pre-teams behavior of "everyone sees everything" unless an operator
// explicitly restricts it).
//
// Response (201 Created): {"ok": true}.
//
// Note: db.CreateProject uses "INSERT OR IGNORE", so calling this with a
// name that already exists is a silent no-op (still 201) rather than a 409
// — the same idempotent-by-design behavior as the `idtrack define project`
// CLI command (see CLAUDE.md).
//
// Errors: 403 not an admin; 400 missing name; 500 on any other db error.
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

// handleCreateComponent serves POST /api/projects/{project}/components. Auth:
// authenticated session plus in-handler admin-only check. {project} is a
// path parameter (the project name, taken as-is — not lower-cased, matching
// project names stored verbatim elsewhere).
//
// Request body (JSON): {"name"} — the component name; required.
//
// Response (201 Created): {"ok": true}.
//
// Errors: 403 not an admin; 400 missing name; 409 if db.AddComponent reports
// the project does not exist (its error is surfaced verbatim). Like
// db.CreateProject, the underlying insert is "INSERT OR IGNORE", so adding a
// component name that already exists under this project is a silent no-op,
// not an error.
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

// handleUpdateProjectTeams serves PUT /api/projects/{project}/teams. Auth:
// authenticated session plus in-handler admin-only check. {project} is a
// path parameter (project name).
//
// Request body (JSON): {"teams"} — the complete replacement team list for
// this project (not a merge/patch — db.SetProjectTeams overwrites the
// stored value outright). An empty list is normalized to the reserved "any"
// team by db.FormatTeams, meaning "visible to everyone."
//
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 404 if db.SetProjectTeams reports no project
// with that name exists (detected via a zero RowsAffected count on the
// UPDATE, not a SQL error — see db/projects.go).
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

// handleDeleteProject serves DELETE /api/projects/{project}. Auth:
// authenticated session plus in-handler admin-only check. {project} is a
// path parameter.
//
// db.DeleteProject removes the project row and, in the same call, every
// component belonging to it — but only after confirming no issue currently
// references this project; if any do, it refuses and its error message
// lists their IDs so the admin knows what to reassign or resolve first.
//
// Request: no body.
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 409 the project still has issues referencing it
// (message includes the offending issue IDs).
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

// handleDeleteComponent serves
// DELETE /api/projects/{project}/components/{component}. Auth: authenticated
// session plus in-handler admin-only check. {project} and {component} are
// both path parameters.
//
// Same "refuse if still referenced" pattern as handleDeleteProject:
// db.DeleteComponent checks for issues that reference this exact
// project+component pair before deleting, and reports their IDs in the
// error if any exist.
//
// Request: no body.
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 409 the component still has issues referencing
// it.
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
