package server

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tucats/idtrack/db"
	"github.com/tucats/idtrack/internal/apns"
)

// ---------------------------------------------------------------------------
// truncateForNotification
// ---------------------------------------------------------------------------

func TestTruncateForNotification_ShortStringUnchanged(t *testing.T) {
	if got := truncateForNotification("hello", 200); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestTruncateForNotification_LongStringTruncated(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}

	got := truncateForNotification(long, 200)
	runes := []rune(got)

	if len(runes) != 201 || runes[200] != '…' {
		t.Errorf("expected 200 chars + ellipsis, got %d runes ending in %q", len(runes), string(runes[len(runes)-1]))
	}
}

func TestTruncateForNotification_MultiByteRunesNotSplit(t *testing.T) {
	// Each of these is a multi-byte UTF-8 rune; truncating by bytes instead
	// of runes would produce invalid UTF-8 or split a character in half.
	s := ""
	for i := 0; i < 5; i++ {
		s += "日"
	}

	got := truncateForNotification(s, 3)
	if got != "日日日…" {
		t.Errorf("got %q", got)
	}
}

// ---------------------------------------------------------------------------
// fakeSender — records every Send call for assertion, and can be told to
// fail with apns.ErrInvalidToken for a specific device token.
// ---------------------------------------------------------------------------

type recordedSend struct {
	token   string
	payload apns.Payload
}

type fakeSender struct {
	mu           sync.Mutex
	sends        []recordedSend
	invalidToken string // if non-empty, Send returns apns.ErrInvalidToken for this token
}

func (f *fakeSender) Send(token string, payload apns.Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sends = append(f.sends, recordedSend{token: token, payload: payload})

	if f.invalidToken != "" && token == f.invalidToken {
		return apns.ErrInvalidToken
	}

	return nil
}

func (f *fakeSender) recipients() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var tokens []string
	for _, s := range f.sends {
		tokens = append(tokens, s.token)
	}

	return tokens
}

// ---------------------------------------------------------------------------
// notify() / notifyOne() / sendToToken() — the core decision logic, tested
// directly (synchronously — these are NOT the `go s.notify(...)` call sites)
// so there is no goroutine-timing concern here.
// ---------------------------------------------------------------------------

func TestNotify_NoOpWhenApnsNotConfigured(t *testing.T) {
	s := newTestSrv(t)
	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")

	// s.apns is nil by default in newTestSrv — this must not panic and must
	// not attempt to send anything.
	s.notify([]string{"alice"}, notifyCategoryNewIssue, "T", "B", 1)
}

func TestNotify_RespectsDisabledPreference(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")
	db.SetNotificationPrefs(s.database, "alice", db.NotificationPrefs{NewIssue: false, NewComment: true, Resolved: true})

	s.notify([]string{"alice"}, notifyCategoryNewIssue, "T", "B", 1)

	if len(fake.sends) != 0 {
		t.Errorf("expected no send for a disabled category, got %v", fake.sends)
	}
}

func TestNotify_SendsToEveryTokenWhenEnabled(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")
	db.RegisterToken(s.database, "alice", "tok2")

	s.notify([]string{"alice"}, notifyCategoryNewComment, "T", "B", 7)

	if len(fake.sends) != 2 {
		t.Fatalf("expected 2 sends (one per device), got %d: %v", len(fake.sends), fake.sends)
	}

	for _, send := range fake.sends {
		if send.payload.IssueID != 7 || send.payload.Title != "T" || send.payload.Body != "B" {
			t.Errorf("unexpected payload: %+v", send.payload)
		}
	}
}

func TestNotify_DeduplicatesRecipients(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")

	s.notify([]string{"alice", "alice", "alice"}, notifyCategoryNewIssue, "T", "B", 1)

	if len(fake.sends) != 1 {
		t.Errorf("expected exactly one send despite duplicate recipients, got %d", len(fake.sends))
	}
}

func TestNotify_SkipsBlankUsernames(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	s.notify([]string{"", "", ""}, notifyCategoryNewIssue, "T", "B", 1)

	if len(fake.sends) != 0 {
		t.Errorf("expected no sends for blank usernames, got %d", len(fake.sends))
	}
}

func TestNotify_BadgeIncrementsPerSend(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")

	s.notify([]string{"alice"}, notifyCategoryNewIssue, "T", "B", 1)
	s.notify([]string{"alice"}, notifyCategoryNewIssue, "T", "B", 1)

	if len(fake.sends) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(fake.sends))
	}

	if fake.sends[0].payload.Badge != 1 || fake.sends[1].payload.Badge != 2 {
		t.Errorf("expected badge counts 1 then 2, got %d then %d", fake.sends[0].payload.Badge, fake.sends[1].payload.Badge)
	}
}

func TestNotify_InvalidTokenIsRemoved(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{invalidToken: "tok1"}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")

	s.notify([]string{"alice"}, notifyCategoryNewIssue, "T", "B", 1)

	toks, err := db.TokensForUser(s.database, "alice")
	if err != nil {
		t.Fatalf("TokensForUser: %v", err)
	}

	if len(toks) != 0 {
		t.Errorf("expected the invalid token to be removed, got %v", toks)
	}
}

// ---------------------------------------------------------------------------
// notify() / notifyOne() — web (in-app) fan-out. These confirm the behavior
// the user asked for explicitly: the web channel shares the exact same
// per-category preferences as push, and — unlike push — is never gated on
// s.apns being configured at all, since in-app notification is a property of
// the web client itself, not an operator-configured external integration.
// ---------------------------------------------------------------------------

func TestNotify_PublishesToWebEvenWithoutApnsConfigured(t *testing.T) {
	s := newTestSrv(t)
	s.webNotify = newWebNotifyHub()
	// s.apns is deliberately left nil here.

	addTestUser(t, s, "alice", false)

	ch, unsubscribe := s.webNotify.subscribe("alice")
	defer unsubscribe()

	s.notify([]string{"alice"}, notifyCategoryNewComment, "T", "B", 9)

	select {
	case evt := <-ch:
		if evt.Category != "new_comment" || evt.Title != "T" || evt.Body != "B" || evt.IssueID != 9 {
			t.Errorf("unexpected event: %+v", evt)
		}
	default:
		t.Fatal("expected a web notification event even with no APNs configured")
	}
}

func TestNotify_WebRespectsDisabledPreference(t *testing.T) {
	s := newTestSrv(t)
	s.webNotify = newWebNotifyHub()

	addTestUser(t, s, "alice", false)
	db.SetNotificationPrefs(s.database, "alice", db.NotificationPrefs{NewIssue: false, NewComment: true, Resolved: true})

	ch, unsubscribe := s.webNotify.subscribe("alice")
	defer unsubscribe()

	s.notify([]string{"alice"}, notifyCategoryNewIssue, "T", "B", 1)

	select {
	case evt := <-ch:
		t.Fatalf("expected no web event for a disabled category, got %+v", evt)
	default:
	}
}

func TestNotify_WebAndPushBothFireForTheSameEvent(t *testing.T) {
	s := newTestSrv(t)
	s.webNotify = newWebNotifyHub()
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "tok1")

	ch, unsubscribe := s.webNotify.subscribe("alice")
	defer unsubscribe()

	s.notify([]string{"alice"}, notifyCategoryStatusChanged, "T", "B", 4)

	if len(fake.sends) != 1 {
		t.Errorf("expected 1 push send, got %d", len(fake.sends))
	}

	select {
	case <-ch:
	default:
		t.Error("expected a web event alongside the push send")
	}
}

func TestNotify_UnknownUserIsSkipped(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	// No user "ghost" exists at all — GetNotificationPrefs returns (nil, nil).
	s.notify([]string{"ghost"}, notifyCategoryNewIssue, "T", "B", 1)

	if len(fake.sends) != 0 {
		t.Errorf("expected no send for a nonexistent user, got %d", len(fake.sends))
	}
}

// ---------------------------------------------------------------------------
// Handler-level trigger wiring. These go through the real HTTP handlers,
// which launch notify() with `go` — waitForSends polls with a timeout
// instead of a fixed sleep so the tests are fast in the common case and
// still deterministic under load.
// ---------------------------------------------------------------------------

func waitForSends(t *testing.T, fake *fakeSender, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		fake.mu.Lock()
		n := len(fake.sends)
		fake.mu.Unlock()

		if n >= want {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Give any extra (unwanted) sends a little more time to arrive before
	// asserting the final count, so an over-notification bug isn't masked
	// by returning as soon as `want` is reached.
	time.Sleep(50 * time.Millisecond)

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if len(fake.sends) != want {
		t.Fatalf("expected %d send(s), got %d: %v", want, len(fake.sends), fake.sends)
	}
}

func TestHandleCreateIssue_NotifiesAssignee(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	token := addTestUser(t, s, "alice", false)
	addTestUser(t, s, "bob", false)
	db.RegisterToken(s.database, "bob", "bobtok")

	body := `{"title":"My Issue","assignee":"bob"}`
	r := jsonReq(t, http.MethodPost, "/api/issues", body, token)
	w := do(s, s.handleCreateIssue, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d: %s", w.Code, w.Body.String())
	}

	waitForSends(t, fake, 1)

	if got := fake.recipients(); got[0] != "bobtok" {
		t.Errorf("expected notification to bob's token, got %v", got)
	}
}

func TestHandleCreateIssue_NoNotificationWhenUnassigned(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	token := addTestUser(t, s, "alice", false)

	body := `{"title":"My Issue"}`
	r := jsonReq(t, http.MethodPost, "/api/issues", body, token)
	do(s, s.handleCreateIssue, r)

	waitForSends(t, fake, 0)
}

func TestHandleCreateIssue_NoNotificationWhenSelfAssigned(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	token := addTestUser(t, s, "alice", false)
	db.RegisterToken(s.database, "alice", "alicetok")

	body := `{"title":"My Issue","assignee":"alice"}`
	r := jsonReq(t, http.MethodPost, "/api/issues", body, token)
	do(s, s.handleCreateIssue, r)

	waitForSends(t, fake, 0)
}

func TestHandleCreateComment_NotifiesBothWhenThirdPartyComments(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false) // reporter
	addTestUser(t, s, "bob", false)   // assignee
	adminToken := addTestUser(t, s, "carol", true)
	db.RegisterToken(s.database, "alice", "alicetok")
	db.RegisterToken(s.database, "bob", "bobtok")

	db.CreateIssue(s.database, "T", "", "alice", "bob", "Medium", "", "", "")

	r := jsonReq(t, http.MethodPost, "/api/issues/1", `{"body":"a comment"}`, adminToken)
	r.SetPathValue("id", "1")
	do(s, s.handleCreateComment, r)

	waitForSends(t, fake, 2)

	got := fake.recipients()
	if !(containsStr(got, "alicetok") && containsStr(got, "bobtok")) {
		t.Errorf("expected both alice and bob notified, got %v", got)
	}
}

func TestHandleCreateComment_ExcludesCommentAuthor(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	aliceToken := addTestUser(t, s, "alice", false) // reporter, will comment
	addTestUser(t, s, "bob", false)                 // assignee
	db.RegisterToken(s.database, "alice", "alicetok")
	db.RegisterToken(s.database, "bob", "bobtok")

	db.CreateIssue(s.database, "T", "", "alice", "bob", "Medium", "", "", "")

	r := jsonReq(t, http.MethodPost, "/api/issues/1", `{"body":"a comment"}`, aliceToken)
	r.SetPathValue("id", "1")
	do(s, s.handleCreateComment, r)

	waitForSends(t, fake, 1)

	if got := fake.recipients(); got[0] != "bobtok" {
		t.Errorf("expected only bob (not the comment author alice) notified, got %v", got)
	}
}

func TestHandleCreateComment_NoNotificationWhenReporterIsAssignee(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	addTestUser(t, s, "alice", false)
	adminToken := addTestUser(t, s, "carol", true)
	db.RegisterToken(s.database, "alice", "alicetok")

	// alice is both reporter and assignee.
	db.CreateIssue(s.database, "T", "", "alice", "alice", "Medium", "", "", "")

	r := jsonReq(t, http.MethodPost, "/api/issues/1", `{"body":"a comment"}`, adminToken)
	r.SetPathValue("id", "1")
	do(s, s.handleCreateComment, r)

	waitForSends(t, fake, 0)
}

func TestHandleUpdateIssue_NotifiesNonActor(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	aliceToken := addTestUser(t, s, "alice", false) // reporter; will make the change
	addTestUser(t, s, "bob", false)                 // assignee
	db.RegisterToken(s.database, "bob", "bobtok")

	db.CreateIssue(s.database, "T", "", "alice", "bob", "Medium", "", "", "")

	body := `{"title":"T","priority":"Medium","status":"Resolved","assignee":"bob"}`
	r := jsonReq(t, http.MethodPut, "/api/issues/1", body, aliceToken)
	r.SetPathValue("id", "1")
	w := do(s, s.handleUpdateIssue, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d: %s", w.Code, w.Body.String())
	}

	waitForSends(t, fake, 1)

	if got := fake.recipients(); got[0] != "bobtok" {
		t.Errorf("expected only bob (not the actor alice) notified, got %v", got)
	}
}

func TestHandleUpdateIssue_NoNotificationWhenStatusUnchanged(t *testing.T) {
	s := newTestSrv(t)
	fake := &fakeSender{}
	s.apns = fake

	aliceToken := addTestUser(t, s, "alice", false)
	addTestUser(t, s, "bob", false)
	db.RegisterToken(s.database, "bob", "bobtok")

	db.CreateIssue(s.database, "T", "", "alice", "bob", "Medium", "", "", "")

	body := `{"title":"T Updated","priority":"High","status":"Open","assignee":"bob"}`
	r := jsonReq(t, http.MethodPut, "/api/issues/1", body, aliceToken)
	r.SetPathValue("id", "1")
	do(s, s.handleUpdateIssue, r)

	waitForSends(t, fake, 0)
}

func containsStr(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}

	return false
}
