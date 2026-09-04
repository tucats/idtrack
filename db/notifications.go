package db

import (
	"database/sql"
	"time"
)

// NotificationToken represents a row in the notification_tokens table: one
// APNs device token belonging to a user. A user may have zero or more of
// these (one per installed device). The token itself is the primary key —
// see NotificationPrefs' doc comment on why prefs live separately — because a
// device token already uniquely identifies one (app, device) pair; if a
// different user later signs into the same physical device, RegisterToken
// simply reassigns this same row rather than creating a second one.
type NotificationToken struct {
	Token          string `json:"token"`
	Username       string `json:"username"`
	CreatedAt      string `json:"created_at"`
	LastNotifiedAt string `json:"last_notified_at,omitempty"`
	BadgeCount     int    `json:"badge_count"`
}

// NotificationPrefs holds one user's opt-in/opt-out choice for each of the
// three notification categories idtrack sends. Deliberately not a field on
// the shared User struct: User backs GET /api/users, which every
// authenticated user can call to build the assignee dropdown/userMap, and
// one user's notification preferences have no business being visible to
// another user's client.
type NotificationPrefs struct {
	NewIssue   bool `json:"new_issue"`
	NewComment bool `json:"new_comment"`
	Resolved   bool `json:"resolved"`
}

// RegisterToken records that token now belongs to username, creating the row
// if it doesn't exist or reassigning it (without touching created_at or
// badge_count) if it does. Called every time the client acquires a token —
// on login, onboarding, and app relaunch — so this is expected to be called
// far more often than the token actually changes; the upsert makes repeat
// calls with an unchanged token a cheap no-op.
func RegisterToken(database *sql.DB, username, token string) error {
	_, err := database.Exec(
		`INSERT INTO notification_tokens (token, username, created_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET username = excluded.username`,
		token, username, time.Now().UTC().Format(time.RFC3339),
	)

	return err
}

// DeleteToken removes a single device token unconditionally. Used by the
// server-internal, trusted cleanup path when APNs reports a token as
// permanently invalid (BadDeviceToken/Unregistered — see internal/apns) —
// that caller only ever has the token string in hand, not an owning
// username, and has no reason to scope the delete.
func DeleteToken(database *sql.DB, token string) error {
	_, err := database.Exec(`DELETE FROM notification_tokens WHERE token = ?`, token)

	return err
}

// DeleteTokenForUser removes token only if it currently belongs to username,
// mirroring DeleteCredential's ownership-scoped delete for WebAuthn passkeys.
// Used by the self-service unregister endpoint so an authenticated caller
// can never remove a device token belonging to a different account, even by
// guessing or replaying another user's token value.
func DeleteTokenForUser(database *sql.DB, username, token string) error {
	_, err := database.Exec(`DELETE FROM notification_tokens WHERE token = ? AND username = ?`, token, username)

	return err
}

// TokensForUser returns every device token currently registered to username,
// in no particular order. Used by the notifier to fan a single logical
// notification out to all of a user's devices.
func TokensForUser(database *sql.DB, username string) ([]string, error) {
	rows, err := database.Query(`SELECT token FROM notification_tokens WHERE username = ?`, username)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var tokens []string

	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}

		tokens = append(tokens, t)
	}

	return tokens, rows.Err()
}

// TouchToken sets last_notified_at to now. Called after a push has been
// successfully handed off to APNs for this token.
func TouchToken(database *sql.DB, token string) error {
	_, err := database.Exec(
		`UPDATE notification_tokens SET last_notified_at = ? WHERE token = ?`,
		time.Now().UTC().Format(time.RFC3339), token,
	)

	return err
}

// IncrementBadge atomically increments token's badge_count and returns the
// new value. APNs itself is stateless about badge numbers — a push's
// "aps.badge" field sets the icon to exactly the value given, so the
// provider (this server) is responsible for tracking the running count per
// device. Using "UPDATE ... RETURNING" (rather than a separate SELECT after
// the UPDATE) keeps this a single statement, avoiding a read-then-write race
// against a concurrent notification to the same token.
func IncrementBadge(database *sql.DB, token string) (int, error) {
	var count int

	err := database.QueryRow(
		`UPDATE notification_tokens SET badge_count = badge_count + 1 WHERE token = ? RETURNING badge_count`,
		token,
	).Scan(&count)

	return count, err
}

// ResetBadge zeroes token's badge_count. Called when the client reports that
// the user has opened the app on that device, so the next push starts
// counting from zero instead of continuing to climb.
func ResetBadge(database *sql.DB, token string) error {
	_, err := database.Exec(`UPDATE notification_tokens SET badge_count = 0 WHERE token = ?`, token)

	return err
}

// GetNotificationPrefs returns username's notification preferences. Every
// user has an implicit set of prefs (the users table's notify_* columns
// default to 1/on), so this never returns (nil, nil) for a user that exists;
// it returns (nil, nil) only when the username itself does not exist, mirroring
// FindUser's convention.
func GetNotificationPrefs(database *sql.DB, username string) (*NotificationPrefs, error) {
	var (
		newIssue, newComment, resolved int
	)

	row := database.QueryRow(
		`SELECT notify_new_issue, notify_new_comment, notify_resolved FROM users WHERE username = ?`,
		username,
	)

	if err := row.Scan(&newIssue, &newComment, &resolved); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}

		return nil, err
	}

	return &NotificationPrefs{
		NewIssue:   newIssue != 0,
		NewComment: newComment != 0,
		Resolved:   resolved != 0,
	}, nil
}

// SetNotificationPrefs replaces username's notification preferences wholesale
// (all three columns are always written together — there is no partial-update
// variant, since the client always sends the full set of toggles at once).
func SetNotificationPrefs(database *sql.DB, username string, prefs NotificationPrefs) error {
	toInt := func(b bool) int {
		if b {
			return 1
		}

		return 0
	}

	_, err := database.Exec(
		`UPDATE users SET notify_new_issue = ?, notify_new_comment = ?, notify_resolved = ? WHERE username = ?`,
		toInt(prefs.NewIssue), toInt(prefs.NewComment), toInt(prefs.Resolved), username,
	)

	return err
}
