package db

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"time"

	"github.com/google/uuid"
)

// AttachmentType enumerates the kinds of file an attachment's blob column may
// hold. New values can be added here as support for more file kinds is
// added; a value not recognized by the server falls back to the generic
// icon+filename thumbnail (see server/images.go's genericThumbnail) rather
// than failing.
type AttachmentType string

const (
	AttachmentImage AttachmentType = "image" // blob is a converted PNG
	AttachmentPDF   AttachmentType = "pdf"   // blob is the raw, unconverted PDF
	AttachmentText  AttachmentType = "text"  // blob is the raw text bytes
)

// Attachment represents a row in the attachments table: one file (image,
// PDF, or text) attached to an issue's description or to one of its
// comments. The image and thumbnail blob columns are deliberately not
// fields on this struct — listing an issue's attachments should never pull
// file bytes off disk, so callers fetch a blob explicitly via
// GetAttachmentImage/GetAttachmentThumbnail only when they actually need to
// serve it. The "image" blob column name predates non-image attachment
// types and is kept as-is rather than renamed, to avoid an unnecessary
// column-rename migration — it holds whatever the Type field says it holds.
type Attachment struct {
	ID        string         `json:"id"`
	IssueID   int64          `json:"issue_id"`
	CommentID int64          `json:"comment_id,omitempty"` // 0 (omitted) = attached to the issue description, not a comment
	Uploader  string         `json:"uploader"`
	Filename  string         `json:"filename"`
	Type      AttachmentType `json:"type"`
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	Size      int64          `json:"size"`
	Pages     int            `json:"pages,omitempty"` // PDF page count; 0 for non-PDF types, or a PDF whose count hasn't been computed/cached yet — see SetAttachmentPages
	CreatedAt string         `json:"created_at"`
}

const attachmentColumns = "id, issue_id, comment_id, uploader, filename, type, width, height, size, pages, created_at"

// compressMinSize is the smallest blob CreateAttachment will even attempt to
// gzip. Below this, there's no realistic way to clear compressMinSavings —
// gzip's own header/footer overhead alone is about 18-20 bytes, so trying on
// anything this small is just wasted CPU with no possible payoff.
const compressMinSize = 1024

// compressMinSavings is the minimum number of bytes gzip must actually save,
// compared to the original, for the compressed form to be kept. Below this,
// the original bytes are stored instead — this is what keeps compression
// from being applied to a blob that doesn't meaningfully benefit from it
// (most obviously: image attachments are already-compressed PNG, and many
// PDFs already Flate-compress their own internal streams, so gzipping the
// whole file again on top often saves little or nothing), which would
// otherwise cost every future read a decompression pass for no real storage
// benefit.
const compressMinSavings = 1024

// compressBlob gzips data and returns (compressed bytes, true) if doing so
// saves at least compressMinSavings bytes versus the original; otherwise it
// returns (data, false) unchanged, meaning "store as-is." Only the main
// attachment blob (the "image" column, holding the actual file content) is
// ever compressed — the thumbnail is always a small, already-PNG-encoded
// preview that's rarely near compressMinSize in the first place and
// wouldn't benefit from a second compression pass on top of PNG's own, so
// it's never passed through this function.
func compressBlob(data []byte) (stored []byte, compressed bool) {
	if len(data) < compressMinSize {
		return data, false
	}

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return data, false
	}

	if err := gw.Close(); err != nil {
		return data, false
	}

	if len(data)-buf.Len() < compressMinSavings {
		return data, false
	}

	return buf.Bytes(), true
}

// decompressBlob reverses compressBlob: it returns data unchanged when
// compressed is false (the common case, and always true for any attachment
// created before this feature existed — see the compressed column's
// addColumnIfMissing default in db.go), or gunzips it when true.
func decompressBlob(data []byte, compressed bool) ([]byte, error) {
	if !compressed {
		return data, nil
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	return io.ReadAll(gr)
}

func scanAttachment(scanner interface {
	Scan(...any) error //nolint:inamedparam
}, a *Attachment) error {
	return scanner.Scan(&a.ID, &a.IssueID, &a.CommentID, &a.Uploader, &a.Filename, &a.Type, &a.Width, &a.Height, &a.Size, &a.Pages, &a.CreatedAt)
}

// CreateAttachment inserts a new attachment row. file and thumbnail are the
// already-processed bytes to store (see server/images.go's
// processUploadedAttachment): a converted PNG for AttachmentImage, or the
// raw original bytes for AttachmentPDF/AttachmentText; width/height are only
// meaningful for AttachmentImage and are 0 otherwise. pages is only
// meaningful for AttachmentPDF (the page count computed at upload time from
// the just-uploaded bytes — see server/images.go's processUploadedAttachment)
// and is 0 otherwise. commentID is 0 when the attachment belongs to the
// issue's description rather than a specific comment.
//
// file is transparently gzip-compressed before storage when compressBlob
// determines it's worth it (see that function's doc comment); the caller
// never needs to know either way — GetAttachmentImage reverses this on
// read, so every caller above this package always sees the original,
// uncompressed bytes. The returned Attachment's Size field (and the stored
// size column) is always len(file), the original byte count — the
// meaningful, user-facing "how big is this file" answer, independent of
// whatever this package chose to do with it on disk.
//
// Returns the fully populated Attachment (metadata only).
func CreateAttachment(database *sql.DB, issueID, commentID int64, uploader, filename string, attachmentType AttachmentType, file, thumbnail []byte, width, height, pages int) (*Attachment, error) {
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	stored, compressed := compressBlob(file)

	compressedInt := 0
	if compressed {
		compressedInt = 1
	}

	_, err := database.Exec(
		`INSERT INTO attachments (id, issue_id, comment_id, uploader, filename, type, width, height, size, pages, compressed, image, thumbnail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, issueID, commentID, uploader, filename, attachmentType, width, height, len(file), pages, compressedInt, stored, thumbnail, now,
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
		Type:      attachmentType,
		Width:     width,
		Height:    height,
		Size:      int64(len(file)),
		Pages:     pages,
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

// GetAttachmentImage returns the full-size original bytes for an attachment
// (a converted PNG for AttachmentImage, or the raw original bytes for
// AttachmentPDF/AttachmentText — see the Attachment.Type doc comment), or
// nil if id does not exist. Transparently gunzips the stored blob first when
// the row's compressed flag is set (see CreateAttachment/compressBlob) — the
// caller always gets back exactly the bytes originally passed to
// CreateAttachment, never needing to know whether this package chose to
// compress them on disk.
func GetAttachmentImage(database *sql.DB, id string) ([]byte, error) {
	var (
		image         []byte
		compressedInt int
	)

	err := database.QueryRow(`SELECT image, compressed FROM attachments WHERE id = ?`, id).Scan(&image, &compressedInt)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return decompressBlob(image, compressedInt != 0)
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

// SetAttachmentPages caches a PDF attachment's page count once it has been
// computed (server/pdf.go), so it never needs to be recomputed by reopening
// and reparsing the stored PDF bytes on a later request. This is the lazy
// counterpart to the page count already computed and stored at upload time
// for new attachments (CreateAttachment's pages parameter): a PDF uploaded
// before this feature existed has pages == 0 until the first request that
// needs its page count computes it and calls this to cache the result —
// deliberately lazy rather than an eager startup backfill (unlike the
// string-default backfills in db.go's initSchema), since computing a page
// count means opening and parsing every stored PDF, which could be slow or
// even fail for a database with many/large attachments.
func SetAttachmentPages(database *sql.DB, id string, pages int) error {
	_, err := database.Exec(`UPDATE attachments SET pages = ? WHERE id = ?`, pages, id)

	return err
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
