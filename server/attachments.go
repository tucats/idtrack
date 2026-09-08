package server

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// attachmentFormField is the multipart form field name an upload must use
// for the file, on both attachment-creation routes. Named generically
// ("file", not "image") because uploads are no longer image-only — see
// db.AttachmentType.
const attachmentFormField = "file"

// attachmentID parses the {aid} path parameter shared by every
// /api/attachments/{aid}... route. Unlike issueID/comment IDs this is a
// UUID string, not a numeric value, so the only validation possible here is
// "non-empty" — a bogus-but-well-formed UUID is caught downstream when
// db.GetAttachment finds no matching row and the handler returns 404.
func attachmentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("aid")
	if id == "" {
		jsonError(w, "invalid attachment id", http.StatusBadRequest)

		return "", false
	}

	return id, true
}

// readUploadedAttachment extracts the uploaded file from a multipart form
// (field name attachmentFormField), sniffs and converts it via
// processUploadedAttachment (server/images.go), and writes an appropriate
// error response on any failure. ok is false whenever an error has already
// been written to w.
func readUploadedAttachment(w http.ResponseWriter, r *http.Request) (kind db.AttachmentType, stored, thumb []byte, width, height int, filename string, ok bool) {
	// The in-memory-vs-temp-file threshold below only controls how
	// ParseMultipartForm buffers the request as it reads it; the request
	// body itself is already capped at maxAttachmentBodyBytes by the
	// limitBody middleware (server/middleware.go), so this can't be used to
	// bypass that limit.
	if err := r.ParseMultipartForm(maxAttachmentBodyBytes); err != nil {
		jsonError(w, "invalid multipart upload", http.StatusBadRequest)

		return "", nil, nil, 0, 0, "", false
	}

	file, header, err := r.FormFile(attachmentFormField)
	if err != nil {
		jsonError(w, "missing '"+attachmentFormField+"' file field", http.StatusBadRequest)

		return "", nil, nil, 0, 0, "", false
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, "failed to read uploaded file", http.StatusBadRequest)

		return "", nil, nil, 0, 0, "", false
	}

	k, storedBytes, thumbBytes, w2, h2, err := processUploadedAttachment(data, header.Filename)
	if err != nil {
		switch {
		case errors.Is(err, errUnsupportedAttachment):
			jsonError(w, "unsupported file format — only PNG/JPEG images, PDF, and plain text are accepted", http.StatusUnsupportedMediaType)
		case errors.Is(err, errImageTooLarge):
			jsonError(w, "image dimensions too large", http.StatusBadRequest)
		default:
			internalError(w, err)
		}

		return "", nil, nil, 0, 0, "", false
	}

	return k, storedBytes, thumbBytes, w2, h2, header.Filename, true
}

// handleCreateIssueAttachment serves POST /api/issues/{id}/attachments —
// attaches an uploaded file (image, PDF, or text — see db.AttachmentType)
// to an issue's description. Auth: any authenticated user, matching
// handleCreateComment's "any signed-in user may add content to any issue
// they can reach" rule.
//
// Request: multipart/form-data with the file in the "file" field.
// Response (201 Created): {"attachment": {...}} — metadata only, no file
// bytes (see db.Attachment's doc comment); fetch the file itself via
// GET /api/attachments/{id} or its thumbnail via /{id}/thumbnail.
//
// Errors: 400 invalid issue id / malformed upload; 404 issue does not exist;
// 415 the uploaded data doesn't sniff as a supported kind (see
// detectAttachmentKind in server/images.go); 500 on db error.
func (s *srv) handleCreateIssueAttachment(w http.ResponseWriter, r *http.Request) {
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

	kind, stored, thumb, width, height, filename, ok := readUploadedAttachment(w, r)
	if !ok {
		return
	}

	attachment, err := db.CreateAttachment(s.database, id, 0, currentUser(r).Username, filename, kind, stored, thumb, width, height)
	if err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{"attachment": attachment})
}

// handleCreateCommentAttachment serves
// POST /api/issues/{id}/comments/{cid}/attachments — attaches an uploaded
// file to one specific comment. Auth and request/response shape are
// identical to handleCreateIssueAttachment; the only difference is the
// parent-existence check also confirms {cid} is a real comment belonging to
// {id}, the same pattern handleCreateComment uses for {id} alone (see its
// doc comment on why this check exists — comments/attachments have no
// database-level foreign key for SQLite to enforce automatically).
//
// Errors: 400 invalid issue/comment id or malformed upload; 404 the issue
// does not exist, or the comment does not exist under that issue; 415
// unsupported file data; 500 on db error.
func (s *srv) handleCreateCommentAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil || cid <= 0 {
		jsonError(w, "invalid comment id", http.StatusBadRequest)

		return
	}

	comment, err := db.GetComment(s.database, cid)
	if err != nil {
		internalError(w, err)

		return
	}

	if comment == nil || comment.IssueID != id {
		jsonError(w, "comment not found", http.StatusNotFound)

		return
	}

	kind, stored, thumb, width, height, filename, ok := readUploadedAttachment(w, r)
	if !ok {
		return
	}

	attachment, err := db.CreateAttachment(s.database, id, cid, currentUser(r).Username, filename, kind, stored, thumb, width, height)
	if err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{"attachment": attachment})
}

// handleListAttachments serves GET /api/issues/{id}/attachments. Auth: any
// authenticated user — matches handleGetIssue, which likewise only requires
// authentication rather than re-checking team visibility (that filter is
// applied at the list-query level for the issue list itself, not per-issue
// GETs; see buildWhereClause).
//
// Response (200 OK): {"attachments": [...]} — metadata only (id, filename,
// type, dimensions, uploader, comment_id when present, timestamps); no file
// bytes. The frontend renders each entry's thumbnail via an <img> tag
// pointed at GET /api/attachments/{id}/thumbnail rather than embedding
// thumbnail bytes in this response, so the browser can cache/lazy-load each
// thumbnail independently instead of the whole list paying for every one up
// front.
//
// Errors: 400 invalid issue id; 404 issue does not exist; 500 on db error.
func (s *srv) handleListAttachments(w http.ResponseWriter, r *http.Request) {
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

	attachments, err := db.ListAttachments(s.database, id)
	if err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"attachments": attachments})
}

// defaultDispositionFilename returns a sensible placeholder filename, with
// an extension matching t, for an attachment that was somehow stored with
// no filename at all.
func defaultDispositionFilename(t db.AttachmentType) string {
	switch t {
	case db.AttachmentPDF:
		return "document.pdf"
	case db.AttachmentText:
		return "text.txt"
	default:
		return "image.png"
	}
}

// sanitizeDispositionFilename strips CR/LF and other control characters from
// a stored (user-supplied) filename before it is placed in a
// Content-Disposition response header, preventing header injection. The
// stored filename itself is never used as a filesystem path — attachments
// are stored entirely as database blobs — so this is the only place it
// needs sanitizing.
func sanitizeDispositionFilename(name string, t db.AttachmentType) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' {
			return -1
		}

		return r
	}, name)

	if name == "" {
		return defaultDispositionFilename(t)
	}

	return name
}

// attachmentContentType maps an attachment's stored kind to the Content-Type
// its full blob should be served with. Thumbnails are always PNG regardless
// of the parent attachment's type (see genericThumbnail/textThumbnail in
// server/images.go), so this is only consulted for the full-size blob.
func attachmentContentType(t db.AttachmentType) string {
	switch t {
	case db.AttachmentPDF:
		return "application/pdf"
	case db.AttachmentText:
		return "text/plain; charset=utf-8"
	default:
		return "image/png"
	}
}

// serveAttachmentBlob is the shared tail of handleGetAttachmentImage and
// handleGetAttachmentThumbnail: look up the attachment's metadata (for its
// filename/type and to 404 on a bogus/deleted id), fetch the requested blob
// column via fetch, and write it as a response with the appropriate
// Content-Type. thumbnail is true when serving the always-PNG thumbnail
// column rather than the type-dependent full blob.
func (s *srv) serveAttachmentBlob(w http.ResponseWriter, r *http.Request, thumbnail bool, fetch func(*srv, string) ([]byte, error)) {
	id, ok := attachmentID(w, r)
	if !ok {
		return
	}

	attachment, err := db.GetAttachment(s.database, id)
	if err != nil {
		internalError(w, err)

		return
	}

	if attachment == nil {
		jsonError(w, "attachment not found", http.StatusNotFound)

		return
	}

	data, err := fetch(s, id)
	if err != nil {
		internalError(w, err)

		return
	}

	if data == nil {
		jsonError(w, "attachment not found", http.StatusNotFound)

		return
	}

	contentType := "image/png"
	if !thumbnail {
		contentType = attachmentContentType(attachment.Type)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+sanitizeDispositionFilename(attachment.Filename, attachment.Type)+`"`)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// handleGetAttachmentImage serves GET /api/attachments/{aid} — the
// full-size stored blob (a converted PNG for an image attachment, or the
// raw original bytes for a PDF/text attachment — see db.AttachmentType).
// Auth: any authenticated user (see handleListAttachments' doc comment on
// the team-visibility precedent this follows). Cache-Control is long-lived
// and "immutable" because an attachment's bytes never change after upload —
// only a DELETE removes the row entirely, which naturally invalidates any
// cached copy the next time it's requested and 404s.
//
// Errors: 400 missing {aid}; 404 no such attachment; 500 on db error.
func (s *srv) handleGetAttachmentImage(w http.ResponseWriter, r *http.Request) {
	s.serveAttachmentBlob(w, r, false, (*srv).fetchAttachmentImage)
}

// handleGetAttachmentThumbnail serves GET /api/attachments/{aid}/thumbnail —
// the smaller preview PNG (every attachment kind has one — see
// server/images.go's genericThumbnail/textThumbnail for the non-image
// kinds). Same auth/caching/error behavior as handleGetAttachmentImage.
func (s *srv) handleGetAttachmentThumbnail(w http.ResponseWriter, r *http.Request) {
	s.serveAttachmentBlob(w, r, true, (*srv).fetchAttachmentThumbnail)
}

func (s *srv) fetchAttachmentImage(id string) ([]byte, error) {
	return db.GetAttachmentImage(s.database, id)
}

func (s *srv) fetchAttachmentThumbnail(id string) ([]byte, error) {
	return db.GetAttachmentThumbnail(s.database, id)
}

// handleDeleteAttachment serves DELETE /api/attachments/{aid}. Auth: the
// attachment's own uploader, or an admin — deliberately looser than
// handleDeleteComment's admin-only rule (see the "Delete permission"
// decision in the attachments feature plan): a user can undo their own
// mistaken upload without needing an admin, the same way issueModifier lets
// an issue's reporter/assignee (not just an admin) modify it.
//
// Request: no body. Response (200 OK): {"ok": true}.
//
// Errors: 400 missing {aid}; 404 no such attachment; 403 authenticated but
// neither the uploader nor an admin; 500 on db error.
func (s *srv) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r)
	if !ok {
		return
	}

	attachment, err := db.GetAttachment(s.database, id)
	if err != nil {
		internalError(w, err)

		return
	}

	if attachment == nil {
		jsonError(w, "attachment not found", http.StatusNotFound)

		return
	}

	user := currentUser(r)
	if !user.IsAdmin && user.Username != attachment.Uploader {
		jsonError(w, "forbidden", http.StatusForbidden)

		return
	}

	if err := db.DeleteAttachment(s.database, id); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
