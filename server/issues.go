package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// Status values for an issue.
const (
	StatusOpen      = "Open"
	StatusResolved  = "Resolved"
	StatusBlocked   = "Blocked"
	StatusDuplicate = "Duplicate"
)

// maxSearchLen caps the search query parameter to prevent callers from sending
// arbitrarily long patterns that force a full table scan on every column (S-10).
const maxSearchLen = 200

// handleListIssues serves GET /api/issues. Auth: any authenticated user (no
// admin check) — results are restricted per-user by team visibility (see
// below), not by an all-or-nothing permission check.
//
// It reads optional query parameters (via r.URL.Query(), which parses the
// "?key=value&..." portion of the URL into a map-like Values type) and
// delegates filtering, sorting, and pagination to db.ListIssues /
// db.CountIssues. All filtering is done in SQL rather than in Go to keep
// memory usage low for large issue lists — this handler never loads the
// full issues table into the server's memory.
//
// Query parameters (all optional):
//
//	status   open|resolved|blocked|duplicate — filter by status
//	priority High|Medium|Low                 — filter by priority
//	project  <name>                          — filter by project
//	search   <text>                          — full-text substring match (capped at maxSearchLen)
//	sort     <column>                        — column to sort by
//	order    asc|desc                        — sort direction
//	limit    <n>                             — page size (0 = return all, the legacy/default mode)
//	offset   <n>                             — rows to skip for pagination
//
// Team visibility: currentUser(r).Teams is passed through to db.ListIssues
// and db.CountIssues so the SQL WHERE clause itself excludes issues the
// caller's teams cannot see (an admin or "any"-team member sees everything;
// see CLAUDE.md's "Team-based access control" section for the full rule).
// This handler does not need to filter results itself — the database only
// ever returns rows the caller is entitled to.
//
// Response (200 OK): {"issues": [...], "total": N, "offset": N, "limit": N}.
// When limit > 0, total is a separate COUNT(*) query result (the total
// number of matching rows across all pages, for a "N of M" UI); when
// limit == 0, total is simply len(issues) since every matching row was
// already returned.
//
// Errors: 400 if search exceeds maxSearchLen, or limit/offset are not
// non-negative integers; 500 on any db error.
func (s *srv) handleListIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	search := q.Get("search")
	if len(search) > maxSearchLen {
		jsonError(w, "search parameter exceeds maximum length of 200 characters", http.StatusBadRequest)

		return
	}

	limit, offset := 0, 0

	// Query string values arrive as strings; strconv.Atoi ("ASCII to
	// integer") parses one into an int and returns a non-nil error if the
	// text isn't a valid integer. This "parse, check error, bail out" shape
	// — a value combined with an error, checked immediately with an
	// early return — is the standard way Go signals failure: there are no
	// exceptions, so every call that can fail returns an error value that
	// the caller must explicitly check.
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

	status := q.Get("status")
	priority := q.Get("priority")
	project := q.Get("project")
	sortCol := q.Get("sort")
	sortDir := q.Get("order")

	// Pass the calling user's teams so the DB layer can filter issues they
	// cannot see.  Admin and "any" users are handled inside buildWhereClause
	// (no filter applied when the user has either of those teams).
	userTeams := currentUser(r).Teams

	// When paginating, run a COUNT query first so the client knows the total
	// number of matching rows without fetching them all.
	total := 0

	if limit > 0 {
		var err error

		total, err = db.CountIssues(s.database, status, priority, search, project, userTeams)
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)

			return
		}
	}

	issues, err := db.ListIssues(s.database, status, priority, search, project, sortCol, sortDir, limit, offset, userTeams)
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

// handleListChanges serves GET /api/issues/changes. Auth: any authenticated
// user; results are restricted by team visibility the same way as
// handleListIssues. Registered in server.go before the wildcard
// "GET /api/issues/{id}" pattern so the literal "/changes" path segment is
// matched here rather than being captured as an {id} value.
//
// Query parameter: since (required) — an RFC3339 timestamp string. Returns
// issues whose updated_at is strictly after that value, restricted only by
// the caller's team visibility. Used by the frontend to poll for changes
// made by other users without discarding the current scroll state.
//
// Response (200 OK): {"issues": [...]}, ordered by updated_at ascending.
// Errors: 400 if since is missing; 500 on db error.
//
// Deliberately NOT filtered by status/priority/project/search: filtering by
// an issue's current state can't represent "this issue just stopped matching
// your filter" (e.g. Open → Resolved no longer matches status=Open), which
// the client needs to know in order to remove that issue from a filtered
// view. The frontend applies status/priority/project/search relevance
// client-side (matchesCurrentFilters() in idtrack.js) against this broader,
// team-filtered result set — both to recognize new matches and to detect
// issues that left the active filter.
func (s *srv) handleListChanges(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	if since == "" {
		jsonError(w, "since parameter is required", http.StatusBadRequest)

		return
	}

	userTeams := currentUser(r).Teams

	issues, err := db.ListChanges(s.database, since, userTeams)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issues": issues,
	})
}

// handleCreateIssue serves POST /api/issues. Auth: any authenticated user —
// there is no admin requirement to file a new issue.
//
// Request body (JSON): {"title", "description", "priority", "assignee",
// "project", "component", "format"}. title is required (non-blank after
// trimming); all other fields are optional and take their database defaults
// when omitted (see the issues table schema in CLAUDE.md — priority
// defaults to "Medium", status always starts as "Open", format defaults to
// "text"). The reporter field is never taken from the request body — it is
// always set to the authenticated caller's own username
// (currentUser(r).Username), so a client cannot file an issue that appears
// to be reported by someone else.
//
// When project is non-blank, the handler re-fetches the full project list
// and confirms the named project both exists and is visible to the caller's
// teams (db.ProjectMatchesUserTeams) before allowing the issue to be
// created against it — this prevents a user from filing an issue into a
// project they are not permitted to see, even though the create-issue
// endpoint itself has no admin gate.
//
// Response (201 Created): {"issue": {...}} — the newly created issue record
// as returned by db.CreateIssue.
//
// Errors: 400 missing title; 403 the named project doesn't exist or isn't
// visible to the caller; 500 on any other db error.
func (s *srv) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Assignee    string `json:"assignee"`
		Project     string `json:"project"`
		Component   string `json:"component"`
		Format      string `json:"format"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)

		return
	}

	u := currentUser(r)

	// Validate that the chosen project is accessible to this user.
	if body.Project != "" {
		projects, err := db.ListProjects(s.database)
		if err != nil {
			internalError(w, err)

			return
		}

		found := false

		for _, p := range projects {
			if p.Name == body.Project && db.ProjectMatchesUserTeams(p.Teams, u.Teams) {
				found = true

				break
			}
		}

		if !found {
			jsonError(w, "project not found or not accessible", http.StatusForbidden)

			return
		}
	}

	issue, err := db.CreateIssue(s.database, body.Title, body.Description, u.Username, body.Assignee, body.Priority, body.Project, body.Component, body.Format)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	// Notify the assignee, unless they're the one who just filed it. Fires
	// as its own goroutine (see notify.go) so a slow or failing push never
	// delays this response.
	if issue.Assignee != "" && issue.Assignee != issue.Reporter {
		title := fmt.Sprintf("New Issue #%d", issue.ID)
		notifyBody := fmt.Sprintf("%s assigned you: %s", u.DisplayName, issue.Title)

		go s.notify([]string{issue.Assignee}, notifyCategoryNewIssue, title, notifyBody, issue.ID)
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{"issue": issue})
}

// handleGetIssue serves GET /api/issues/{id}. Auth: any authenticated user —
// note there is no team-visibility check here (unlike handleListIssues):
// any authenticated user who knows or guesses a valid issue ID can fetch it
// directly. This is a pre-existing gap relative to the list endpoint's
// team filtering; see the note in the final report for this review.
//
// Path parameter: {id} — parsed and validated by the shared issueID() helper
// in helpers.go, which writes the 400 response itself and returns ok=false
// on a missing/non-numeric/non-positive value; callers just check ok and
// return.
//
// Returns a single issue together with all of its comments in one response,
// so the frontend can display the full detail view without a second
// round-trip.
//
// Response (200 OK): {"issue": {...}, "comments": [...]}. The issue's
// DescriptionHTML field and each comment's BodyHTML field are populated here
// via renderFormatted (see server/render.go) based on the issue's format
// ("text" | "markdown" | "html") — these are transient, request-scoped
// fields (json:"...,omitempty", never persisted to the database) that let
// the client render formatted content without a separate render request.
//
// Errors: 400 invalid id; 404 no issue with that id; 500 on db error.
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

	// Render the description and each comment body per the issue's format.
	// renderFormatted returns "" for plain text, since the frontend already
	// handles escaping and whitespace for that case.
	issue.DescriptionHTML = renderFormatted(issue.Format, issue.Description)

	for i := range comments {
		comments[i].BodyHTML = renderFormatted(issue.Format, comments[i].Body)
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issue":    issue,
		"comments": comments,
	})
}

// issueModifier returns true when the user is authorized to edit or delete the
// given issue. Admins, the original reporter, and the current assignee may all
// make changes; any other authenticated user is a read-only third party. Both
// handleUpdateIssue and handleDeleteIssue call this after fetching the
// current issue record, so the check is always evaluated against the issue's
// state as it exists right now, not against any (possibly different) values
// the client is trying to write.
func issueModifier(u *db.User, issue *db.Issue) bool {
	return u.IsAdmin || u.Username == issue.Reporter || u.Username == issue.Assignee
}

// handleUpdateIssue serves PUT /api/issues/{id}. Auth: any authenticated
// user may attempt the request, but the handler itself enforces via
// issueModifier (above) that only the issue's reporter, its current
// assignee, or an admin may actually apply changes — everyone else gets 403.
//
// It replaces all editable fields of an issue. All fields must be sent in
// the request body — this is a full replacement (PUT semantics), not a
// partial update (PATCH semantics): a client that omits a field is sending
// an empty value for it, not "leave unchanged."
//
// Path parameter: {id}, parsed via issueID() (see helpers.go).
//
// Request body (JSON): {"title", "description", "priority", "status",
// "assignee", "project", "component", "format", "dependent_issues",
// "teams", "comment"}. teams is special-cased: it is only applied when
// non-empty, and only an admin may actually change it (a non-admin sending
// back the issue's existing team list is fine — see teamsEqual below —
// but sending a different list is rejected with 403). comment is not a
// column on the issue at all; it exists only for symmetry with the
// frontend's dialog flow (see CLAUDE.md's "Status-change dialogs" section)
// but is not read anywhere in this handler — the client posts status-change
// comments itself via a separate POST to the comments endpoint.
//
// Additional rules for the new status values:
//
//   - Duplicate: dependent_issues must contain exactly one existing issue ID.
//     The server auto-posts a "Duplicate of issue #N" comment on transition.
//
//   - Blocked: dependent_issues must contain at least one existing issue ID.
//     Unlike Duplicate, the server does NOT auto-post a comment for this
//     transition — the frontend posts a "Blocked by issues #N[, #M...]"
//     comment itself (seeded text plus any user additions) as a separate
//     request after this PUT succeeds; the `comment` body field on this
//     endpoint is not used by this handler at all (see above). Non-admins
//     may only ADD entries to an already-blocked issue's dependent_issues —
//     they cannot remove entries.
//
//   - Open (from Blocked): all entries in the current dependent_issues must
//     have status Resolved before the transition is allowed (HTTP 409 otherwise).
//
//   - Open or Resolved: dependent_issues is cleared automatically by this
//     handler regardless of what the client sends.
//
// Response (200 OK): {"issue": {...}} — the updated issue as returned by
// db.UpdateIssue, with DescriptionHTML populated the same way as
// handleGetIssue.
//
// Errors: 400 invalid id, missing title, or an invalid dependent_issues
// list for the requested status; 403 the caller is not the reporter,
// assignee, or an admin, a non-admin tried to remove a Blocked dependency,
// or a non-admin tried to change teams; 404 issue not found (either the
// target issue or a referenced dependent_issues entry that doesn't exist);
// 409 attempting Blocked→Open while a dependency is still unresolved.
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
		Format          string  `json:"format"`
		DependentIssues []int64 `json:"dependent_issues"`
		// Teams replaces the issue's team list when provided. Only admins may
		// change teams; a non-admin sending a different list receives 403.
		Teams   []string `json:"teams"`
		Comment string   `json:"comment"`
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
	case StatusDuplicate:
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

	case StatusBlocked:
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
		if oldStatus == StatusBlocked && !u.IsAdmin {
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

	case StatusOpen:
		// Transitioning from Blocked to Open requires every blocking issue to
		// be Resolved.  This rule ensures a blocked issue cannot be re-opened
		// until the work it depends on is actually complete.
		if oldStatus == StatusBlocked {
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

				if dep.Status != StatusResolved {
					jsonError(w, fmt.Sprintf("cannot unblock: issue #%d is still %s", depID, dep.Status), http.StatusConflict)

					return
				}
			}
		}

		// Clear dependent_issues when reopening so the field doesn't carry
		// stale data from a previous Blocked or Duplicate state.
		body.DependentIssues = nil

	case StatusResolved:
		// Clear dependent_issues on resolution for the same reason.
		body.DependentIssues = nil
	}

	// -------------------------------------------------------------------------
	// Validate the teams change: only admins may modify the teams list.
	// -------------------------------------------------------------------------

	var newTeams []string // nil = leave unchanged

	if len(body.Teams) > 0 {
		if !u.IsAdmin {
			// Check whether the client is trying to change teams.
			teamsChanged := !teamsEqual(existing.Teams, body.Teams)
			if teamsChanged {
				jsonError(w, "only admins may change issue teams", http.StatusForbidden)

				return
			}
		}

		newTeams = body.Teams
	}

	// -------------------------------------------------------------------------
	// Persist the update.
	// -------------------------------------------------------------------------

	issue, err := db.UpdateIssue(s.database, id,
		body.Title, body.Description, body.Priority, body.Status,
		body.Assignee, body.Project, body.Component, body.Format,
		body.DependentIssues,
		newTeams,
	)
	if err != nil {
		internalError(w, err)

		return
	}

	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	// Populate the transient rendered-HTML field so the client can refresh its
	// description preview from this response without a second round-trip.
	issue.DescriptionHTML = renderFormatted(issue.Format, issue.Description)

	// -------------------------------------------------------------------------
	// Auto-generate a comment for status transitions to Duplicate or Blocked.
	// These comments are always server-generated so they are consistent across
	// all clients (web, iOS, API).  The Resolve/Reopen comments follow the
	// existing client-side pattern and are unaffected here.
	// -------------------------------------------------------------------------

	author := u.Username

	if oldStatus != StatusDuplicate && newStatus == StatusDuplicate {
		commentBody := fmt.Sprintf("Duplicate of issue #%d", body.DependentIssues[0])
		// Ignore the error — a failed auto-comment does not roll back the status change.
		_, _ = db.CreateComment(s.database, id, author, commentBody)
	}

	// Notify whichever of the reporter/assignee did NOT make this change,
	// for every status transition (not just ->Resolved — see
	// docs/NOTIFICATIONS.md's resolved decisions). Uses existing's
	// pre-update reporter/assignee, and u.Username as the actor, since those
	// are the two roles the requirement is defined in terms of regardless of
	// whether this same PUT also changed the assignee.
	if oldStatus != newStatus {
		var recipients []string

		if existing.Reporter != "" && existing.Reporter != author {
			recipients = append(recipients, existing.Reporter)
		}

		if existing.Assignee != "" && existing.Assignee != author {
			recipients = append(recipients, existing.Assignee)
		}

		if len(recipients) > 0 {
			title := fmt.Sprintf("Issue #%d: %s", id, newStatus)
			notifyBody := fmt.Sprintf("%s changed status from %s to %s: %s", u.DisplayName, oldStatus, newStatus, existing.Title)

			go s.notify(recipients, notifyCategoryStatusChanged, title, notifyBody, id)
		}
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"issue": issue})
}

// teamsEqual reports whether two team slices contain the same elements
// (order-independent, case-insensitive).
func teamsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	m := make(map[string]struct{}, len(a))
	for _, t := range a {
		m[strings.ToLower(t)] = struct{}{}
	}

	for _, t := range b {
		if _, ok := m[strings.ToLower(t)]; !ok {
			return false
		}
	}

	return true
}

// handleDeleteIssue serves DELETE /api/issues/{id}. Auth: any authenticated
// user may attempt the request, but issueModifier (above) restricts the
// actual delete to the issue's reporter, its current assignee, or an admin —
// same authorization rule as handleUpdateIssue.
//
// Path parameter: {id}, parsed via issueID().
//
// db.DeleteIssue permanently removes the issue row and, first, all comments
// attached to it — SQLite here has no foreign-key constraint wired up
// between comments.issue_id and issues.id (that requires
// "PRAGMA foreign_keys = ON", which this project does not enable), so the
// application code does that cleanup manually rather than relying on a
// cascading delete at the database level.
//
// Request: no body.
// Response (200 OK): {"ok": true}.
//
// Errors: 400 invalid id; 403 caller is not the reporter/assignee/admin;
// 404 no issue with that id; 500 on db error.
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
