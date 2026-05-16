package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// User represents a row in the users table. The json struct tags control how
// the fields are named when this struct is encoded to JSON (e.g. in API
// responses). "omitempty" means the field is omitted from the JSON output when
// it is the zero value (empty string) — useful for fields we don't want to
// expose in list responses.
type User struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"password_hash,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastLoginAt  string `json:"last_login_at,omitempty"`
	IsAdmin      bool   `json:"is_admin"`
}

// AddUser inserts a new user or updates an existing one (upsert). The
// ON CONFLICT DO UPDATE clause means: if a row with this username already
// exists, overwrite its display name, password hash, and admin flag instead of
// returning an error. This lets the CLI "--add" command act as both create and
// update.
//
// isAdmin is stored as an integer (0/1) because SQLite has no native boolean
// type — we convert it here rather than sprinkle the conversion everywhere.
func AddUser(database *sql.DB, username, displayName, passwordHash string, isAdmin bool) error {
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}
	_, err := database.Exec(
		`INSERT INTO users (username, display_name, password_hash, created_at, is_admin) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET display_name=excluded.display_name, password_hash=excluded.password_hash, is_admin=excluded.is_admin`,
		username, displayName, passwordHash, time.Now().UTC().Format(time.RFC3339), adminInt,
	)
	return err
}

// DeleteUser removes the user row for username. It does not cascade to issues
// or comments — those rows store the username as a plain string and are left
// intact. Orphaned reporter/assignee strings are acceptable for an internal
// tool where users are infrequently removed.
func DeleteUser(database *sql.DB, username string) error {
	_, err := database.Exec(`DELETE FROM users WHERE username = ?`, username)
	return err
}

// FindUser looks up a single user by username and returns it. Returns (nil,
// nil) — no user and no error — when the username does not exist. The caller
// must check for nil before using the returned pointer.
//
// sql.ErrNoRows is the sentinel error that database/sql returns from
// row.Scan() when a QueryRow finds no matching record. We translate it to a
// nil pointer so callers can write a simple "if user == nil" check.
func FindUser(database *sql.DB, username string) (*User, error) {
	row := database.QueryRow(
		`SELECT username, display_name, password_hash, created_at, last_login_at, is_admin FROM users WHERE username = ?`, username,
	)
	var u User
	var adminInt int
	if err := row.Scan(&u.Username, &u.DisplayName, &u.PasswordHash, &u.CreatedAt, &u.LastLoginAt, &adminInt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // not found — not an error
		}
		return nil, err
	}
	u.IsAdmin = adminInt != 0 // convert SQLite integer back to Go bool
	return &u, nil
}

// UpdateUser modifies one or more fields of an existing user. Only fields with
// non-empty / non-nil values are updated; others are left unchanged. This
// allows callers to update just the display name without touching the password,
// for example.
//
// isAdmin is a *bool (pointer to bool) rather than a plain bool so that nil
// can represent "not specified". A plain false bool is ambiguous — it could
// mean "set admin to false" or "the caller didn't pass the flag".
//
// The function builds a SET clause dynamically by accumulating "col = ?"
// fragments and matching argument values, then runs a single UPDATE statement.
func UpdateUser(database *sql.DB, username, displayName, passwordHash string, isAdmin *bool) error {
	// Verify the user exists before attempting an update. UpdateUser must not
	// silently create a new row — use AddUser for that.
	u, err := FindUser(database, username)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("user %q not found", username)
	}

	// Build a dynamic UPDATE statement from whichever fields were provided.
	// setClauses collects fragments like "display_name = ?" and args holds the
	// corresponding values in the same order.
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
	if isAdmin != nil {
		adminInt := 0
		if *isAdmin {
			adminInt = 1
		}
		setClauses = append(setClauses, "is_admin = ?")
		args = append(args, adminInt)
	}
	if len(setClauses) == 0 {
		return nil // nothing to do
	}

	// The WHERE clause's ? placeholder value goes last in the args slice.
	args = append(args, username)
	_, err = database.Exec(
		fmt.Sprintf("UPDATE users SET %s WHERE username = ?", strings.Join(setClauses, ", ")),
		args..., // the ... unpacks the slice as individual arguments
	)
	return err
}

// RecordLogin updates the last_login_at timestamp for username to the current
// UTC time. It is called after a successful /api/login request (not on every
// authenticated API call) to keep the overhead low.
func RecordLogin(database *sql.DB, username string) error {
	_, err := database.Exec(
		`UPDATE users SET last_login_at = ? WHERE username = ?`,
		time.Now().UTC().Format(time.RFC3339), username,
	)
	return err
}

// ListUsers returns every user ordered alphabetically by username. Only the
// columns needed for display are selected — password_hash is intentionally
// excluded to avoid including it in API responses.
func ListUsers(database *sql.DB) ([]User, error) {
	rows, err := database.Query(`SELECT username, display_name, last_login_at, is_admin FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	// defer rows.Close() ensures the result set is released even if we return
	// early due to a Scan error. Always close rows when you are done with them.
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var adminInt int
		if err := rows.Scan(&u.Username, &u.DisplayName, &u.LastLoginAt, &adminInt); err != nil {
			return nil, err
		}
		u.IsAdmin = adminInt != 0
		users = append(users, u)
	}
	// Return an empty slice rather than nil so JSON encoding produces "[]"
	// instead of "null", which is friendlier for frontend consumers.
	if users == nil {
		users = []User{}
	}
	// rows.Err() returns any error that occurred during iteration (e.g. a
	// network blip mid-query). Always check it after the loop.
	return users, rows.Err()
}
