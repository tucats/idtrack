package server

import (
	"testing"
	"time"
)

func TestSessionStore_CreateAndLookup(t *testing.T) {
	ss := newSessionStore()

	token := ss.create("alice", time.Hour)

	if len(token) != 64 {
		t.Errorf("token length: got %d, want 64", len(token))
	}

	username, ok := ss.lookup(token)
	if !ok {
		t.Fatal("expected lookup to succeed")
	}

	if username != "alice" {
		t.Errorf("username: got %q, want %q", username, "alice")
	}
}

func TestSessionStore_LookupNotFound(t *testing.T) {
	ss := newSessionStore()

	_, ok := ss.lookup("nonexistent_token")
	if ok {
		t.Error("lookup of nonexistent token should return false")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	ss := newSessionStore()

	token := ss.create("bob", time.Hour)
	ss.delete(token)

	_, ok := ss.lookup(token)
	if ok {
		t.Error("lookup of deleted token should return false")
	}
}

func TestSessionStore_DeleteNonexistent(t *testing.T) {
	ss := newSessionStore()

	// Should not panic or error.
	ss.delete("no-such-token")
}

func TestSessionStore_Expired(t *testing.T) {
	ss := newSessionStore()

	// Create a session that is already expired.
	token := ss.create("carol", -time.Second)

	_, ok := ss.lookup(token)
	if ok {
		t.Error("expired session should not be found")
	}
}

func TestSessionStore_ExpiredIsEvicted(t *testing.T) {
	ss := newSessionStore()

	// Create expired session and look it up (evicts it).
	token := ss.create("dave", -time.Second)
	ss.lookup(token)

	// The map should not grow with dead entries.
	ss.mu.Lock()
	n := len(ss.sessions)
	ss.mu.Unlock()

	if n != 0 {
		t.Errorf("expected 0 sessions after eviction, got %d", n)
	}
}

func TestSessionStore_MultipleUsers(t *testing.T) {
	ss := newSessionStore()

	t1 := ss.create("user1", time.Hour)
	t2 := ss.create("user2", time.Hour)

	u1, ok1 := ss.lookup(t1)
	u2, ok2 := ss.lookup(t2)

	if !ok1 || u1 != "user1" {
		t.Errorf("user1: ok=%v, got %q", ok1, u1)
	}

	if !ok2 || u2 != "user2" {
		t.Errorf("user2: ok=%v, got %q", ok2, u2)
	}
}

func TestSessionStore_UniqueTokens(t *testing.T) {
	ss := newSessionStore()

	tokens := make(map[string]struct{}, 10)
	for i := 0; i < 10; i++ {
		tok := ss.create("u", time.Hour)
		if _, dup := tokens[tok]; dup {
			t.Fatalf("duplicate token generated")
		}

		tokens[tok] = struct{}{}
	}
}
