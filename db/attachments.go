package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// Attachment represents a row in the attachments table: one image attached to
// an issue's description or to one of its comments. The image and thumbnail
// blob columns are deliberately not fields on this struct — listing an
// issue's attachments should never pull image bytes off disk, so callers
// fetch a blob explicitly via GetAttachmentImage/GetAttachmentThumbnail only
// when they actually need to serve it.
type Attachment struct {
	ID        string `json:"id"`
	IssueID   int64  `json:"issue_id"`
	CommentID int64  `json:"comment_id,omitempty"` // 0 (omitted) = attached to the issue description, not a comment
	Uploader  string `json:"uploader"`
	Filename  string `json:"filename"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
}

const attachmentColumns = "id, issue_id, comment_id, uploader, filename, width, height, size, created_at"

func scanAttachment(scanner interface {
	Scan(...any) error //nolint:inamedparam
}, a *Attachment) error {
	return scanner.Scan(&a.ID, &a.IssueID, &a.CommentID, &a.Uploader, &a.Filename, &a.Width, &a.Height, &a.Size, &a.CreatedAt)
}

// CreateAttachment inserts a new attachment row. image and thumbnail are the
// already-converted PNG bytes (see server/images.go); commentID is 0 when the
// attachment belongs to the issue's description rather than a specific
// comment. Returns the fully populated Attachment (metadata only).
func CreateAttachment(database *sql.DB, issueID, commentID int64, uploader, filename string, image, thumbnail []byte, width, height int) (*Attachment, error) {
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := database.Exec(
		`INSERT INTO attachments (id, issue_id, comment_id, uploader, filename, width, height, size, image, thumbnail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, issueID, commentID, uploader, filename, width, height, len(image), image, thumbnail, now,
	)
	if err != nil {
		return nil, err
	}

	return &Attachment{
		ID:        id,
		IssueID:   issueID,
		CommentID: commentID,
		Uploader:  uploader,
		Filename:  filename,
		Width:     width,
		Height:    height,
		Size:      int64(len(image)),
		CreatedAt: now,
	}, nil
}

// ListAttachments returns every attachment belonging to an issue — both those
// attached to the description (CommentID == 0) and to any of its comments —
// ordered by creation time. Used by the "list images" endpoint; the frontend
// distinguishes description vs. comment attachments via CommentID.
func ListAttachments(database *sql.DB, issueID int64) ([]Attachment, error) {
	rows, err := database.Query(
		`SELECT `+attachmentColumns+` FROM attachments WHERE issue_id = ? ORDER BY created_at ASC`, issueID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attachments []Attachment

	for rows.Next() {
		var a Attachment

		if err := scanAttachment(rows, &a); err != nil {
			return nil, err
		}

		attachments = append(attachments, a)
	}

	if attachments == nil {
		attachments = []Attachment{}
	}

	return attachments, rows.Err()
}

// GetAttachment returns one attachment's metadata (no blob columns), or nil
// if id does not exist. Handlers use this to check ownership/existence
// before serving or deleting an attachment's image bytes.
func GetAttachment(database *sql.DB, id string) (*Attachment, error) {
	var a Attachment

	err := scanAttachment(database.QueryRow(`SELECT `+attachmentColumns+` FROM attachments WHERE id = ?`, id), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &a, nil
}

// GetAttachmentImage returns the full-size PNG bytes for an attachment, or
// nil if id does not exist.
func GetAttachmentImage(database *sql.DB, id string) ([]byte, error) {
	var image []byte

	err := database.QueryRow(`SELECT image FROM attachments WHERE id = ?`, id).Scan(&image)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	return image, err
}

// GetAttachmentThumbnail returns the thumbnail PNG bytes for an attachment,
// or nil if id does not exist.
func GetAttachmentThumbnail(database *sql.DB, id string) ([]byte, error) {
	var thumbnail []byte

	err := database.QueryRow(`SELECT thumbnail FROM attachments WHERE id = ?`, id).Scan(&thumbnail)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	return thumbnail, err
}

// DeleteAttachment removes a single attachment by its UUID.
func DeleteAttachment(database *sql.DB, id string) error {
	_, err := database.Exec(`DELETE FROM attachments WHERE id = ?`, id)

	return err
}

// DeleteAttachmentsByIssue removes every attachment belonging to an issue.
// Called from db.DeleteIssue alongside its existing comment cleanup, since
// (like comments) attachments have no database-level foreign key back to
// issues for SQLite to enforce automatically.
func DeleteAttachmentsByIssue(database *sql.DB, issueID int64) error {
	_, err := database.Exec(`DELETE FROM attachments WHERE issue_id = ?`, issueID)

	return err
}

// DeleteAttachmentsByComment removes every attachment belonging to a single
// comment (CommentID == 0 attachments, which belong to the issue description
// itself, are untouched). Called from db.DeleteComment so deleting a comment
// doesn't orphan the images attached to it.
func DeleteAttachmentsByComment(database *sql.DB, commentID int64) error {
	_, err := database.Exec(`DELETE FROM attachments WHERE comment_id = ?`, commentID)

	return err
}
