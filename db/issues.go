package db

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// Issue represents a row in the issues table plus its JSON serialization.
type Issue struct {
	ID              int64    `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Reporter        string   `json:"reporter"`
	Assignee        string   `json:"assignee"`
	Priority        string   `json:"priority"`
	Status          string   `json:"status"`
	Project         string   `json:"project"`
	Component       string   `json:"component"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	ResolvedAt      string   `json:"resolved_at"`
	DependentIssues []int64  `json:"dependent_issues"`
	Teams           []string `json:"teams"`
	CommentCount    int      `json:"comment_count"`
}

// parseDependentIssues converts the comma-separated string stored in the DB
// to a []int64.  An empty or blank string returns an empty slice — never nil.
func parseDependentIssues(s string) []int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return []int64{}
	}

	parts := strings.Split(s, ",")
	result := make([]int64, 0, len(parts))

	for _, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err == nil && n > 0 {
			result = append(result, n)
		}
	}

	return result
}

// formatDependentIssues converts a []int64 to the comma-separated string
// used for DB storage.  An empty or nil slice returns "".
func formatDependentIssues(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}

	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}

	return strings.Join(parts, ",")
}

// issueColumns is the SELECT column list shared by ListIssues, ListChanges,
// and GetIssue.
const issueColumns = `id, title, description, reporter, assignee, priority, status, project, component, created_at, updated_at, resolved_at, dependent_issues, teams, (SELECT COUNT(*) FROM comments WHERE issue_id = issues.id) AS comment_count`

// scanIssue reads a single issue row from any type that exposes Scan.
func scanIssue(scanner interface {
	Scan(...any) error //golint: inamedparam
}, i *Issue) error {
	var depStr, teamsStr string

	err := scanner.Scan(
		&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee,
		&i.Priority, &i.Status, &i.Project, &i.Component,
		&i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt,
		&depStr,
		&teamsStr,
		&i.CommentCount,
	)
	if err != nil {
		return err
	}

	i.DependentIssues = parseDependentIssues(depStr)
	i.Teams = ParseTeams(teamsStr)

	return nil
}

// buildWhereClause constructs the WHERE clause and argument slice shared by
// ListIssues and CountIssues.  userTeams is the calling user's team membership;
// pass nil to skip team filtering (used when the caller has already determined
// the user can see everything, e.g. admin/any users).
func buildWhereClause(status, priority, search, project string, userTeams []string) (string, []interface{}) {
	var args []interface{}

	where := ` WHERE 1=1`

	switch status {
	case "open":
		where += ` AND status = 'Open'`
	case "resolved":
		where += ` AND status = 'Resolved'`
	case "blocked":
		where += ` AND status = 'Blocked'`
	case "duplicate":
		where += ` AND status = 'Duplicate'`
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
		where += ` AND (title LIKE ? OR description LIKE ? OR reporter LIKE ? OR assignee LIKE ? OR project LIKE ? OR component LIKE ?)`
		s := "%" + search + "%"
		args = append(args, s, s, s, s, s, s)
	}

	// Team filtering: skip entirely when the user has admin or any team.
	// Otherwise, add an OR condition: issue has 'any', or shares a team with
	// the user.  The padded-LIKE pattern prevents false substring matches.
	if len(userTeams) > 0 && !ContainsTeam(userTeams, TeamAdmin) && !ContainsTeam(userTeams, TeamAny) {
		clauses := []string{`',' || lower(teams) || ',' LIKE ?`}

		args = append(args, "%,any,%")

		for _, t := range userTeams {
			clauses = append(clauses, `',' || lower(teams) || ',' LIKE ?`)
			args = append(args, "%,"+strings.ToLower(t)+",%")
		}

		where += " AND (" + strings.Join(clauses, " OR ") + ")"
	}

	return where, args
}

// ListIssues returns issues filtered and sorted according to the provided
// parameters.  Pass nil for userTeams to skip team filtering.
func ListIssues(database *sql.DB, status, priority, search, project, sortCol, sortDir string, limit, offset int, userTeams []string) ([]Issue, error) {
	var issues []Issue

	where, args := buildWhereClause(status, priority, search, project, userTeams)

	validOrders := map[string][2]string{
		"id":          {" ORDER BY id ASC", " ORDER BY id DESC"},
		"title":       {" ORDER BY title ASC", " ORDER BY title DESC"},
		"priority":    {" ORDER BY priority ASC", " ORDER BY priority DESC"},
		"status":      {" ORDER BY status ASC", " ORDER BY status DESC"},
		"reporter":    {" ORDER BY reporter ASC", " ORDER BY reporter DESC"},
		"assignee":    {" ORDER BY assignee ASC", " ORDER BY assignee DESC"},
		"created_at":  {" ORDER BY created_at ASC", " ORDER BY created_at DESC"},
		"updated_at":  {" ORDER BY updated_at ASC", " ORDER BY updated_at DESC"},
		"project":     {" ORDER BY project ASC", " ORDER BY project DESC"},
		"component":   {" ORDER BY component ASC", " ORDER BY component DESC"},
		"resolved_at": {" ORDER BY resolved_at ASC", " ORDER BY resolved_at DESC"},
	}

	clauses, ok := validOrders[sortCol]
	if !ok {
		clauses = validOrders["id"]
	}

	order := clauses[1]
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
		if err := scanIssue(rows, &i); err != nil {
			return nil, err
		}

		issues = append(issues, i)
	}

	if issues == nil {
		issues = []Issue{}
	}

	return issues, rows.Err()
}

// CountIssues returns the total number of issues matching the given filters.
// Pass nil for userTeams to skip team filtering.
func CountIssues(database *sql.DB, status, priority, search, project string, userTeams []string) (int, error) {
	var n int

	where, args := buildWhereClause(status, priority, search, project, userTeams)
	err := database.QueryRow(`SELECT COUNT(*) FROM issues`+where, args...).Scan(&n)

	return n, err
}

// ListChanges returns all issues whose updated_at is strictly after since.
func ListChanges(database *sql.DB, since string) ([]Issue, error) {
	var issues []Issue

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

	for rows.Next() {
		var i Issue
		if err := scanIssue(rows, &i); err != nil {
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
// no row with that ID exists.
func GetIssue(database *sql.DB, id int64) (*Issue, error) {
	var i Issue

	row := database.QueryRow(
		`SELECT `+issueColumns+` FROM issues WHERE id = ?`, id,
	)

	if err := scanIssue(row, &i); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	return &i, nil
}

// CreateIssue inserts a new issue with status "Open" and teams="any".
func CreateIssue(database *sql.DB, title, description, reporter, assignee, priority, project, component string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	if priority == "" {
		priority = "Medium"
	}

	result, err := database.Exec(
		`INSERT INTO issues (title, description, reporter, assignee, priority, status, project, component, created_at, updated_at, teams)
		 VALUES (?, ?, ?, ?, ?, 'Open', ?, ?, ?, ?, 'any')`,
		title, description, reporter, assignee, priority, project, component, now, now,
	)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()

	return GetIssue(database, id)
}

// UpdateIssue replaces all editable fields of an issue.  When teams is
// non-nil, the issue's team list is replaced; pass nil to leave it unchanged.
//
// resolved_at is managed automatically by status:
//   - Transitioning to Resolved or Duplicate: set to now if currently empty.
//   - Transitioning to Open or Blocked: always cleared.
//   - Any other value: left unchanged.
func UpdateIssue(database *sql.DB, id int64, title, description, priority, status, assignee, project, component string, dependentIssues []int64, teams []string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	depStr := formatDependentIssues(dependentIssues)

	if teams != nil {
		teamsStr := FormatTeams(teams)

		_, err := database.Exec(`
			UPDATE issues
			SET title=?, description=?, priority=?, status=?, assignee=?, project=?, component=?,
			    dependent_issues=?,
			    teams=?,
			    updated_at=?,
			    resolved_at = CASE
			        WHEN ? IN ('Resolved', 'Duplicate') AND resolved_at = '' THEN ?
			        WHEN ? IN ('Open', 'Blocked')                            THEN ''
			        ELSE resolved_at
			    END
			WHERE id=?`,
			title, description, priority, status, assignee, project, component,
			depStr,
			teamsStr,
			now,
			status, now,
			status,
			id,
		)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := database.Exec(`
			UPDATE issues
			SET title=?, description=?, priority=?, status=?, assignee=?, project=?, component=?,
			    dependent_issues=?,
			    updated_at=?,
			    resolved_at = CASE
			        WHEN ? IN ('Resolved', 'Duplicate') AND resolved_at = '' THEN ?
			        WHEN ? IN ('Open', 'Blocked')                            THEN ''
			        ELSE resolved_at
			    END
			WHERE id=?`,
			title, description, priority, status, assignee, project, component,
			depStr,
			now,
			status, now,
			status,
			id,
		)
		if err != nil {
			return nil, err
		}
	}

	return GetIssue(database, id)
}

// DeleteIssue removes an issue and all of its comments.
func DeleteIssue(database *sql.DB, id int64) error {
	if _, err := database.Exec(`DELETE FROM comments WHERE issue_id = ?`, id); err != nil {
		return err
	}

	_, err := database.Exec(`DELETE FROM issues WHERE id = ?`, id)

	return err
}
