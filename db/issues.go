package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Issue represents a row in the issues table plus its JSON serialisation.
// All timestamps are stored and returned as RFC3339 strings (e.g.
// "2026-05-16T12:00:00Z") rather than time.Time, which keeps the DB schema
// and the JSON API consistent without any conversion layer.
type Issue struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Reporter    string `json:"reporter"`
	Assignee    string `json:"assignee"`
	Priority    string `json:"priority"`
	Status      string `json:"status"`
	Project     string `json:"project"`
	Component   string `json:"component"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ListIssues returns issues filtered and sorted according to the provided
// parameters. All parameters are optional — empty strings mean "no filter".
//
// The query is built dynamically: we start with a base SELECT and accumulate
// WHERE clauses and argument values in parallel slices. Using "?" placeholders
// (never string interpolation for user-supplied values) prevents SQL injection.
//
// status: "open" | "resolved" | "" (all)
// priority: "High" | "Medium" | "Low" | "" | "all" (all)
// search: substring matched against title, description, reporter, assignee, project, component
// sortCol: column name to sort by (validated against an allowlist)
// sortDir: "asc" | "desc" (defaults to "desc")
func ListIssues(database *sql.DB, status, priority, search, sortCol, sortDir string) ([]Issue, error) {
	// "WHERE 1=1" is a common trick that lets us unconditionally append
	// "AND ..." clauses without needing to track whether this is the first one.
	query := `SELECT id, title, description, reporter, assignee, priority, status, project, component, created_at, updated_at FROM issues WHERE 1=1`
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
		// Search across all text columns. Each "?" must have a matching value
		// in args, hence six copies of the wrapped search term.
		query += ` AND (title LIKE ? OR description LIKE ? OR reporter LIKE ? OR assignee LIKE ? OR project LIKE ? OR component LIKE ?)`
		s := "%" + search + "%"
		args = append(args, s, s, s, s, s, s)
	}

	// Validate sortCol against an allowlist of known column names before
	// interpolating it into the query string. We cannot use a "?" placeholder
	// for column names in SQL, so we must guard against injection ourselves.
	validCols := map[string]bool{
		"id": true, "title": true, "priority": true, "status": true,
		"reporter": true, "assignee": true, "created_at": true, "updated_at": true,
		"project": true, "component": true,
	}
	if !validCols[sortCol] {
		sortCol = "id" // unknown column — fall back to a safe default
	}
	if sortDir != "asc" {
		sortDir = "desc" // only accept "asc"; anything else defaults to "desc"
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
		if err := rows.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.Project, &i.Component, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}
	// Return an empty slice (not nil) so the JSON response is "[]" not "null".
	if issues == nil {
		issues = []Issue{}
	}
	return issues, rows.Err()
}

// GetIssue fetches a single issue by its integer ID. Returns (nil, nil) when
// no row with that ID exists — callers should treat a nil return as a 404.
func GetIssue(database *sql.DB, id int64) (*Issue, error) {
	row := database.QueryRow(
		`SELECT id, title, description, reporter, assignee, priority, status, project, component, created_at, updated_at FROM issues WHERE id = ?`, id,
	)
	var i Issue
	if err := row.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.Project, &i.Component, &i.CreatedAt, &i.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &i, nil
}

// CreateIssue inserts a new issue with status "Open" and the current UTC time
// for both created_at and updated_at. After the insert it re-reads the row via
// GetIssue so the caller receives the complete record including the
// auto-assigned ID. Priority defaults to "Medium" when not specified.
func CreateIssue(database *sql.DB, title, description, reporter, assignee, priority, project, component string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if priority == "" {
		priority = "Medium"
	}
	result, err := database.Exec(
		`INSERT INTO issues (title, description, reporter, assignee, priority, status, project, component, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'Open', ?, ?, ?, ?)`,
		title, description, reporter, assignee, priority, project, component, now, now,
	)
	if err != nil {
		return nil, err
	}
	// LastInsertId returns the row ID assigned by AUTOINCREMENT. We ignore the
	// error because SQLite always populates this after a successful INSERT.
	id, _ := result.LastInsertId()
	return GetIssue(database, id)
}

// UpdateIssue replaces all editable fields of an issue in one statement and
// sets updated_at to now. Like CreateIssue, it re-reads the row afterwards so
// the returned struct reflects the committed state.
func UpdateIssue(database *sql.DB, id int64, title, description, priority, status, assignee, project, component string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.Exec(
		`UPDATE issues SET title=?, description=?, priority=?, status=?, assignee=?, project=?, component=?, updated_at=? WHERE id=?`,
		title, description, priority, status, assignee, project, component, now, id,
	)
	if err != nil {
		return nil, err
	}
	return GetIssue(database, id)
}

// DeleteIssue removes an issue and all of its comments. Comments are deleted
// first because SQLite does not enforce foreign key constraints by default (it
// requires PRAGMA foreign_keys = ON), so we perform the cascade manually to
// avoid orphaned comment rows.
func DeleteIssue(database *sql.DB, id int64) error {
	if _, err := database.Exec(`DELETE FROM comments WHERE issue_id = ?`, id); err != nil {
		return err
	}
	_, err := database.Exec(`DELETE FROM issues WHERE id = ?`, id)
	return err
}
