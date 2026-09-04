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
//
// Doc-comment convention: notice that this comment (and most others in this
// package) starts by repeating the function's own name — "Open opens...".
// That is a Go convention, not a style choice made just for this project. Tools
// like `go doc`, godoc.org, and IDE hover tooltips extract the first sentence
// of a comment directly above an exported (capitalized) declaration and show
// it as that symbol's documentation. Starting the sentence with the symbol's
// name reads naturally in that context ("db.Open opens...").
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
		CREATE TABLE IF NOT EXISTS teams (
			name        TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT ''
		);
		-- Registered passkeys (Touch ID / Face ID / security keys). The table
		-- itself is brand new rather than a migrated addition to an existing
		-- table, so an old database file gets it for free the first time it
		-- is opened with a binary that knows about it, with nothing to
		-- backfill since it starts (and stays) empty until a user registers
		-- their first passkey. One column (flags, below) was added after the
		-- table's initial shape and does need the usual addColumnIfMissing
		-- treatment, same as every column on every other table here.
		CREATE TABLE IF NOT EXISTS webauthn_credentials (
			id            TEXT PRIMARY KEY,
			username      TEXT NOT NULL,
			public_key    BLOB NOT NULL,
			sign_count    INTEGER NOT NULL DEFAULT 0,
			transports    TEXT NOT NULL DEFAULT '',
			flags         INTEGER NOT NULL DEFAULT 0,
			name          TEXT NOT NULL DEFAULT '',
			created_at    TEXT NOT NULL,
			last_used_at  TEXT NOT NULL DEFAULT ''
		);
		-- Image attachments on an issue's description or one of its comments.
		-- Like webauthn_credentials above, this is a brand-new table rather than
		-- a migrated addition to an existing one, so an old database gets it for
		-- free via CREATE TABLE IF NOT EXISTS with nothing to backfill. comment_id
		-- uses 0 (not NULL) as the "attached to the description, not a comment"
		-- sentinel — no comment row ever has id 0 (AUTOINCREMENT starts at 1) — to
		-- keep the column a plain NOT NULL INTEGER like the rest of the schema.
		CREATE TABLE IF NOT EXISTS attachments (
			id          TEXT PRIMARY KEY,
			issue_id    INTEGER NOT NULL,
			comment_id  INTEGER NOT NULL DEFAULT 0,
			uploader    TEXT NOT NULL,
			filename    TEXT NOT NULL DEFAULT '',
			width       INTEGER NOT NULL DEFAULT 0,
			height      INTEGER NOT NULL DEFAULT 0,
			size        INTEGER NOT NULL DEFAULT 0,
			image       BLOB NOT NULL,
			thumbnail   BLOB NOT NULL,
			created_at  TEXT NOT NULL
		);
		-- Registered APNs push-notification device tokens. Like
		-- webauthn_credentials and attachments above, this is a brand-new table,
		-- so an old database gets it for free via CREATE TABLE IF NOT EXISTS with
		-- nothing to backfill — it starts (and stays) empty until a client
		-- registers its first device token. The token itself is the primary key
		-- (see db.RegisterToken) because a device token already uniquely
		-- identifies one (app, device) pair; badge_count is tracked here because
		-- APNs itself is stateless about badge numbers (see db.IncrementBadge).
		CREATE TABLE IF NOT EXISTS notification_tokens (
			token             TEXT PRIMARY KEY,
			username          TEXT NOT NULL,
			created_at        TEXT NOT NULL,
			last_notified_at  TEXT NOT NULL DEFAULT '',
			badge_count       INTEGER NOT NULL DEFAULT 0
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

	// teams columns store a comma-separated list of team names.  DEFAULT ''
	// allows the migration guard (WHERE teams = '') to distinguish unset rows
	// from rows that have been explicitly assigned a team.
	if err := addColumnIfMissing(database, "users", "teams", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "projects", "teams", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "issues", "teams", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// format records how the description and comment bodies of an issue should
	// be interpreted for display: "text" (plain, escaped), "markdown", or
	// "html". DEFAULT 'text' backfills every existing row as part of the
	// ALTER TABLE itself, so no separate UPDATE is needed.
	if err := addColumnIfMissing(database, "issues", "format", "TEXT NOT NULL DEFAULT 'text'"); err != nil {
		return err
	}

	// flags stores the single-byte packed form of the go-webauthn library's
	// CredentialFlags (UserPresent/UserVerified/BackupEligible/BackupState) —
	// see webauthnUser.WebAuthnCredentials() in server/webauthn.go. This was
	// missed when webauthn_credentials was first added (its CREATE TABLE
	// above already includes the column for a brand-new database), so unlike
	// that table itself, an already-created webauthn_credentials table still
	// needs this one addColumnIfMissing to catch up.
	if err := addColumnIfMissing(database, "webauthn_credentials", "flags", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	// Per-category push-notification opt-in/opt-out. DEFAULT 1 (on) is a safe
	// schema default for rows that predate this feature — an upgraded server
	// shouldn't retroactively silence a user who never made a choice — though
	// in practice the client always writes an explicit value once the OS
	// permission prompt is answered (see docs/NOTIFICATIONS.md), so the
	// default is rarely observed once a user has actually launched a build
	// with notifications support.
	if err := addColumnIfMissing(database, "users", "notify_new_issue", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "users", "notify_new_comment", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}

	if err := addColumnIfMissing(database, "users", "notify_resolved", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}

	// Seed the two reserved team names.  INSERT OR IGNORE is idempotent.
	if _, err := database.Exec(`
		INSERT OR IGNORE INTO teams (name, description) VALUES ('admin', '');
		INSERT OR IGNORE INTO teams (name, description) VALUES ('any',   '');
	`); err != nil {
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

	// Migrate users: admin users get team 'admin', everyone else gets 'any'.
	// The WHERE teams = '' guard makes this a no-op after the first run.
	if _, err := database.Exec(`
		UPDATE users
		SET    teams = CASE WHEN is_admin = 1 THEN 'admin' ELSE 'any' END
		WHERE  teams = ''
	`); err != nil {
		return err
	}

	// Migrate projects and issues to the 'any' team (visible to all users).
	if _, err := database.Exec(`UPDATE projects SET teams = 'any' WHERE teams = ''`); err != nil {
		return err
	}

	if _, err := database.Exec(`UPDATE issues SET teams = 'any' WHERE teams = ''`); err != nil {
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
		// Used by ListCredentials/DeleteCredential to fetch/scope a user's passkeys.
		`CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_username ON webauthn_credentials (username)`,
		// Used by ListAttachments/DeleteAttachmentsByIssue to fetch/scope an issue's attachments.
		`CREATE INDEX IF NOT EXISTS idx_attachments_issue_id ON attachments (issue_id)`,
		// Used by TokensForUser to fetch all of a user's registered devices.
		`CREATE INDEX IF NOT EXISTS idx_notification_tokens_username ON notification_tokens (username)`,
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
//
// This function, plus the calls to it in initSchema, IS the entire migration
// system for this project — there is no separate migration framework, no
// numbered migration files, and no "schema_version" table. Every time the
// binary starts, Open -> initSchema runs every one of these calls again; on
// an up-to-date database every call hits the "duplicate column name" branch
// and returns nil immediately, so the cost of "running migrations" on a
// database that needs none is a handful of cheap no-op statements. Adding a
// new column to the schema in the future means adding one more
// addColumnIfMissing call here — nothing else to wire up.
func addColumnIfMissing(database *sql.DB, table, column, definition string) error {
	_, err := database.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil // column already exists — nothing to do
	}

	return err
}
