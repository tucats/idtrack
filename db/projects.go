package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// Project represents a project row combined with its list of component names.
// The two are stored in separate tables but returned together as one struct
// for convenience in API responses.
type Project struct {
	Name       string   `json:"name"`
	Components []string `json:"components"`
}

// ListProjects returns every project in alphabetical order, each with its
// component names pre-populated. Because projects and components live in
// separate tables we do two passes:
//  1. Fetch all project names.
//  2. For each project, fetch its components with a second query.
//
// The result set from step 1 must be closed (rows.Close()) before we open the
// per-project queries in step 2; SQLite with MaxOpenConns(1) allows only one
// active statement at a time on the same connection.
func ListProjects(database *sql.DB) ([]Project, error) {
	rows, err := database.Query(`SELECT name FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Explicitly close the first result set before running the per-project
	// queries below. The deferred rows.Close() would also work, but calling it
	// explicitly here makes the intent clear to readers.
	rows.Close()

	// Populate the Components slice for each project using a separate query.
	for i, p := range projects {
		comps, err := GetComponents(database, p.Name)
		if err != nil {
			return nil, err
		}
		projects[i].Components = comps // update the slice element by index, not by range copy
	}

	// Return an empty slice rather than nil so JSON encoding produces "[]".
	if projects == nil {
		projects = []Project{}
	}
	return projects, nil
}

// GetComponents returns the names of all components belonging to project,
// sorted alphabetically. It is also called by ListProjects to populate the
// embedded Components field.
func GetComponents(database *sql.DB, project string) ([]string, error) {
	rows, err := database.Query(`SELECT name FROM components WHERE project = ? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comps []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		comps = append(comps, name)
	}
	// Return an empty slice rather than nil for consistent JSON output.
	if comps == nil {
		comps = []string{}
	}
	return comps, rows.Err()
}

// CreateProject inserts a new project. INSERT OR IGNORE means the statement
// succeeds silently when the project already exists — making this operation
// idempotent. Callers do not need to check for duplicates first.
func CreateProject(database *sql.DB, name string) error {
	_, err := database.Exec(`INSERT OR IGNORE INTO projects (name) VALUES (?)`, name)
	return err
}

// AddComponent adds a component to an existing project. It first confirms the
// project exists (returning an error if not) and then inserts the component
// using INSERT OR IGNORE, which is a no-op if the (project, name) pair already
// exists.
func AddComponent(database *sql.DB, project, component string) error {
	// Check that the parent project exists before inserting the component.
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
// It refuses to delete a project that is still referenced by open or resolved
// issues, returning a descriptive error with the blocking issue IDs so the
// caller can inform the user.
func DeleteProject(database *sql.DB, name string) error {
	// Collect every issue ID that references this project so we can report them.
	rows, err := database.Query(`SELECT id FROM issues WHERE project = ?`, name)
	if err != nil {
		return err
	}
	var ids []int64
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
		// Format the IDs as "#1, #2, ..." for a human-readable error message.
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = fmt.Sprintf("#%d", id)
		}
		return fmt.Errorf("project %q is referenced by issues: %s", name, strings.Join(parts, ", "))
	}

	// Delete components first (no foreign key enforcement in SQLite by default),
	// then delete the project itself.
	if _, err := database.Exec(`DELETE FROM components WHERE project = ?`, name); err != nil {
		return err
	}
	_, err = database.Exec(`DELETE FROM projects WHERE name = ?`, name)
	return err
}

// DeleteComponent removes a single component from a project. Like DeleteProject
// it checks for referencing issues first and returns an error with their IDs
// when any are found.
func DeleteComponent(database *sql.DB, project, component string) error {
	rows, err := database.Query(`SELECT id FROM issues WHERE project = ? AND component = ?`, project, component)
	if err != nil {
		return err
	}
	var ids []int64
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
