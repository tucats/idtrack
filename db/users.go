package db

import (
	"database/sql"
	"time"
)

type User struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"password_hash,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func AddUser(database *sql.DB, username, displayName, passwordHash string) error {
	_, err := database.Exec(
		`INSERT INTO users (username, display_name, password_hash, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET display_name=excluded.display_name, password_hash=excluded.password_hash`,
		username, displayName, passwordHash, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func DeleteUser(database *sql.DB, username string) error {
	_, err := database.Exec(`DELETE FROM users WHERE username = ?`, username)
	return err
}

func FindUser(database *sql.DB, username string) (*User, error) {
	row := database.QueryRow(
		`SELECT username, display_name, password_hash, created_at FROM users WHERE username = ?`, username,
	)
	var u User
	if err := row.Scan(&u.Username, &u.DisplayName, &u.PasswordHash, &u.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func ListUsers(database *sql.DB) ([]User, error) {
	rows, err := database.Query(`SELECT username, display_name FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Username, &u.DisplayName); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if users == nil {
		users = []User{}
	}
	return users, rows.Err()
}
