package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type User struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"password_hash,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastLoginAt  string `json:"last_login_at,omitempty"`
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

func UpdateUser(database *sql.DB, username, displayName, passwordHash string) error {
	u, err := FindUser(database, username)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("user %q not found", username)
	}

	var setClauses []string
	var args []any
	if displayName != "" {
		setClauses = append(setClauses, "display_name = ?")
		args = append(args, displayName)
	}
	if passwordHash != "" {
		setClauses = append(setClauses, "password_hash = ?")
		args = append(args, passwordHash)
	}
	if len(setClauses) == 0 {
		return nil
	}
	args = append(args, username)
	_, err = database.Exec(
		fmt.Sprintf("UPDATE users SET %s WHERE username = ?", strings.Join(setClauses, ", ")),
		args...,
	)
	return err
}

func RecordLogin(database *sql.DB, username string) error {
	_, err := database.Exec(
		`UPDATE users SET last_login_at = ? WHERE username = ?`,
		time.Now().UTC().Format(time.RFC3339), username,
	)
	return err
}

func ListUsers(database *sql.DB) ([]User, error) {
	rows, err := database.Query(`SELECT username, display_name, last_login_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Username, &u.DisplayName, &u.LastLoginAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if users == nil {
		users = []User{}
	}
	return users, rows.Err()
}
