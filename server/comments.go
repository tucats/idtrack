package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleCreateComment serves POST /api/issues/{id}/comments. Auth: any
// authenticated user — any signed-in user may comment on any issue they can
// reach (there is no reporter/assignee/admin restriction here, unlike
// updating or deleting the issue itself).
//
// Path parameter: {id} — the issue to comment on, parsed via issueID().
// Request body (JSON): {"body"} — the comment text; required (rejected if
// blank after trimming).
//
// The author field is never taken from the request body — it is always set
// to the authenticated caller's own username (currentUser(r).Username), the
// same pattern handleCreateIssue uses for reporter, so a client cannot post
// a comment that appears to come from someone else.
//
// Parent-existence check (prevents orphaned comments): before inserting
// anything, the handler calls db.GetIssue(id) and returns 404 if it comes
// back nil. This check exists because the comments table has no database-
// level foreign key back to issues — SQLite does not enforce foreign keys
// unless "PRAGMA foreign_keys = ON" is set, which this project does not do
// (see db.DeleteIssue's manual comment cleanup for the same reason) — so
// without this explicit check, POSTing to a bogus or already-deleted issue
// ID would silently succeed and leave a comment row pointing at an issue_id
// that doesn't exist in the issues table. Checking here, at the one place
// comments are created, is simpler than adding real referential-integrity
// enforcement at the database layer.
//
// Response (201 Created): {"comment": {...}} — the newly created comment
// record, including its assigned id.
//
// Errors: 400 invalid issue id or blank body; 404 the issue does not exist;
// 500 on db error.
func (s *srv) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	issue, err := db.GetIssue(s.database, id)
	if err != nil {
		internalError(w, err)

		return
	}

	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)

		return
	}

	var body struct {
		Body string `json:"body"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if strings.TrimSpace(body.Body) == "" {
		jsonError(w, "comment body is required", http.StatusBadRequest)

		return
	}

	u := currentUser(r)
	author := u.Username

	comment, err := db.CreateComment(s.database, id, author, body.Body)
	if err != nil {
		internalError(w, err)

		return
	}

	// Notify the reporter and/or assignee, but only when they're different
	// people — a single-person issue has no "other involved party" to tell
	// about a comment — and never the comment's own author.
	if issue.Reporter != "" && issue.Assignee != "" && issue.Reporter != issue.Assignee {
		var recipients []string

		if issue.Reporter != author {
			recipients = append(recipients, issue.Reporter)
		}

		if issue.Assignee != author {
			recipients = append(recipients, issue.Assignee)
		}

		if len(recipients) > 0 {
			title := fmt.Sprintf("New Comment on #%d", id)
			notifyBody := fmt.Sprintf("%s commented: %s", u.DisplayName, body.Body)

			go s.notify(recipients, notifyCategoryNewComment, title, notifyBody, id)
		}
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{"comment": comment})
}

// handleDeleteComment serves DELETE /api/issues/{id}/comments/{cid}. Auth:
// authenticated session plus an in-handler admin-only check — unlike
// creating a comment (open to any authenticated user), removing one is
// restricted to admins, matching the "Trash icon only for admins" rule
// described in CLAUDE.md's frontend Admin UI section.
//
// Path parameters: {id} is the parent issue (present in the route pattern
// for REST-ful nesting but not actually read or validated by this handler —
// deletion is keyed purely off {cid}); {cid} is the comment ID itself,
// parsed the same way issueID() parses {id} elsewhere in this package
// (strconv.ParseInt, base 10, into an int64, rejecting non-positive values).
//
// Request: no body.
// Response (200 OK): {"ok": true}.
//
// Errors: 403 not an admin; 400 invalid comment id; 500 on db error. Note
// db.DeleteComment does not report whether a row with that ID actually
// existed — deleting an already-deleted or bogus (but well-formed) comment
// ID still returns 200.
func (s *srv) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	raw := r.PathValue("cid")

	cid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cid <= 0 {
		jsonError(w, "invalid comment id", http.StatusBadRequest)

		return
	}

	if err := db.DeleteComment(s.database, cid); err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
