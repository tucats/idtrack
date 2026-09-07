package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// syncRecorder is a minimal, mutex-protected http.ResponseWriter+http.Flusher
// used only for testing handleNotificationStream. httptest.ResponseRecorder
// is not safe for concurrent use, but these tests need to observe the
// handler's progress (has it flushed yet? what has it written so far?) from
// the test goroutine while the handler goroutine is still running and
// writing — something a real client connection handles via the network
// stack instead of shared memory. snapshot() is the only way the test reads
// state, and it takes the same lock every write goes through, so there is no
// data race (verified under -race).
type syncRecorder struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	flushed bool
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{header: make(http.Header)}
}

func (r *syncRecorder) Header() http.Header { return r.header }

func (r *syncRecorder) WriteHeader(int) {}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.buf.Write(p)
}

func (r *syncRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.flushed = true
}

func (r *syncRecorder) snapshot() (flushed bool, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.flushed, r.buf.String()
}

// ---------------------------------------------------------------------------
// webNotifyHub — subscribe/publish/unsubscribe, tested directly and
// synchronously (no goroutine-timing concern — publish blocks only long
// enough to do a non-blocking channel send).
// ---------------------------------------------------------------------------

func TestWebNotifyHub_PublishDeliversToSubscriber(t *testing.T) {
	h := newWebNotifyHub()

	ch, unsubscribe := h.subscribe("alice")
	defer unsubscribe()

	h.publish("alice", webNotifyEvent{Category: "new_issue", Title: "T", Body: "B", IssueID: 7})

	select {
	case evt := <-ch:
		if evt.Category != "new_issue" || evt.Title != "T" || evt.Body != "B" || evt.IssueID != 7 {
			t.Errorf("unexpected event: %+v", evt)
		}
	default:
		t.Fatal("expected an event to be immediately available")
	}
}

func TestWebNotifyHub_PublishIgnoresOtherUsers(t *testing.T) {
	h := newWebNotifyHub()

	ch, unsubscribe := h.subscribe("alice")
	defer unsubscribe()

	h.publish("bob", webNotifyEvent{Title: "T"})

	select {
	case evt := <-ch:
		t.Fatalf("expected no event for alice, got %+v", evt)
	default:
	}
}

func TestWebNotifyHub_FanOutToMultipleSubscribers(t *testing.T) {
	h := newWebNotifyHub()

	ch1, unsub1 := h.subscribe("alice")
	defer unsub1()

	ch2, unsub2 := h.subscribe("alice")
	defer unsub2()

	h.publish("alice", webNotifyEvent{Title: "T"})

	for i, ch := range []<-chan webNotifyEvent{ch1, ch2} {
		select {
		case <-ch:
		default:
			t.Fatalf("subscriber %d did not receive the event", i)
		}
	}
}

func TestWebNotifyHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := newWebNotifyHub()

	ch, unsubscribe := h.subscribe("alice")
	unsubscribe()

	h.publish("alice", webNotifyEvent{Title: "T"})

	if _, ok := <-ch; ok {
		t.Error("expected the channel to be closed after unsubscribe")
	}
}

func TestWebNotifyHub_PublishNeverBlocksOnFullSubscriber(t *testing.T) {
	h := newWebNotifyHub()

	ch, unsubscribe := h.subscribe("alice")
	defer unsubscribe()

	done := make(chan struct{})

	go func() {
		// Fill the subscriber's buffer, then publish well past capacity —
		// this must never block, since notify() calls this synchronously
		// per recipient and a stalled tab must not delay every other one.
		for i := 0; i < webNotifyEventBuffer+5; i++ {
			h.publish("alice", webNotifyEvent{IssueID: int64(i)})
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a full subscriber buffer")
	}

	// Drain what's there; exact count isn't the point (buffer-full drops are
	// expected), just that nothing panicked or deadlocked above.
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestWebNotifyHub_PublishToUnknownUserIsNoOp(t *testing.T) {
	h := newWebNotifyHub()

	// Must not panic on a username with no subscribers at all.
	h.publish("ghost", webNotifyEvent{Title: "T"})
}

// ---------------------------------------------------------------------------
// handleNotificationStream — a genuinely long-lived handler. The test drives
// it with a cancellable request context (standing in for the client
// disconnecting) and only reads the response recorder's buffer after the
// handler goroutine has fully returned, so there is no data race between the
// handler's writes and the test's read.
// ---------------------------------------------------------------------------

func TestHandleNotificationStream_DeliversPublishedEvent(t *testing.T) {
	s := newTestSrv(t)
	s.webNotify = newWebNotifyHub()

	token := addTestUser(t, s, "alice", false)

	ctx, cancel := context.WithCancel(context.Background())

	r := httptest.NewRequest(http.MethodGet, "/api/notifications/stream", nil)
	r = r.WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})

	// s.auth expects a real session; drive the handler directly (as
	// notify_test.go's other handler tests do) via the auth middleware so
	// currentUser(r) is populated the same way a real request would be.
	// A plain httptest.ResponseRecorder isn't safe for the test goroutine to
	// poll while the handler goroutine is still writing to it — see
	// syncRecorder's doc comment.
	w := newSyncRecorder()

	done := make(chan struct{})

	go func() {
		s.auth(http.HandlerFunc(s.handleNotificationStream)).ServeHTTP(w, r)
		close(done)
	}()

	// Give the handler a moment to subscribe before publishing.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if flushed, _ := w.snapshot(); flushed {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	s.webNotify.publish("alice", webNotifyEvent{Category: "new_issue", Title: "Hi", Body: "There", IssueID: 3})

	// Wait for the event to be flushed into the recorder.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, body := w.snapshot(); contains(body, `"issue_id":3`) {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after context cancellation")
	}

	_, body := w.snapshot()
	if want := `"title":"Hi"`; !contains(body, want) {
		t.Errorf("expected body to contain %q, got %q", want, body)
	}

	if want := `"issue_id":3`; !contains(body, want) {
		t.Errorf("expected body to contain %q, got %q", want, body)
	}
}

func TestHandleNotificationStream_UnsubscribesOnDisconnect(t *testing.T) {
	s := newTestSrv(t)
	s.webNotify = newWebNotifyHub()

	token := addTestUser(t, s, "alice", false)

	ctx, cancel := context.WithCancel(context.Background())

	r := httptest.NewRequest(http.MethodGet, "/api/notifications/stream", nil)
	r = r.WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})

	w := newSyncRecorder()

	done := make(chan struct{})

	go func() {
		s.auth(http.HandlerFunc(s.handleNotificationStream)).ServeHTTP(w, r)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if flushed, _ := w.snapshot(); flushed {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after context cancellation")
	}

	s.webNotify.mu.Lock()
	remaining := len(s.webNotify.subs["alice"])
	s.webNotify.mu.Unlock()

	if remaining != 0 {
		t.Errorf("expected subscriber to be removed after disconnect, got %d remaining", remaining)
	}
}
