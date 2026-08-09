package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// Team represents a row in the teams table.
type Team struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Reserved team names that cannot be renamed or deleted.
const (
	TeamAdmin = "admin"
	TeamAny   = "any"
)

// isReservedTeam reports whether name is a reserved team (case-insensitive).
func isReservedTeam(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))

	return lower == TeamAdmin || lower == TeamAny
}

// ParseTeams converts the comma-separated string stored in the DB to a
// []string of lower-cased names.  An empty or blank string returns an empty
// slice — never nil — so JSON encoding always produces "[]", not "null".
func ParseTeams(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}
	}

	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			result = append(result, p)
		}
	}

	return result
}

// FormatTeams converts a []string to the comma-separated lower-case string
// used for DB storage.  A nil or empty slice returns "any" as the safe default
// so rows are never left with an empty teams column after an explicit write.
func FormatTeams(teams []string) string {
	if len(teams) == 0 {
		return TeamAny
	}

	lower := make([]string, 0, len(teams))

	for _, t := range teams {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			lower = append(lower, t)
		}
	}

	if len(lower) == 0 {
		return TeamAny
	}

	return strings.Join(lower, ",")
}

// ContainsTeam reports whether teams contains name (case-insensitive).
func ContainsTeam(teams []string, name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, t := range teams {
		if strings.ToLower(t) == lower {
			return true
		}
	}

	return false
}

// IssueMatchesUserTeams returns true when the user is allowed to see the
// issue based on team membership.  The four-way rule:
//  1. User has "admin" team → sees everything.
//  2. User has "any" team → sees everything.
//  3. Issue carries "any" in its teams → any user can see it.
//  4. Intersection of user teams and issue teams is non-empty.
func IssueMatchesUserTeams(issueTeams, userTeams []string) bool {
	if ContainsTeam(userTeams, TeamAdmin) || ContainsTeam(userTeams, TeamAny) {
		return true
	}

	if ContainsTeam(issueTeams, TeamAny) {
		return true
	}

	for _, ut := range userTeams {
		if ContainsTeam(issueTeams, ut) {
			return true
		}
	}

	return false
}

// ProjectMatchesUserTeams applies the same four-way rule to a project's teams.
func ProjectMatchesUserTeams(projectTeams, userTeams []string) bool {
	return IssueMatchesUserTeams(projectTeams, userTeams)
}

// ListTeams returns all teams ordered by name.
func ListTeams(database *sql.DB) ([]Team, error) {
	var teams []Team

	rows, err := database.Query(`SELECT name, description FROM teams ORDER BY name`)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.Name, &t.Description); err != nil {
			return nil, err
		}

		teams = append(teams, t)
	}

	if teams == nil {
		teams = []Team{}
	}

	return teams, rows.Err()
}

// CreateTeam inserts a new team with the given name and optional description.
// Returns an error if the name is reserved or already exists.
func CreateTeam(database *sql.DB, name, description string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return fmt.Errorf("team name is required")
	}

	if isReservedTeam(name) {
		return fmt.Errorf("team name %q is reserved and cannot be created", name)
	}

	_, err := database.Exec(
		`INSERT INTO teams (name, description) VALUES (?, ?)`,
		name, description,
	)

	return err
}

// DeleteTeam removes a team by name.  Returns an error if the team is
// reserved or if any user, project, or issue still carries this team name.
func DeleteTeam(database *sql.DB, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))

	if isReservedTeam(name) {
		return fmt.Errorf("team %q is reserved and cannot be deleted", name)
	}

	// Same padded-LIKE technique used by CountAdmins in users.go: pad the
	// stored comma-separated list with a leading/trailing comma so the LIKE
	// pattern matches "name" as a whole list entry, not as a substring of a
	// longer team name.
	pattern := "%," + name + ",%"

	var userCount, projectCount, issueCount int

	if err := database.QueryRow(
		`SELECT COUNT(*) FROM users WHERE ',' || lower(teams) || ',' LIKE ?`, pattern,
	).Scan(&userCount); err != nil {
		return err
	}

	if err := database.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE ',' || lower(teams) || ',' LIKE ?`, pattern,
	).Scan(&projectCount); err != nil {
		return err
	}

	if err := database.QueryRow(
		`SELECT COUNT(*) FROM issues WHERE ',' || lower(teams) || ',' LIKE ?`, pattern,
	).Scan(&issueCount); err != nil {
		return err
	}

	if userCount+projectCount+issueCount > 0 {
		return fmt.Errorf(
			"team %q is still in use: %d user(s), %d project(s), %d issue(s) — reassign before deleting",
			name, userCount, projectCount, issueCount,
		)
	}

	_, err := database.Exec(`DELETE FROM teams WHERE name = ?`, name)

	return err
}

// UpdateTeam renames a team and/or updates its description.  Either newName or
// description may be empty to signal "no change".  A rename cascades to
// users.teams, projects.teams, and issues.teams in a single transaction.
// Returns an error if the team is reserved and a rename was requested.
func UpdateTeam(database *sql.DB, name, newName, description string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	newName = strings.ToLower(strings.TrimSpace(newName))

	if name == "" {
		return fmt.Errorf("team name is required")
	}

	doRename := newName != "" && newName != name
	doDesc := description != ""

	if !doRename && !doDesc {
		return nil // nothing to do
	}

	if doRename && isReservedTeam(name) {
		return fmt.Errorf("team %q is reserved and cannot be renamed", name)
	}

	tx, err := database.Begin()
	if err != nil {
		return err
	}

	defer tx.Rollback() //nolint:errcheck

	if doRename {
		// Rename in the teams registry.
		if _, err := tx.Exec(`UPDATE teams SET name = ? WHERE name = ?`, newName, name); err != nil {
			return err
		}

		// Cascade the rename to comma-separated team lists.  The replace()
		// approach handles the name appearing at the start, middle, and end of
		// the list, as long as the list is stored lower-case (which FormatTeams
		// guarantees).  We update all three tables.
		for _, tbl := range []string{"users", "projects", "issues"} {
			// Replace ",oldname," in the padded string, then strip padding.
			q := fmt.Sprintf(`
				UPDATE %s
				SET    teams = trim(
					replace(',' || teams || ',', ',%s,', ',%s,'),
					','
				)
				WHERE  ',' || lower(teams) || ',' LIKE ?
			`, tbl, name, newName)

			pattern := "%," + name + ",%"

			if _, err := tx.Exec(q, pattern); err != nil {
				return err
			}
		}
	}

	if doDesc {
		target := name
		if doRename {
			target = newName
		}

		if _, err := tx.Exec(`UPDATE teams SET description = ? WHERE name = ?`, description, target); err != nil {
			return err
		}
	}

	return tx.Commit()
}
