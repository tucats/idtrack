package db

import (
	"database/sql"
	"fmt"
	"time"
)

type Issue struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Reporter    string `json:"reporter"`
	Assignee    string `json:"assignee"`
	Priority    string `json:"priority"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func ListIssues(database *sql.DB, status, priority, search, sortCol, sortDir string) ([]Issue, error) {
	query := `SELECT id, title, description, reporter, assignee, priority, status, created_at, updated_at FROM issues WHERE 1=1`
	var args []interface{}

	switch status {
	case "open":
		query += ` AND status = 'Open'`
	case "resolved":
		query += ` AND status = 'Resolved'`
	}

	if priority != "" && priority != "all" {
		query += ` AND priority = ?`
		args = append(args, priority)
	}

	if search != "" {
		query += ` AND (title LIKE ? OR description LIKE ? OR reporter LIKE ? OR assignee LIKE ?)`
		s := "%" + search + "%"
		args = append(args, s, s, s, s)
	}

	validCols := map[string]bool{
		"id": true, "title": true, "priority": true, "status": true,
		"reporter": true, "assignee": true, "created_at": true, "updated_at": true,
	}
	if !validCols[sortCol] {
		sortCol = "id"
	}
	if sortDir != "asc" {
		sortDir = "desc"
	}
	query += fmt.Sprintf(` ORDER BY %s %s`, sortCol, sortDir)

	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []Issue
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}
	if issues == nil {
		issues = []Issue{}
	}
	return issues, rows.Err()
}

func GetIssue(database *sql.DB, id int64) (*Issue, error) {
	row := database.QueryRow(
		`SELECT id, title, description, reporter, assignee, priority, status, created_at, updated_at FROM issues WHERE id = ?`, id,
	)
	var i Issue
	if err := row.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.CreatedAt, &i.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &i, nil
}

func CreateIssue(database *sql.DB, title, description, reporter, assignee, priority string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if priority == "" {
		priority = "Medium"
	}
	result, err := database.Exec(
		`INSERT INTO issues (title, description, reporter, assignee, priority, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'Open', ?, ?)`,
		title, description, reporter, assignee, priority, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return GetIssue(database, id)
}

func UpdateIssue(database *sql.DB, id int64, title, description, priority, status, assignee string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.Exec(
		`UPDATE issues SET title=?, description=?, priority=?, status=?, assignee=?, updated_at=? WHERE id=?`,
		title, description, priority, status, assignee, now, id,
	)
	if err != nil {
		return nil, err
	}
	return GetIssue(database, id)
}

func DeleteIssue(database *sql.DB, id int64) error {
	if _, err := database.Exec(`DELETE FROM comments WHERE issue_id = ?`, id); err != nil {
		return err
	}
	_, err := database.Exec(`DELETE FROM issues WHERE id = ?`, id)
	return err
}
