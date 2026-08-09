package db

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// Querier is satisfied by both *sql.DB and *sql.Tx, letting callers choose
// whether a write should be part of a larger transaction (e.g. the bulk
// ingest command, which must be all-or-nothing) or run standalone.
//
// This is Go's structural typing ("duck typing") at work: Querier is not
// implemented by any explicit "implements" declaration the way you might
// write `class Foo implements Querier` in Java or C#. Instead, *any* type
// that happens to define methods with these exact names and signatures —
// Exec, QueryRow, Query — automatically satisfies this interface, with no
// declaration linking them together. Both *sql.DB (a connection pool) and
// *sql.Tx (a single transaction) define exactly these three methods, so both
// can be passed wherever a Querier is expected.
//
// This is what makes CreateIssue, GetIssue, UpdateIssue (below), and
// CreateComment (comments.go) dual-purpose: called with a *sql.DB directly,
// each write auto-commits immediately and independently; called with a
// *sql.Tx obtained from database.Begin(), the writes only become permanent
// when the caller commits the transaction, and any of them failing can be
// rolled back to undo everything since Begin(). commands.runIngestTx (in the
// commands package) relies on exactly this: it opens one *sql.Tx for an
// entire batch of issues/comments being bulk-imported from files, passes
// that *sql.Tx into these same db functions, and rolls the whole batch back
// if any single file fails to import — so a partially-bad batch never leaves
// the database partially written.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

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
	Format          string   `json:"format"`
	CommentCount    int      `json:"comment_count"`
	// DescriptionHTML is a transient, request-computed rendering of Description
	// according to Format. It is never stored in the database and is only
	// populated by handlers that need to return rendered content (e.g.
	// handleGetIssue); omitempty keeps it out of responses that don't set it.
	DescriptionHTML string `json:"description_html,omitempty"`
}

// validIssueFormats are the only accepted values for Issue.Format. Anything
// else (including empty string) normalizes to "text".
var validIssueFormats = map[string]bool{
	"text":     true,
	"markdown": true,
	"html":     true,
}

// NormalizeFormat maps any non-recognized format value to "text", the
// default. This keeps the stored value constrained to a known set even
// though SQLite has no native enum/check-constraint support here.
func NormalizeFormat(format string) string {
	if !validIssueFormats[format] {
		return "text"
	}

	return format
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
const issueColumns = `id, title, description, reporter, assignee, priority, status, project, component, created_at, updated_at, resolved_at, dependent_issues, teams, format, (SELECT COUNT(*) FROM comments WHERE issue_id = issues.id) AS comment_count`

// scanIssue reads a single issue row from any type that exposes Scan.
//
// The parameter type here — `interface { Scan(...any) error }` — is an
// anonymous (inline, unnamed) interface, another expression of Go's
// structural typing: rather than declaring a separate named interface type
// just to describe "anything with a Scan method," the interface is written
// directly in the function signature. Both *sql.Row (returned by QueryRow,
// used by GetIssue below) and *sql.Rows (returned by Query, used by
// ListIssues/ListChanges while iterating rows.Next()) have a matching Scan
// method, so this one helper works for both call sites and the column-list
// scanning logic only has to be written once.
func scanIssue(scanner interface {
	Scan(...any) error //nolint:inamedparam
}, i *Issue) error {
	var depStr, teamsStr string

	err := scanner.Scan(
		&i.ID, &i.Title, &i.Description, &i.Reporter, &i.Assignee,
		&i.Priority, &i.Status, &i.Project, &i.Component,
		&i.CreatedAt, &i.UpdatedAt, &i.ResolvedAt,
		&depStr,
		&teamsStr,
		&i.Format,
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
//
// This logic is factored out into its own function — rather than being
// written inline in both ListIssues and CountIssues — specifically so that
// the two can never drift apart. The UI shows "N of M issues" by combining
// CountIssues' total with a page of results from ListIssues; if the two
// functions built their WHERE clauses independently, an edit to one filter
// (say, adding a new status value) could easily be applied to only one of
// them, silently producing a total that doesn't match what the page-fetching
// query actually returns. Sharing one function makes that class of bug
// impossible: both callers always agree on which rows match, because they
// are asking the exact same question.
//
// The returned args slice uses "?" placeholders (see the note in
// UpgradePasswordHash in users.go on why placeholders are used for values),
// so every value here — status, priority, search text, project, team names —
// is safe against SQL injection no matter what the caller passes in.
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
// parameters.  status is one of "", "open", "resolved", "blocked",
// "duplicate" (see buildWhereClause); priority/project/search are optional
// filters ("" or "all" for priority/project means "no filter"); sortCol/
// sortDir select the ORDER BY (see the validOrders lookup table below).  When
// limit > 0, a "LIMIT ? OFFSET ?" clause is appended for pagination; limit ==
// 0 returns every matching row with no LIMIT clause at all.  Pass nil for
// userTeams to skip team filtering (caller already knows the user can see
// everything). Never returns a nil slice — an empty result set is []Issue{}.
func ListIssues(database *sql.DB, status, priority, search, project, sortCol, sortDir string, limit, offset int, userTeams []string) ([]Issue, error) {
	var issues []Issue

	where, args := buildWhereClause(status, priority, search, project, userTeams)

	// sortCol and sortDir both come from an HTTP query parameter
	// (server/issues.go), so they cannot be trusted as-is. Unlike the values
	// handled by buildWhereClause above, though, they can't be passed as "?"
	// placeholder arguments either — placeholders only substitute literal
	// values (like the string 'Open'), not SQL syntax like a column name or
	// the ASC/DESC keyword; "ORDER BY ?" is not valid SQL. So instead of
	// building the ORDER BY clause from the caller-supplied strings directly,
	// this map only ever produces one of a small, fixed set of hardcoded
	// literal clauses that were written into the source ahead of time. The
	// caller-supplied sortCol is used only as a lookup *key* into this map,
	// never concatenated into the query — so no value the caller sends can
	// ever inject arbitrary SQL, and any unrecognized column name harmlessly
	// falls through to the "id" default just below.
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

// ListChanges returns issues visible to userTeams whose updated_at is
// strictly after since. Deliberately NOT filtered by status/priority/search/
// project the way ListIssues is: filtering by an issue's *current* state
// cannot detect an issue that just transitioned away from matching a filter
// (e.g. Open → Resolved no longer matches status=Open), so a client needs to
// see the full set of team-visible changes and decide relevance itself
// (matchesCurrentFilters() in idtrack.js) — both to recognize new matches
// and to remove issues that just left its active filter. Pass nil for
// userTeams to skip team filtering.
func ListChanges(database *sql.DB, since string, userTeams []string) ([]Issue, error) {
	var issues []Issue

	if since == "" {
		return []Issue{}, nil
	}

	where, args := buildWhereClause("", "", "", "", userTeams)
	args = append(args, since)

	rows, err := database.Query(
		`SELECT `+issueColumns+` FROM issues`+where+` AND updated_at > ? ORDER BY updated_at ASC`,
		args...,
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
func GetIssue(database Querier, id int64) (*Issue, error) {
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

// CreateIssue inserts a new issue with status "Open" and teams="any", and
// returns the fully populated Issue as it now exists in the database
// (including its assigned ID and the comment_count computed by GetIssue).
// priority defaults to "Medium" when empty; format is normalized via
// NormalizeFormat (an unrecognized value silently becomes "text"). database
// may be a *sql.DB or a *sql.Tx — see the Querier doc comment above.
func CreateIssue(database Querier, title, description, reporter, assignee, priority, project, component, format string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	if priority == "" {
		priority = "Medium"
	}

	result, err := database.Exec(
		`INSERT INTO issues (title, description, reporter, assignee, priority, status, project, component, created_at, updated_at, teams, format)
		 VALUES (?, ?, ?, ?, ?, 'Open', ?, ?, ?, ?, 'any', ?)`,
		title, description, reporter, assignee, priority, project, component, now, now, NormalizeFormat(format),
	)
	if err != nil {
		return nil, err
	}

	// database/sql's Exec/Result has no way to return the row it just wrote —
	// unlike, say, "INSERT ... RETURNING *" in some other databases — so the
	// pattern used throughout this package is: grab the auto-assigned primary
	// key via LastInsertId, then issue a second query (here, GetIssue) to
	// fetch the complete row. LastInsertId's own error is ignored (`id, _ :=`)
	// because SQLite always populates it after a successful INSERT into a
	// table with an INTEGER PRIMARY KEY AUTOINCREMENT column. The same
	// two-step pattern appears again in CreateComment (comments.go).
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
func UpdateIssue(database Querier, id int64, title, description, priority, status, assignee, project, component, format string, dependentIssues []int64, teams []string) (*Issue, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	depStr := formatDependentIssues(dependentIssues)
	format = NormalizeFormat(format)

	// Both branches below (teams provided vs. teams left unchanged) contain
	// the same resolved_at = CASE ... END expression. A SQL CASE is the
	// equivalent of an if/else-if/else chain, evaluated once per row and
	// producing the value assigned to the column. Read in plain English:
	// "if the new status is Resolved or Duplicate AND resolved_at is
	// currently empty, set resolved_at to now; else if the new status is
	// Open or Blocked, clear resolved_at back to empty; otherwise (any other
	// status, or a Resolved/Duplicate issue that already has a resolved_at)
	// leave resolved_at exactly as it was." The "resolved_at = ''" condition
	// on the first branch is what stops re-saving an already-Resolved issue
	// (e.g. just editing its title) from stamping a new resolved_at every
	// time — only the actual transition into Resolved/Duplicate sets it, so
	// the original close time is preserved across later edits.
	if teams != nil {
		teamsStr := FormatTeams(teams)

		_, err := database.Exec(`
			UPDATE issues
			SET title=?, description=?, priority=?, status=?, assignee=?, project=?, component=?,
			    dependent_issues=?,
			    teams=?,
			    format=?,
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
			format,
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
			    format=?,
			    updated_at=?,
			    resolved_at = CASE
			        WHEN ? IN ('Resolved', 'Duplicate') AND resolved_at = '' THEN ?
			        WHEN ? IN ('Open', 'Blocked')                            THEN ''
			        ELSE resolved_at
			    END
			WHERE id=?`,
			title, description, priority, status, assignee, project, component,
			depStr,
			format,
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

// DeleteIssue removes an issue and all of its comments. The comment rows must
// be deleted explicitly, in a separate statement, before the issue row: by
// default SQLite does not enforce foreign-key constraints (that requires
// opting in with `PRAGMA foreign_keys = ON`, which this project does not do),
// so there is no automatic ON DELETE CASCADE to rely on — deleting the issue
// first would silently leave its comments behind as orphaned rows pointing at
// a nonexistent issue_id.
func DeleteIssue(database *sql.DB, id int64) error {
	if _, err := database.Exec(`DELETE FROM comments WHERE issue_id = ?`, id); err != nil {
		return err
	}

	_, err := database.Exec(`DELETE FROM issues WHERE id = ?`, id)

	return err
}
