package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tucats/idtrack/db"
)

// This file implements the self-service push-notification endpoints: device
// token registration/unregistration, per-user notification preferences, and
// badge-count reset. All five endpoints require an authenticated session and
// operate only on currentUser(r) — none of them takes a username in the
// path or body — following the same self-service pattern as the WebAuthn
// credential endpoints in webauthn.go. See docs/NOTIFICATIONS.md for the
// overall design.

// handleRegisterNotificationToken serves POST /api/notifications/token.
// Called by the client on every login and app relaunch (not just once at
// onboarding) — see docs/NOTIFICATIONS.md §4.4 — since an APNs device token
// can change at any time and the server-side upsert makes a repeat
// registration of an unchanged token a cheap no-op.
//
// Request body (JSON): {"token"} — the APNs device token, hex-encoded;
// required (rejected if blank after trimming).
//
// Response (200 OK): {"ok": true}.
// Errors: 400 missing token; 500 on db error.
func (s *srv) handleRegisterNotificationToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	token := strings.TrimSpace(body.Token)
	if token == "" {
		jsonError(w, "token is required", http.StatusBadRequest)

		return
	}

	if err := db.RegisterToken(s.database, currentUser(r).Username, token); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleUnregisterNotificationToken serves DELETE /api/notifications/token/{token}.
// Called on sign-out so a stale token doesn't linger against an account the
// user has since signed out of on this device (best-effort on the client
// side — see AppState.signOut in the iOS app).
//
// db.DeleteTokenForUser scopes the delete to the caller's own username
// (mirroring handleWebAuthnDeleteCredential's use of db.DeleteCredential),
// so this can never remove a token belonging to a different account.
//
// Response (200 OK): {"ok": true}, whether or not a matching row existed —
// same "always succeeds" shape as handleWebAuthnDeleteCredential/handleLogout.
func (s *srv) handleUnregisterNotificationToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	if err := db.DeleteTokenForUser(s.database, currentUser(r).Username, token); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetNotificationPrefs serves GET /api/notifications/prefs. Returns the
// caller's own three-category opt-in/opt-out state — never another user's,
// since db.User's shared struct (backing GET /api/users) deliberately does
// not carry these fields (see db/notifications.go's NotificationPrefs doc
// comment).
//
// Response (200 OK): {"new_issue", "new_comment", "resolved"} (all bool).
// Errors: 500 on db error (a nil result is not expected here — the caller is
// always an existing, authenticated user).
func (s *srv) handleGetNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	prefs, err := db.GetNotificationPrefs(s.database, currentUser(r).Username)
	if err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, prefs)
}

// handleUpdateNotificationPrefs serves PUT /api/notifications/prefs. Replaces
// all three preference fields at once — there is no partial-update variant,
// since the client's Settings screen always sends the full toggle state
// together (see docs/NOTIFICATIONS.md §4.7).
//
// Request body (JSON): {"new_issue", "new_comment", "resolved"} (all bool).
// Response (200 OK): {"ok": true}.
// Errors: 400 malformed body; 500 on db error.
func (s *srv) handleUpdateNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	var prefs db.NotificationPrefs

	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	if err := db.SetNotificationPrefs(s.database, currentUser(r).Username, prefs); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleResetNotificationBadge serves POST /api/notifications/badge/reset.
// Called by the client when the app becomes active on a device, so the next
// push notification to that device starts counting its badge number from
// zero again (see db.IncrementBadge/db.ResetBadge — APNs itself has no
// notion of "the current badge count," so the provider must track it).
//
// Request body (JSON): {"token"} — the device whose badge count should be
// reset; required. Deliberately takes a single token rather than resetting
// every token the caller owns, since badge count is inherently per-device —
// opening the app on an iPhone shouldn't zero an iPad's badge.
//
// Response (200 OK): {"ok": true}, whether or not the token exists (matching
// the "always succeeds" shape used elsewhere in this file) — db.ResetBadge's
// UPDATE is simply a no-op if the token row is gone.
//
// Errors: 400 missing token; 500 on db error.
func (s *srv) handleResetNotificationBadge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	token := strings.TrimSpace(body.Token)
	if token == "" {
		jsonError(w, "token is required", http.StatusBadRequest)

		return
	}

	if err := db.ResetBadge(s.database, token); err != nil {
		internalError(w, err)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
