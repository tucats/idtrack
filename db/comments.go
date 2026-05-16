package db

import (
	"database/sql"
	"time"
)

type Comment struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

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
	if comments == nil {
		comments = []Comment{}
	}
	return comments, rows.Err()
}

func DeleteComment(database *sql.DB, commentID int64) error {
	_, err := database.Exec(`DELETE FROM comments WHERE id = ?`, commentID)
	return err
}

func CreateComment(database *sql.DB, issueID int64, author, body string) (*Comment, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := database.Exec(
		`INSERT INTO comments (issue_id, author, body, created_at) VALUES (?, ?, ?, ?)`,
		issueID, author, body, now,
	)
	if err != nil {
		return nil, err
	}
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
