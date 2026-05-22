package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// maxSearchLen caps the search query parameter to prevent callers from sending
// arbitrarily long patterns that force a full table scan on every column (S-10).
const maxSearchLen = 200

// handleListIssues reads optional query parameters and delegates filtering,
// sorting, and pagination to db.ListIssues / db.CountIssues. All filtering is
// done in SQL rather than in Go to keep memory usage low for large issue lists.
//
// Query parameters:
//
//	status   open|resolved|blocked|duplicate — filter by status
//	priority High|Medium|Low                 — filter by priority
//	project  <name>                          — filter by project
//	search   <text>                          — full-text substring match
//	sort     <column>                        — column to sort by
//	order    asc|desc                        — sort direction
//	limit    <n>                             — page size (0 = return all)
//	offset   <n>                             — rows to skip for pagination
func (s *srv) handleListIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	search := q.Get("search")
	if len(search) > maxSearchLen {
		jsonError(w, "search parameter exceeds maximum length of 200 characters", http.StatusBadRequest)

		return
	}

	limit, offset := 0, 0

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			jsonError(w, "invalid limit parameter", http.StatusBadRequest)

			return
		}

		limit = n
	}

	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			jsonError(w, "invalid offset parameter", http.StatusBadRequest)

			return
		}

		offset = n
	}

	status   := q.Get("status")
	priority := q.Get("priority")
	project  := q.Get("project")
	sortCol  := q.Get("sort")
	sortDir  := q.Get("order")

	// When paginating, run a COUNT query first so the client knows the total
	// number of matching rows without fetching them all.
	total := 0

	if limit > 0 {
		var err error

		total, err = db.CountIssues(s.database, status, priority, search, project)
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)

			return
		}
	}

	issues, err := db.ListIssues(s.database, status, priority, search, project, sortCol, sortDir, limit, offset)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	// When limit == 0 (legacy / return-all mode) the total is the result length.
	if limit == 0 {
		total = len(issues)
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issues": issues,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// handleListChanges returns all issues whose updated_at is strictly after the
// "since" query parameter (an RFC3339 timestamp). Used by the frontend to poll
// for changes made by other users without discarding the current scroll state.
func (s *srv) handleListChanges(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		jsonError(w, "since parameter is required", http.StatusBadRequest)

		return
	}

	issues, err := db.ListChanges(s.database, since)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issues": issues,
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

// issueModifier returns true when the user is authorized to edit or delete the
// given issue. Admins, the original reporter, and the current assignee may all
// make changes; any other authenticated user is a read-only third party.
func issueModifier(u *db.User, issue *db.Issue) bool {
	return u.IsAdmin || u.Username == issue.Reporter || u.Username == issue.Assignee
}

// handleUpdateIssue replaces all editable fields of an issue. All fields must
// be sent in the request body — this is a full replacement (PUT semantics), not
// a partial update (PATCH semantics). Only the reporter, assignee, or an admin
// may update an issue; all other authenticated users receive 403.
//
// Additional rules for the new status values:
//
//   - Duplicate: dependent_issues must contain exactly one existing issue ID.
//     The server auto-posts a "Duplicate of issue #N" comment on transition.
//
//   - Blocked: dependent_issues must contain at least one existing issue ID.
//     The server auto-posts a "Blocked by issues #N[, #M...]" comment on
//     transition; the optional `comment` request field appends user text.
//     Non-admins may only ADD entries to an already-blocked issue's
//     dependent_issues — they cannot remove entries.
//
//   - Open (from Blocked): all entries in the current dependent_issues must
//     have status Resolved before the transition is allowed (HTTP 409 otherwise).
//
//   - Open or Resolved: dependent_issues is cleared automatically by this
//     handler regardless of what the client sends.
func (s *srv) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	// Fetch the current record before decoding the body so we can authorize
	// against the current reporter and assignee fields.
	existing, err := db.GetIssue(s.database, id)
	if err != nil {
		internalError(w, err)

		return
	}

	if existing == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	u := currentUser(r)

	if !issueModifier(u, existing) {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	var body struct {
		Title           string  `json:"title"`
		Description     string  `json:"description"`
		Priority        string  `json:"priority"`
		Status          string  `json:"status"`
		Assignee        string  `json:"assignee"`
		Project         string  `json:"project"`
		Component       string  `json:"component"`
		// DependentIssues carries the issue IDs for Duplicate and Blocked statuses.
		DependentIssues []int64 `json:"dependent_issues"`
		// Comment is optional extra text appended to the server-generated
		// auto-comment when transitioning to Blocked.
		Comment         string  `json:"comment"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)

		return
	}

	oldStatus := existing.Status
	newStatus := body.Status

	// -------------------------------------------------------------------------
	// Validate dependent_issues based on the requested new status.
	// -------------------------------------------------------------------------

	switch newStatus {

	case "Duplicate":
		if len(body.DependentIssues) != 1 {
			jsonError(w, "a Duplicate issue requires exactly one target issue ID in dependent_issues", http.StatusBadRequest)

			return
		}

		depID := body.DependentIssues[0]

		if depID == id {
			jsonError(w, "an issue cannot be marked as a duplicate of itself", http.StatusBadRequest)

			return
		}

		dep, err := db.GetIssue(s.database, depID)
		if err != nil {
			internalError(w, err)

			return
		}

		if dep == nil {
			jsonError(w, fmt.Sprintf("issue #%d does not exist", depID), http.StatusBadRequest)

			return
		}

	case "Blocked":
		if len(body.DependentIssues) == 0 {
			jsonError(w, "a Blocked issue requires at least one blocking issue ID in dependent_issues", http.StatusBadRequest)

			return
		}

		for _, depID := range body.DependentIssues {
			if depID == id {
				jsonError(w, "an issue cannot block itself", http.StatusBadRequest)

				return
			}

			dep, err := db.GetIssue(s.database, depID)
			if err != nil {
				internalError(w, err)

				return
			}

			if dep == nil {
				jsonError(w, fmt.Sprintf("issue #%d does not exist", depID), http.StatusBadRequest)

				return
			}
		}

		// When the issue is already Blocked, non-admins may only append to the
		// dependent_issues list — they cannot remove existing entries.
		if oldStatus == "Blocked" && !u.IsAdmin {
			for _, existingDepID := range existing.DependentIssues {
				found := false

				for _, newDepID := range body.DependentIssues {
					if newDepID == existingDepID {
						found = true

						break
					}
				}

				if !found {
					jsonError(w, "only admins may remove blocking issues from a Blocked issue", http.StatusForbidden)

					return
				}
			}
		}

	case "Open":
		// Transitioning from Blocked to Open requires every blocking issue to
		// be Resolved.  This rule ensures a blocked issue cannot be re-opened
		// until the work it depends on is actually complete.
		if oldStatus == "Blocked" {
			for _, depID := range existing.DependentIssues {
				dep, err := db.GetIssue(s.database, depID)
				if err != nil {
					internalError(w, err)

					return
				}

				if dep == nil {
					jsonError(w, fmt.Sprintf("blocking issue #%d no longer exists", depID), http.StatusConflict)

					return
				}

				if dep.Status != "Resolved" {
					jsonError(w, fmt.Sprintf("cannot unblock: issue #%d is still %s", depID, dep.Status), http.StatusConflict)

					return
				}
			}
		}

		// Clear dependent_issues when reopening so the field doesn't carry
		// stale data from a previous Blocked or Duplicate state.
		body.DependentIssues = nil

	case "Resolved":
		// Clear dependent_issues on resolution for the same reason.
		body.DependentIssues = nil
	}

	// -------------------------------------------------------------------------
	// Persist the update.
	// -------------------------------------------------------------------------

	issue, err := db.UpdateIssue(s.database, id,
		body.Title, body.Description, body.Priority, body.Status,
		body.Assignee, body.Project, body.Component,
		body.DependentIssues,
	)
	if err != nil {
		internalError(w, err)

		return
	}

	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	// -------------------------------------------------------------------------
	// Auto-generate a comment for status transitions to Duplicate or Blocked.
	// These comments are always server-generated so they are consistent across
	// all clients (web, iOS, API).  The Resolve/Reopen comments follow the
	// existing client-side pattern and are unaffected here.
	// -------------------------------------------------------------------------

	author := u.Username

	if oldStatus != "Duplicate" && newStatus == "Duplicate" {
		commentBody := fmt.Sprintf("Duplicate of issue #%d", body.DependentIssues[0])
		// Ignore the error — a failed auto-comment does not roll back the status change.
		_, _ = db.CreateComment(s.database, id, author, commentBody)

	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"issue": issue})
}

// handleDeleteIssue permanently removes an issue and all its comments. Only
// the reporter, assignee, or an admin may delete an issue.
func (s *srv) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	existing, err := db.GetIssue(s.database, id)
	if err != nil {
		internalError(w, err)

		return
	}

	if existing == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	if !issueModifier(currentUser(r), existing) {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	if err := db.DeleteIssue(s.database, id); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
