package db

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User represents a row in the users table. The json struct tags control how
// the fields are named when this struct is encoded to JSON (e.g. in API
// responses). "omitempty" means the field is omitted from the JSON output when
// it is the zero value (empty string) — useful for fields we don't want to
// expose in list responses.
type User struct {
	Username     string   `json:"username"`
	DisplayName  string   `json:"display_name"`
	PasswordHash string   `json:"password_hash,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	LastLoginAt  string   `json:"last_login_at,omitempty"`
	Teams        []string `json:"teams"`
	IsAdmin      bool     `json:"is_admin"` // derived from Teams; not stored directly
}

// hashPassword hashes a plaintext password with bcrypt at the default cost.
func hashPassword(plaintext string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)

	return string(hash), err
}

// IsLegacyHash reports whether storedHash is a raw SHA-256 hex digest from the
// old client-side hashing scheme (64 lowercase hex characters). bcrypt hashes
// always begin with "$2" and are 60 characters long, so the two formats are
// unambiguous.
func IsLegacyHash(storedHash string) bool {
	return len(storedHash) == 64 && !strings.HasPrefix(storedHash, "$2")
}

// VerifyPassword checks whether plaintext matches storedHash. It handles both
// current bcrypt hashes and legacy SHA-256 hex digests produced by the old
// client-side hashing scheme, so existing accounts continue to work until
// their hash is upgraded on next login.
func VerifyPassword(storedHash, plaintext string) bool {
	if strings.HasPrefix(storedHash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(plaintext)) == nil
	}

	// Legacy path: compare SHA-256(plaintext) with the stored hex digest using
	// constant-time comparison to avoid timing side-channels.
	computed := fmt.Sprintf("%x", sha256.Sum256([]byte(plaintext)))

	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(computed)) == 1
}

// UpgradePasswordHash replaces a user's stored hash in-place with a fresh
// bcrypt hash. Called after a successful legacy-SHA-256 login so that the
// stored credential is upgraded transparently without requiring a password
// reset.
func UpgradePasswordHash(database *sql.DB, username, plaintext string) error {
	hash, err := hashPassword(plaintext)
	if err != nil {
		return err
	}

	_, err = database.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, hash, username)

	return err
}

// AddUser inserts a new user or updates an existing one (upsert). The password
// is hashed with bcrypt before storage; the caller passes the plaintext
// password. teams is the initial team membership; nil or empty defaults to
// ["any"]. ON CONFLICT DO UPDATE means an existing row is overwritten rather
// than returning an error, letting the CLI "add" command act as both create and
// update.
func AddUser(database *sql.DB, username, displayName, password string, teams []string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	teamsStr := FormatTeams(teams)

	// is_admin is kept in sync with team membership for backward compatibility
	// with any direct DB queries that still rely on the column.
	isAdmin := 0
	if ContainsTeam(teams, TeamAdmin) {
		isAdmin = 1
	}

	_, err = database.Exec(
		`INSERT INTO users (username, display_name, password_hash, created_at, teams, is_admin)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET
		     display_name=excluded.display_name,
		     password_hash=excluded.password_hash,
		     teams=excluded.teams,
		     is_admin=excluded.is_admin`,
		username, displayName, hash, time.Now().UTC().Format(time.RFC3339), teamsStr, isAdmin,
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

// scanUser reads the columns written by FindUser and ListUsers into a User
// struct, deriving IsAdmin from the teams column.
func scanUser(username, displayName, passwordHash, createdAt, lastLoginAt, teamsStr *string, u *User) {
	u.Username = *username
	u.DisplayName = *displayName
	u.PasswordHash = *passwordHash
	u.CreatedAt = *createdAt
	u.LastLoginAt = *lastLoginAt
	u.Teams = ParseTeams(*teamsStr)
	u.IsAdmin = ContainsTeam(u.Teams, TeamAdmin)
}

// FindUser looks up a single user by username and returns it. Returns (nil,
// nil) — no user and no error — when the username does not exist. The caller
// must check for nil before using the returned pointer.
func FindUser(database *sql.DB, username string) (*User, error) {
	var (
		u            User
		usernameVal  string
		displayName  string
		passwordHash string
		createdAt    string
		lastLoginAt  string
		teamsStr     string
		adminInt     int
	)

	row := database.QueryRow(
		`SELECT username, display_name, password_hash, created_at, last_login_at, teams, is_admin
		 FROM users WHERE username = ?`, username,
	)

	if err := row.Scan(&usernameVal, &displayName, &passwordHash, &createdAt, &lastLoginAt, &teamsStr, &adminInt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	scanUser(&usernameVal, &displayName, &passwordHash, &createdAt, &lastLoginAt, &teamsStr, &u)

	return &u, nil
}

// UpdateUser modifies one or more fields of an existing user. Only fields with
// non-empty / non-nil values are updated; others are left unchanged. When a
// non-empty password is provided it is hashed with bcrypt before storage; the
// caller passes the plaintext.  teams replaces the existing team list when
// non-nil; pass nil to leave teams unchanged.
//
// isAdmin is a *bool (pointer to bool) convenience alias: when non-nil it
// adds or removes the "admin" team from the teams list.  If teams is also
// provided, isAdmin is applied on top of it.  nil means "no change".
func UpdateUser(database *sql.DB, username, displayName, password string, isAdmin *bool, teams []string) error {
	var (
		setClauses []string
		args       []any
	)

	u, err := FindUser(database, username)
	if err != nil {
		return err
	}

	if u == nil {
		return fmt.Errorf("user %q not found", username)
	}

	if displayName != "" {
		setClauses = append(setClauses, "display_name = ?")
		args = append(args, displayName)
	}

	if password != "" {
		hash, err := hashPassword(password)
		if err != nil {
			return fmt.Errorf("hashing password: %w", err)
		}

		setClauses = append(setClauses, "password_hash = ?")
		args = append(args, hash)
	}

	// Determine the effective new teams list.
	effectiveTeams := u.Teams // start from current

	if teams != nil {
		effectiveTeams = teams
	}

	// Apply the isAdmin convenience alias on top.
	if isAdmin != nil {
		if *isAdmin {
			if !ContainsTeam(effectiveTeams, TeamAdmin) {
				effectiveTeams = append(effectiveTeams, TeamAdmin)
			}
		} else {
			// Remove admin from the list.
			filtered := make([]string, 0, len(effectiveTeams))

			for _, t := range effectiveTeams {
				if strings.ToLower(t) != TeamAdmin {
					filtered = append(filtered, t)
				}
			}

			effectiveTeams = filtered
		}
	}

	if teams != nil || isAdmin != nil {
		teamsStr := FormatTeams(effectiveTeams)
		adminInt := 0

		if ContainsTeam(effectiveTeams, TeamAdmin) {
			adminInt = 1
		}

		setClauses = append(setClauses, "teams = ?", "is_admin = ?")
		args = append(args, teamsStr, adminInt)
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

// CountAdmins returns the number of users with admin privileges, determined by
// whether "admin" is in their teams list.
func CountAdmins(database *sql.DB) (int, error) {
	var count int

	err := database.QueryRow(
		`SELECT COUNT(*) FROM users WHERE ',' || lower(teams) || ',' LIKE '%,admin,%'`,
	).Scan(&count)

	return count, err
}

func HasUsers(database *sql.DB) (bool, error) {
	var count int

	err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)

	return count > 0, err
}

func ListUsers(database *sql.DB) ([]User, error) {
	var users []User

	rows, err := database.Query(
		`SELECT username, display_name, last_login_at, teams, is_admin FROM users ORDER BY username`,
	)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var (
			u           User
			lastLoginAt string
			teamsStr    string
			adminInt    int
		)

		if err := rows.Scan(&u.Username, &u.DisplayName, &lastLoginAt, &teamsStr, &adminInt); err != nil {
			return nil, err
		}

		u.LastLoginAt = lastLoginAt
		u.Teams = ParseTeams(teamsStr)
		u.IsAdmin = ContainsTeam(u.Teams, TeamAdmin)
		users = append(users, u)
	}

	if users == nil {
		users = []User{}
	}

	return users, rows.Err()
}
