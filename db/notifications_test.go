package db_test

import (
	"testing"

	"github.com/tucats/idtrack/db"
)

func TestRegisterToken_And_TokensForUser(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})

	if err := db.RegisterToken(d, "alice", "tok1"); err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}

	toks, err := db.TokensForUser(d, "alice")
	if err != nil {
		t.Fatalf("TokensForUser: %v", err)
	}

	if len(toks) != 1 || toks[0] != "tok1" {
		t.Errorf("tokens: got %v, want [tok1]", toks)
	}
}

func TestRegisterToken_ReassignsOnConflict(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})
	db.AddUser(d, "bob", "Bob", "pw", []string{"any"})

	db.RegisterToken(d, "alice", "shared-token")
	db.RegisterToken(d, "bob", "shared-token")

	toks, _ := db.TokensForUser(d, "alice")
	if len(toks) != 0 {
		t.Errorf("expected alice to lose the token on reassignment, got %v", toks)
	}

	toks, _ = db.TokensForUser(d, "bob")
	if len(toks) != 1 {
		t.Errorf("expected bob to own the token after reassignment, got %v", toks)
	}
}

func TestDeleteToken(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})
	db.RegisterToken(d, "alice", "tok1")

	if err := db.DeleteToken(d, "tok1"); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}

	toks, _ := db.TokensForUser(d, "alice")
	if len(toks) != 0 {
		t.Errorf("expected no tokens after delete, got %v", toks)
	}
}

func TestDeleteTokenForUser_ScopedToOwner(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})
	db.AddUser(d, "bob", "Bob", "pw", []string{"any"})
	db.RegisterToken(d, "alice", "tok1")

	// bob attempting to delete alice's token should be a no-op.
	if err := db.DeleteTokenForUser(d, "bob", "tok1"); err != nil {
		t.Fatalf("DeleteTokenForUser (wrong owner): %v", err)
	}

	toks, _ := db.TokensForUser(d, "alice")
	if len(toks) != 1 {
		t.Fatalf("expected alice to still own the token, got %v", toks)
	}

	if err := db.DeleteTokenForUser(d, "alice", "tok1"); err != nil {
		t.Fatalf("DeleteTokenForUser (correct owner): %v", err)
	}

	toks, _ = db.TokensForUser(d, "alice")
	if len(toks) != 0 {
		t.Fatalf("expected token deleted, got %v", toks)
	}
}

func TestIncrementBadge_And_ResetBadge(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})
	db.RegisterToken(d, "alice", "tok1")

	c, err := db.IncrementBadge(d, "tok1")
	if err != nil || c != 1 {
		t.Fatalf("first increment: got %d, %v; want 1, nil", c, err)
	}

	c, err = db.IncrementBadge(d, "tok1")
	if err != nil || c != 2 {
		t.Fatalf("second increment: got %d, %v; want 2, nil", c, err)
	}

	if err := db.ResetBadge(d, "tok1"); err != nil {
		t.Fatalf("ResetBadge: %v", err)
	}

	c, err = db.IncrementBadge(d, "tok1")
	if err != nil || c != 1 {
		t.Fatalf("increment after reset: got %d, %v; want 1, nil", c, err)
	}
}

func TestNotificationPrefs_DefaultOn(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})

	prefs, err := db.GetNotificationPrefs(d, "alice")
	if err != nil {
		t.Fatalf("GetNotificationPrefs: %v", err)
	}

	if prefs == nil || !prefs.NewIssue || !prefs.NewComment || !prefs.Resolved {
		t.Errorf("expected all-on defaults, got %+v", prefs)
	}
}

func TestSetNotificationPrefs(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "alice", "Alice", "pw", []string{"any"})

	if err := db.SetNotificationPrefs(d, "alice", db.NotificationPrefs{NewIssue: false, NewComment: true, Resolved: false}); err != nil {
		t.Fatalf("SetNotificationPrefs: %v", err)
	}

	prefs, err := db.GetNotificationPrefs(d, "alice")
	if err != nil {
		t.Fatalf("GetNotificationPrefs: %v", err)
	}

	if prefs.NewIssue || !prefs.NewComment || prefs.Resolved {
		t.Errorf("prefs after set: got %+v, want {false true false}", prefs)
	}
}

func TestGetNotificationPrefs_NotFound(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	prefs, err := db.GetNotificationPrefs(d, "nobody")
	if err != nil {
		t.Fatalf("GetNotificationPrefs: %v", err)
	}

	if prefs != nil {
		t.Errorf("expected nil for nonexistent user, got %+v", prefs)
	}
}
