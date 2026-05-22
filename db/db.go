// Package db provides all database access for idtrack. Every function in this
// package takes a *sql.DB as its first argument rather than keeping global
// state, which makes testing and multiple concurrent databases straightforward.
//
// The underlying engine is SQLite via modernc.org/sqlite — a pure-Go driver
// that does not require CGO or any C toolchain.
package db

import (
	"database/sql"
	"fmt"
	"strings"

	// The blank import registers the "sqlite" driver with the database/sql
	// package as a side effect. After this import, sql.Open("sqlite", path)
	// works. We never call anything from the package directly.
	_ "modernc.org/sqlite"
)

// Open opens (or creates) the SQLite database at path, applies any missing
// schema objects, and returns the connection pool ready to use. It is safe to
// call Open on an existing database — all DDL uses IF NOT EXISTS / ALTER TABLE
// patterns that are harmless when the objects already exist.
func Open(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// SQLite only supports one writer at a time. Setting the pool to a single
	// connection serializes all queries through one connection and prevents
	// "SQLITE_BUSY: database is locked" errors under concurrent HTTP requests.
	database.SetMaxOpenConns(1)

	if err := initSchema(database); err != nil {
		database.Close()

		return nil, err
	}

	return database, nil
}

// initSchema creates the base tables (if they don't already exist) and then
// applies any columns that were added after the initial schema via ALTER TABLE.
// This gives us zero-downtime migrations: an old database file is upgraded
// automatically when the new binary starts, with no manual steps required.
func initSchema(database *sql.DB) error {
	// A single Exec call can contain multiple statements separated by semicolons.
	// CREATE TABLE IF NOT EXISTS is a no-op when the table already exists, so
	// this block is safe to run against both a fresh and an existing database.
	_, err := database.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			username      TEXT PRIMARY KEY,
			display_name  TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at    TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS issues (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			title       TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			reporter    TEXT NOT NULL,
			assignee    TEXT NOT NULL DEFAULT '',
			priority    TEXT NOT NULL DEFAULT 'Medium',
			status      TEXT NOT NULL DEFAULT 'Open',
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS comments (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id   INTEGER NOT NULL,
			author     TEXT NOT NULL,
			body       TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS projects (
			name TEXT PRIMARY KEY
		);
		CREATE TABLE IF NOT EXISTS components (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			project TEXT NOT NULL,
			name    TEXT NOT NULL,
			UNIQUE(project, name)
		);
	`)
	if err != nil {
		return err
	}

	// These columns were added to the schema after the initial release.
	// addColumnIfMissing runs ALTER TABLE ... ADD COLUMN and silently ignores
	// the error if the column already exists, so existing databases are
	// upgraded automatically and new databases are fine too.
	if err := addColumnIfMissing(database, "users", "last_login_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "users", "is_admin", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "issues", "project", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "issues", "component", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "issues", "resolved_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// dependent_issues stores a comma-separated list of issue IDs that this
	// issue depends on.  For a Duplicate issue this is always a single ID; for
	// a Blocked issue it may be one or more.  The empty string means no
	// dependencies.  Added as a post-initial-release migration.
	if err := addColumnIfMissing(database, "issues", "dependent_issues", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// Backfill resolved_at for existing Resolved issues that have no value yet.
	// The most recent comment timestamp is used as the best available proxy —
	// it is typically the "Fixed in version X" comment posted when the issue was
	// closed. Issues with no comments keep resolved_at = '' (unknown).
	if _, err := database.Exec(`
		UPDATE issues
		SET    resolved_at = (SELECT MAX(created_at) FROM comments WHERE issue_id = issues.id)
		WHERE  status      = 'Resolved'
		AND    resolved_at = ''
		AND    EXISTS (SELECT 1 FROM comments WHERE issue_id = issues.id)
	`); err != nil {
		return err
	}

	// Create covering indexes for the most common filter and sort columns.
	// CREATE INDEX IF NOT EXISTS is a no-op when the index already exists,
	// so this runs safely against both new and already-upgraded databases.
	for _, ddl := range []string{
		`CREATE INDEX IF NOT EXISTS idx_issues_status     ON issues (status)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_status_pri ON issues (status, priority)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_updated_at ON issues (updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_assignee   ON issues (assignee)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_reporter   ON issues (reporter)`,
		// Used by the comment_count correlated subquery in ListIssues / GetIssue.
		`CREATE INDEX IF NOT EXISTS idx_comments_issue_id ON comments (issue_id)`,
	} {
		if _, err := database.Exec(ddl); err != nil {
			return err
		}
	}

	return nil
}

// addColumnIfMissing adds a column to a table if it does not already exist.
// SQLite's ALTER TABLE ADD COLUMN returns an error containing "duplicate column
// name" when the column is present — we treat that specific error as success
// so that calling this function is always safe regardless of schema state.
func addColumnIfMissing(database *sql.DB, table, column, definition string) error {
	_, err := database.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil // column already exists — nothing to do
	}

	return err
}
