package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tucats/idtrack/db"
)

// ---------------------------------------------------------------------------
// handleRegisterNotificationToken
// ---------------------------------------------------------------------------

func TestHandleRegisterNotificationToken_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	r := jsonReq(t, http.MethodPost, "/api/notifications/token", `{"token":"devtok1"}`, token)
	w := do(s, s.handleRegisterNotificationToken, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	toks, err := db.TokensForUser(s.database, "alice")
	if err != nil || len(toks) != 1 || toks[0] != "devtok1" {
		t.Errorf("tokens after register: %v, err %v", toks, err)
	}
}

func TestHandleRegisterNotificationToken_MissingToken(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	r := jsonReq(t, http.MethodPost, "/api/notifications/token", `{"token":"  "}`, token)
	w := do(s, s.handleRegisterNotificationToken, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// handleUnregisterNotificationToken
// ---------------------------------------------------------------------------

func TestHandleUnregisterNotificationToken_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "devtok1")

	r := jsonReq(t, http.MethodDelete, "/api/notifications/token/devtok1", "", token)
	r.SetPathValue("token", "devtok1")
	w := do(s, s.handleUnregisterNotificationToken, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	toks, _ := db.TokensForUser(s.database, "alice")
	if len(toks) != 0 {
		t.Errorf("expected token removed, got %v", toks)
	}
}

func TestHandleUnregisterNotificationToken_CannotDeleteAnotherUsersToken(t *testing.T) {
	s := newTestSrv(t)
	addTestUser(t, s, "alice", false)
	bobToken := addTestUser(t, s, "bob", false)
	db.RegisterToken(s.database, "alice", "devtok1")

	r := jsonReq(t, http.MethodDelete, "/api/notifications/token/devtok1", "", bobToken)
	r.SetPathValue("token", "devtok1")
	w := do(s, s.handleUnregisterNotificationToken, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d (this endpoint always reports success): %s", w.Code, http.StatusOK, w.Body.String())
	}

	toks, _ := db.TokensForUser(s.database, "alice")
	if len(toks) != 1 {
		t.Errorf("expected alice's token to survive bob's delete attempt, got %v", toks)
	}
}

// ---------------------------------------------------------------------------
// handleGetNotificationPrefs / handleUpdateNotificationPrefs
// ---------------------------------------------------------------------------

func TestHandleGetNotificationPrefs_DefaultsOn(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	r := jsonReq(t, http.MethodGet, "/api/notifications/prefs", "", token)
	w := do(s, s.handleGetNotificationPrefs, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var prefs db.NotificationPrefs
	if err := json.Unmarshal(w.Body.Bytes(), &prefs); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if !prefs.NewIssue || !prefs.NewComment || !prefs.Resolved {
		t.Errorf("expected all-on defaults, got %+v", prefs)
	}
}

func TestHandleUpdateNotificationPrefs_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	r := jsonReq(t, http.MethodPut, "/api/notifications/prefs", `{"new_issue":false,"new_comment":true,"resolved":false}`, token)
	w := do(s, s.handleUpdateNotificationPrefs, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	prefs, err := db.GetNotificationPrefs(s.database, "alice")
	if err != nil || prefs.NewIssue || !prefs.NewComment || prefs.Resolved {
		t.Errorf("prefs after update: %+v, err %v", prefs, err)
	}
}

func TestHandleUpdateNotificationPrefs_OnlyAffectsCaller(t *testing.T) {
	s := newTestSrv(t)
	aliceToken := addTestUser(t, s, "alice", false)
	addTestUser(t, s, "bob", false)

	r := jsonReq(t, http.MethodPut, "/api/notifications/prefs", `{"new_issue":false,"new_comment":false,"resolved":false}`, aliceToken)
	do(s, s.handleUpdateNotificationPrefs, r)

	bobPrefs, err := db.GetNotificationPrefs(s.database, "bob")
	if err != nil || !bobPrefs.NewIssue || !bobPrefs.NewComment || !bobPrefs.Resolved {
		t.Errorf("expected bob's prefs untouched, got %+v, err %v", bobPrefs, err)
	}
}

// ---------------------------------------------------------------------------
// handleResetNotificationBadge
// ---------------------------------------------------------------------------

func TestHandleResetNotificationBadge_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "devtok1")
	db.IncrementBadge(s.database, "devtok1")
	db.IncrementBadge(s.database, "devtok1")

	r := jsonReq(t, http.MethodPost, "/api/notifications/badge/reset", `{"token":"devtok1"}`, token)
	w := do(s, s.handleResetNotificationBadge, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}

	count, err := db.IncrementBadge(s.database, "devtok1")
	if err != nil || count != 1 {
		t.Errorf("expected badge to have reset to 0 (next increment = 1), got %d, err %v", count, err)
	}
}

func TestHandleResetNotificationBadge_MissingToken(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	r := jsonReq(t, http.MethodPost, "/api/notifications/badge/reset", `{"token":""}`, token)
	w := do(s, s.handleResetNotificationBadge, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}
