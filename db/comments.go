package db

import (
	"database/sql"
	"time"
)

// Comment represents a row in the comments table. issue_id links the comment
// to its parent issue; author stores the username (not display name) of the
// person who wrote it.
type Comment struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// ListComments returns all comments for a given issue in chronological order
// (oldest first, by id). Displaying comments in creation order is the
// conventional thread-style layout.
func ListComments(database *sql.DB, issueID int64) ([]Comment, error) {
	rows, err := database.Query(
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE issue_id = ? ORDER BY id ASC`, issueID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	// Return an empty slice rather than nil so JSON encoding produces "[]"
	// instead of "null" for issues with no comments yet.
	if comments == nil {
		comments = []Comment{}
	}
	return comments, rows.Err()
}

// DeleteComment removes a single comment by its primary-key ID. It is only
// exposed to admin users via the HTTP API.
func DeleteComment(database *sql.DB, commentID int64) error {
	_, err := database.Exec(`DELETE FROM comments WHERE id = ?`, commentID)
	return err
}

// CreateComment inserts a new comment and returns the fully populated Comment
// struct. Because database/sql's Exec does not return the inserted row, we
// re-fetch it with QueryRow after the INSERT using the auto-assigned id from
// LastInsertId.
func CreateComment(database *sql.DB, issueID int64, author, body string) (*Comment, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := database.Exec(
		`INSERT INTO comments (issue_id, author, body, created_at) VALUES (?, ?, ?, ?)`,
		issueID, author, body, now,
	)
	if err != nil {
		return nil, err
	}
	// LastInsertId is always populated after a successful INSERT in SQLite.
	// The second return value (error) is ignored because we know the driver
	// supports it.
	id, _ := result.LastInsertId()

	row := database.QueryRow(
		`SELECT id, issue_id, author, body, created_at FROM comments WHERE id = ?`, id,
	)
	var c Comment
	if err := row.Scan(&c.ID, &c.IssueID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}
