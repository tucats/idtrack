package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// Project represents a project row combined with its list of component names
// and its team visibility list.
type Project struct {
	Name       string   `json:"name"`
	Components []string `json:"components"`
	Teams      []string `json:"teams"`
}

// ListProjects returns every project in alphabetical order, each with its
// component names and teams pre-populated.
func ListProjects(database *sql.DB) ([]Project, error) {
	var projects []Project

	rows, err := database.Query(`SELECT name, teams FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var (
			p        Project
			teamsStr string
		)

		if err := rows.Scan(&p.Name, &teamsStr); err != nil {
			return nil, err
		}

		p.Teams = ParseTeams(teamsStr)
		projects = append(projects, p)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows.Close()

	for i, p := range projects {
		comps, err := GetComponents(database, p.Name)
		if err != nil {
			return nil, err
		}

		projects[i].Components = comps
	}

	if projects == nil {
		projects = []Project{}
	}

	return projects, nil
}

// GetComponents returns the names of all components belonging to project,
// sorted alphabetically.
func GetComponents(database *sql.DB, project string) ([]string, error) {
	var comps []string

	rows, err := database.Query(`SELECT name FROM components WHERE project = ? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}

		comps = append(comps, name)
	}

	if comps == nil {
		comps = []string{}
	}

	return comps, rows.Err()
}

// CreateProject inserts a new project. INSERT OR IGNORE makes the operation
// idempotent. teams defaults to ["any"] when nil or empty.
func CreateProject(database *sql.DB, name string, teams []string) error {
	teamsStr := FormatTeams(teams)
	_, err := database.Exec(
		`INSERT OR IGNORE INTO projects (name, teams) VALUES (?, ?)`,
		name, teamsStr,
	)

	return err
}

// SetProjectTeams replaces the teams list for an existing project.
func SetProjectTeams(database *sql.DB, project string, teams []string) error {
	teamsStr := FormatTeams(teams)
	result, err := database.Exec(
		`UPDATE projects SET teams = ? WHERE name = ?`,
		teamsStr, project,
	)

	if err != nil {
		return err
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project %q not found", project)
	}

	return nil
}

// AddComponent adds a component to an existing project. It first confirms the
// project exists (returning an error if not) and then inserts the component
// using INSERT OR IGNORE, which is a no-op if the (project, name) pair already
// exists.
func AddComponent(database *sql.DB, project, component string) error {
	var exists int

	if err := database.QueryRow(`SELECT COUNT(*) FROM projects WHERE name = ?`, project).Scan(&exists); err != nil {
		return err
	}

	if exists == 0 {
		return fmt.Errorf("project %q does not exist", project)
	}

	_, err := database.Exec(`INSERT OR IGNORE INTO components (project, name) VALUES (?, ?)`, project, component)

	return err
}

// DeleteProject removes a project and all its components from the database.
// It refuses to delete a project that is still referenced by issues.
func DeleteProject(database *sql.DB, name string) error {
	var ids []int64

	rows, err := database.Query(`SELECT id FROM issues WHERE project = ?`, name)
	if err != nil {
		return err
	}

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()

			return err
		}

		ids = append(ids, id)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return err
	}

	if len(ids) > 0 {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = fmt.Sprintf("#%d", id)
		}

		return fmt.Errorf("project %q is referenced by issues: %s", name, strings.Join(parts, ", "))
	}

	if _, err := database.Exec(`DELETE FROM components WHERE project = ?`, name); err != nil {
		return err
	}

	_, err = database.Exec(`DELETE FROM projects WHERE name = ?`, name)

	return err
}

// DeleteComponent removes a single component from a project.
func DeleteComponent(database *sql.DB, project, component string) error {
	var ids []int64

	rows, err := database.Query(`SELECT id FROM issues WHERE project = ? AND component = ?`, project, component)
	if err != nil {
		return err
	}

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()

			return err
		}

		ids = append(ids, id)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return err
	}

	if len(ids) > 0 {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = fmt.Sprintf("#%d", id)
		}

		return fmt.Errorf("component %q/%q is referenced by issues: %s", project, component, strings.Join(parts, ", "))
	}

	_, err = database.Exec(`DELETE FROM components WHERE project = ? AND name = ?`, project, component)

	return err
}
