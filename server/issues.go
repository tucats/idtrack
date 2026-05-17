package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleListIssues reads optional query parameters and delegates filtering and
// sorting to db.ListIssues. All filtering is done in SQL rather than in Go to
// keep memory usage low for large issue lists.
func (s *srv) handleListIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	issues, err := db.ListIssues(
		s.database,
		q.Get("status"),
		q.Get("priority"),
		q.Get("search"),
		q.Get("sort"),
		q.Get("order"),
	)

	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issues": issues,
		"total":  len(issues), // included so the client can show a count without iterating
	})
}

// handleCreateIssue creates a new issue. The reporter is always set to the
// authenticated user's username — clients cannot spoof the reporter field.
func (s *srv) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Assignee    string `json:"assignee"`
		Project     string `json:"project"`
		Component   string `json:"component"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)

		return
	}

	reporter := currentUser(r).Username

	issue, err := db.CreateIssue(s.database, body.Title, body.Description, reporter, body.Assignee, body.Priority, body.Project, body.Component)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{"issue": issue})
}

// handleGetIssue returns a single issue together with all of its comments in
// one response, so the frontend can display the full detail view without a
// second round-trip.
func (s *srv) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return // issueID already wrote the error response
	}

	issue, err := db.GetIssue(s.database, id)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	comments, err := db.ListComments(s.database, id)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issue":    issue,
		"comments": comments,
	})
}

// handleUpdateIssue replaces all editable fields of an issue. All fields must
// be sent in the request body — this is a full replacement (PUT semantics), not
// a partial update (PATCH semantics).
func (s *srv) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Status      string `json:"status"`
		Assignee    string `json:"assignee"`
		Project     string `json:"project"`
		Component   string `json:"component"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)

		return
	}

	issue, err := db.UpdateIssue(s.database, id, body.Title, body.Description, body.Priority, body.Status, body.Assignee, body.Project, body.Component)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"issue": issue})
}

// handleDeleteIssue permanently removes an issue and all its comments.
// Admin-only because deletions are irreversible.
func (s *srv) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	id, ok := issueID(w, r)
	if !ok {
		return
	}

	if err := db.DeleteIssue(s.database, id); err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
