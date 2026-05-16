package db

import (
	"database/sql"
	"fmt"
	"strings"
)

type Project struct {
	Name       string   `json:"name"`
	Components []string `json:"components"`
}

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
	if comps == nil {
		comps = []string{}
	}
	return comps, rows.Err()
}

func CreateProject(database *sql.DB, name string) error {
	_, err := database.Exec(`INSERT OR IGNORE INTO projects (name) VALUES (?)`, name)
	return err
}

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

func DeleteProject(database *sql.DB, name string) error {
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
