package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// handleCreateComment adds a comment to an existing issue. The author is always
// set to the authenticated user — clients cannot post as someone else. The
// issue must exist; a non-existent issue ID returns 404 rather than creating
// an orphaned comment row (S-12).
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

	author := currentUser(r).Username

	comment, err := db.CreateComment(s.database, id, author, body.Body)
	if err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{"comment": comment})
}

// handleDeleteComment removes a single comment by its ID. Admin-only.
// The comment ID (cid) is a separate path parameter from the issue ID (id).
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
