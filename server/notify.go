package server

import (
	"errors"
	"log"

	"github.com/tucats/idtrack/db"
	"github.com/tucats/idtrack/internal/apns"
)

// notifyCategory identifies which of a user's three notify_* preference
// columns (see db.NotificationPrefs) governs a given notification.
type notifyCategory int

const (
	notifyCategoryNewIssue notifyCategory = iota
	notifyCategoryNewComment
	// notifyCategoryStatusChanged covers every issue status transition
	// (Open<->Blocked<->Resolved<->Duplicate), not just ->Resolved, per the
	// resolved decision recorded at the top of docs/NOTIFICATIONS.md. It is
	// still gated by the single notify_resolved preference rather than a
	// fourth column.
	notifyCategoryStatusChanged
)

// notificationTitleMaxLen and notificationBodyMaxLen keep push payloads
// small and legible on a lock screen. APNs itself has a much larger overall
// payload limit (4KB), but a multi-paragraph comment as a push body would be
// both unreadable in a notification banner and wasteful to encrypt/transmit
// for content the recipient will read in full inside the app anyway.
const (
	notificationBodyMaxLen = 200
)

// truncateForNotification trims s to at most max runes, appending an
// ellipsis when truncation actually happened. Operates on runes (not bytes)
// so multi-byte UTF-8 text is never cut mid-character.
func truncateForNotification(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}

	return string(runes[:max]) + "…"
}

// notify sends one push notification to every user in usernames who (a) has
// the relevant category enabled in their preferences and (b) has at least
// one registered device token. It is always invoked as `go s.notify(...)`
// from the HTTP handlers below (handleCreateIssue, handleCreateComment,
// handleUpdateIssue) so a slow or failing APNs call never delays the API
// response the caller is waiting on — see docs/NOTIFICATIONS.md §2.
//
// usernames may contain blanks, duplicates, and the empty string; all are
// handled safely (blanks and dupes are skipped). It is always safe to call
// even when push notifications are not configured at all — s.apns is nil in
// that case and this becomes a no-op immediately.
func (s *srv) notify(usernames []string, category notifyCategory, title, body string, issueID int64) {
	if s.apns == nil {
		return
	}

	body = truncateForNotification(body, notificationBodyMaxLen)

	seen := make(map[string]bool, len(usernames))

	for _, username := range usernames {
		if username == "" || seen[username] {
			continue
		}

		seen[username] = true

		s.notifyOne(username, category, title, body, issueID)
	}
}

// notifyOne handles a single recipient: preference check, token fan-out,
// badge accounting, and invalid-token cleanup. Split out from notify so the
// per-recipient logic (several early-return branches) doesn't nest three
// loops deep.
func (s *srv) notifyOne(username string, category notifyCategory, title, body string, issueID int64) {
	prefs, err := db.GetNotificationPrefs(s.database, username)
	if err != nil || prefs == nil {
		if err != nil {
			log.Printf("notify: loading prefs for %s: %v", username, err)
		}

		return
	}

	var enabled bool

	switch category {
	case notifyCategoryNewIssue:
		enabled = prefs.NewIssue
	case notifyCategoryNewComment:
		enabled = prefs.NewComment
	case notifyCategoryStatusChanged:
		enabled = prefs.Resolved
	}

	if !enabled {
		return
	}

	tokens, err := db.TokensForUser(s.database, username)
	if err != nil {
		log.Printf("notify: loading tokens for %s: %v", username, err)

		return
	}

	for _, token := range tokens {
		s.sendToToken(token, apns.Payload{Title: title, Body: body, IssueID: issueID})
	}
}

// sendToToken increments token's server-tracked badge count (APNs itself is
// stateless about badges — see db.IncrementBadge), sends the notification,
// and either records the send (db.TouchToken) or, if APNs reports the token
// as permanently invalid, removes it (db.DeleteToken) so nothing tries it
// again.
func (s *srv) sendToToken(token string, payload apns.Payload) {
	badge, err := db.IncrementBadge(s.database, token)
	if err != nil {
		log.Printf("notify: incrementing badge for a device token: %v", err)
		// Not fatal to the send — fall back to a bare alert with no badge
		// number rather than skipping the notification entirely.
		badge = 0
	}

	payload.Badge = badge

	if err := s.apns.Send(token, payload); err != nil {
		if errors.Is(err, apns.ErrInvalidToken) {
			if delErr := db.DeleteToken(s.database, token); delErr != nil {
				log.Printf("notify: removing invalid device token: %v", delErr)
			}
		} else {
			log.Printf("notify: sending push: %v", err)
		}

		return
	}

	if err := db.TouchToken(s.database, token); err != nil {
		log.Printf("notify: recording send for a device token: %v", err)
	}
}
