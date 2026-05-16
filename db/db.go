package db

import (
	"database/sql"
	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := initSchema(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func initSchema(database *sql.DB) error {
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
	`)
	return err
}
