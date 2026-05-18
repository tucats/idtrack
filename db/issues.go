package db

import (
	"database/sql"
	"time"
)

// Issue represents a row in the issues table plus its JSON serialization.
// All timestamps are stored and returned as RFC3339 strings (e.g.
// "2026-05-16T12:00:00Z") rather than time.Time, which keeps the DB schema
// and the JSON API consistent without any conversion layer.
type Issue struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Reporter     string `json:"reporter"`
	Assignee     string `json:"assignee"`
	Priority     string `json:"priority"`
	Status       string `json:"status"`
	Project      string `json:"project"`
	Component    string `json:"component"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	ResolvedAt   string `json:"resolved_at"`
	CommentCount int    `json:"comment_count"`
}

// issueColumns is the SELECT column list shared by ListIssues, ListChanges,
// and GetIssue. The correlated subquery for comment_count runs once per row;
// the idx_comments_issue_id index keeps it fast.
const issueColumns = `id, title, description, reporter, assignee, priority, status, project, component, created_at, updated_at, resolved_at, (SELECT COUNT(*) FROM comments WHERE issue_id = issues.id) AS comment_count`

// buildWhereClause constructs the WHERE clause and argument slice shared by
// ListIssues and CountIssues. "WHERE 1=1" lets us unconditionally append
// "AND ..." clauses without tracking whether this is the first one.
func buildWhereClause(status, priority, search, project string) (string, []interface{}) {
	where := ` WHERE 1=1`
	var args []interface{}

	switch status {
	case "open":
		where += ` AND status = 'Open'`
	case "resolved":
		where += ` AND status = 'Resolved'`
	}

	if priority != "" && priority != "all" {
		where += ` AND priority = ?`
		args = append(args, priority)
	}

	if project != "" && project != "all" {
		where += ` AND project = ?`
		args = append(args, project)
	}

	if search != "" {
		// Search across all text columns. Each "?" must have a matching value
		// in args, hence six copies of the wrapped search term.
		where += ` AND (title LIKE ? OR description LIKE ? OR reporter LIKE ? OR assignee LIKE ? OR project LIKE ? OR component LIKE ?)`
		s := "%" + search + "%"
		args = append(args, s, s, s, s, s, s)
	}

	return where, args
}

// ListIssues returns issues filtered and sorted according to the provided
// parameters. All parameters are optional — empty strings / zero ints mean
// "no filter / no limit". When limit > 0, LIMIT/OFFSET pagination is applied.
//
// The query is built dynamically: we start with a base SELECT and accumulate
// WHERE clauses and argument values in parallel slices. Using "?" placeholders
// (never string interpolation for user-supplied values) prevents SQL injection.
func ListIssues(database *sql.DB, status, priority, search, project, sortCol, sortDir string, limit, offset int) ([]Issue, error) {
	var issues []Issue

	where, args := buildWhereClause(status, priority, search, project)

	// SQL placeholders ("?") can only bind literal values — they cannot
	// substitute column names or keywords such as ASC/DESC.  To prevent
	// injection we use a lookup table whose values are all hardcoded string
	// literals.  sortCol and sortDir are used only as lookup keys; neither
	// is ever interpolated into the query string, which breaks the data-flow
	// path that static analysis tools track from HTTP parameters to SQL.
	//
	// Index 0 = ASC clause, index 1 = DESC clause.
	validOrders := map[string][2]string{
		"id":          {" ORDER BY id ASC",          " ORDER BY id DESC"},
		"title":       {" ORDER BY title ASC",       " ORDER BY title DESC"},
		"priority":    {" ORDER BY priority ASC",    " ORDER BY priority DESC"},
		"status":      {" ORDER BY status ASC",      " ORDER BY status DESC"},
		"reporter":    {" ORDER BY reporter ASC",    " ORDER BY reporter DESC"},
		"assignee":    {" ORDER BY assignee ASC",    " ORDER BY assignee DESC"},
		"created_at":  {" ORDER BY created_at ASC",  " ORDER BY created_at DESC"},
		"updated_at":  {" ORDER BY updated_at ASC",  " ORDER BY updated_at DESC"},
		"project":     {" ORDER BY project ASC",     " ORDER BY project DESC"},
		"component":   {" ORDER BY component ASC",   " ORDER BY component DESC"},
		"resolved_at": {" ORDER BY resolved_at ASC", " ORDER BY resolved_at DESC"},
	}

	clauses, ok := validOrders[sortCol]
	if !ok {
		clauses = validOrders["id"] // unknown column — fall back to a safe default
	}

	order := clauses[1] // default DESC
	if sortDir == "asc" {
		order = clauses[0]
	}

	query := `SELECT ` + issueColumns + ` FROM issues` + where + order

	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}

	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.Project, &i.Component, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt, &i.CommentCount); err != nil {
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

// CountIssues returns the total number of issues matching the given filters.
// It uses the same WHERE clause logic as ListIssues so the count always
// corresponds to the paginated result set.
func CountIssues(database *sql.DB, status, priority, search, project string) (int, error) {
	where, args := buildWhereClause(status, priority, search, project)
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM issues`+where, args...).Scan(&n)
	return n, err
}

// ListChanges returns all issues whose updated_at is strictly after since.
// The since value must be an RFC3339 timestamp string; an empty string
// returns no rows. Results are ordered oldest-first so the caller can
// update _lastSeenAt incrementally.
func ListChanges(database *sql.DB, since string) ([]Issue, error) {
	if since == "" {
		return []Issue{}, nil
	}

	rows, err := database.Query(
		`SELECT `+issueColumns+` FROM issues WHERE updated_at > ? ORDER BY updated_at ASC`,
		since,
	)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var issues []Issue
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.Project, &i.Component, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt, &i.CommentCount); err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}

	if issues == nil {
		issues = []Issue{}
	}

	return issues, rows.Err()
}

// GetIssue fetches a single issue by its integer ID. Returns (nil, nil) when
// no row with that ID exists — callers should treat a nil return as a 404.
func GetIssue(database *sql.DB, id int64) (*Issue, error) {
	var i Issue

	row := database.QueryRow(
		`SELECT `+issueColumns+` FROM issues WHERE id = ?`, id,
	)

	if err := row.Scan(&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee, &i.Priority, &i.Status, &i.Project, &i.Component, &i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt, &i.CommentCount); err != nil {
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
// UpdateIssue replaces all editable fields of an issue in one statement and
// sets updated_at to now. Like CreateIssue, it re-reads the row afterwards so
// the returned struct reflects the committed state.
//
// resolved_at is managed automatically:
//   - Transitioning to Resolved: set to now, but only if it is currently empty
//     (preserves the original resolved timestamp if the issue is re-saved while
//     already Resolved — e.g. editing the description without changing status).
//   - Transitioning to Open: always cleared to '' so a later resolution gets a
//     fresh timestamp.
//   - Any other status change: ELSE branch leaves resolved_at unchanged.
func UpdateIssue(database *sql.DB, id int64, title, description, priority, status, assignee, project, component string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := database.Exec(`
		UPDATE issues
		SET title=?, description=?, priority=?, status=?, assignee=?, project=?, component=?,
		    updated_at=?,
		    resolved_at = CASE
		        WHEN ? = 'Resolved' AND resolved_at = '' THEN ?
		        WHEN ? = 'Open'                          THEN ''
		        ELSE resolved_at
		    END
		WHERE id=?`,
		title, description, priority, status, assignee, project, component,
		now,
		status, now, // CASE: set resolved_at when transitioning to Resolved
		status,      // CASE: clear resolved_at when transitioning to Open
		id,
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
