package db_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/tucats/idtrack/db"
)

// ---------------------------------------------------------------------------
// Schema / Open
// ---------------------------------------------------------------------------

func TestOpen_CreatesSchema(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// HasUsers should succeed (schema exists) and return false (no users yet).
	has, err := db.HasUsers(d)
	if err != nil {
		t.Fatalf("HasUsers after Open: %v", err)
	}

	if has {
		t.Error("fresh database should have no users")
	}
}

func TestOpen_Idempotent(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer d.Close()

	// A second initSchema call should not fail (IF NOT EXISTS guards).
	// We approximate this by checking that basic operations still work.
	if err := db.AddUser(d, "u", "U", "pass", []string{"any"}); err != nil {
		t.Fatalf("AddUser after reopen: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Password helpers
// ---------------------------------------------------------------------------

func TestIsLegacyHash(t *testing.T) {
	legacy := fmt.Sprintf("%x", sha256.Sum256([]byte("password")))

	if !db.IsLegacyHash(legacy) {
		t.Error("expected 64-char SHA-256 hex to be recognized as legacy")
	}

	if db.IsLegacyHash("$2a$10$somebcrypthash") {
		t.Error("bcrypt hash should not be recognized as legacy")
	}

	if db.IsLegacyHash("short") {
		t.Error("short string should not be recognized as legacy")
	}
}

func TestVerifyPassword_Bcrypt(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.AddUser(d, "alice", "Alice", "secret", []string{"any"}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	u, err := db.FindUser(d, "alice")
	if err != nil || u == nil {
		t.Fatalf("FindUser: %v", err)
	}

	if !db.VerifyPassword(u.PasswordHash, "secret") {
		t.Error("correct password should verify")
	}

	if db.VerifyPassword(u.PasswordHash, "wrong") {
		t.Error("wrong password should not verify")
	}
}

func TestVerifyPassword_Legacy(t *testing.T) {
	legacy := fmt.Sprintf("%x", sha256.Sum256([]byte("legacypass")))

	if !db.VerifyPassword(legacy, "legacypass") {
		t.Error("correct password should verify against legacy SHA-256 hash")
	}

	if db.VerifyPassword(legacy, "wrongpass") {
		t.Error("wrong password should not verify against legacy SHA-256 hash")
	}
}

func TestUpgradePasswordHash(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.AddUser(d, "bob", "Bob", "pass", []string{"any"}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	if err := db.UpgradePasswordHash(d, "bob", "newpass"); err != nil {
		t.Fatalf("UpgradePasswordHash: %v", err)
	}

	u, err := db.FindUser(d, "bob")
	if err != nil || u == nil {
		t.Fatalf("FindUser after upgrade: %v", err)
	}

	if !db.VerifyPassword(u.PasswordHash, "newpass") {
		t.Error("upgraded hash should verify against new password")
	}

	if db.VerifyPassword(u.PasswordHash, "pass") {
		t.Error("old password should not verify after upgrade")
	}
}

// ---------------------------------------------------------------------------
// User CRUD
// ---------------------------------------------------------------------------

func TestAddUser_And_FindUser(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.AddUser(d, "carol", "Carol Smith", "pw", []string{"admin"}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	u, err := db.FindUser(d, "carol")
	if err != nil {
		t.Fatalf("FindUser: %v", err)
	}

	if u == nil {
		t.Fatal("expected user to be found")
	}

	if u.Username != "carol" {
		t.Errorf("username: got %q, want %q", u.Username, "carol")
	}

	if u.DisplayName != "Carol Smith" {
		t.Errorf("display_name: got %q, want %q", u.DisplayName, "Carol Smith")
	}

	if !u.IsAdmin {
		t.Error("expected is_admin=true for user with admin team")
	}

	if !db.ContainsTeam(u.Teams, "admin") {
		t.Error("expected teams to contain admin")
	}
}

func TestFindUser_NotFound(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	u, err := db.FindUser(d, "nobody")
	if err != nil {
		t.Fatalf("FindUser: %v", err)
	}

	if u != nil {
		t.Error("expected nil for nonexistent user")
	}
}

func TestAddUser_Upsert(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "dave", "Dave", "pass1", []string{"any"})
	db.AddUser(d, "dave", "Dave Updated", "pass2", []string{"admin"})

	u, _ := db.FindUser(d, "dave")
	if u.DisplayName != "Dave Updated" {
		t.Errorf("display_name after upsert: got %q, want %q", u.DisplayName, "Dave Updated")
	}

	if !u.IsAdmin {
		t.Error("expected is_admin=true after upsert with admin team")
	}
}

func TestDeleteUser(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "eve", "Eve", "pw", []string{"any"})

	if err := db.DeleteUser(d, "eve"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	u, err := db.FindUser(d, "eve")
	if err != nil {
		t.Fatalf("FindUser after delete: %v", err)
	}

	if u != nil {
		t.Error("expected nil after deletion")
	}
}

func TestUpdateUser_DisplayName(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "frank", "Frank", "pw", []string{"any"})

	if err := db.UpdateUser(d, "frank", "Franklin", "", nil, nil); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	u, _ := db.FindUser(d, "frank")
	if u.DisplayName != "Franklin" {
		t.Errorf("display_name: got %q, want %q", u.DisplayName, "Franklin")
	}
}

func TestUpdateUser_Password(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "grace", "Grace", "old", []string{"any"})
	db.UpdateUser(d, "grace", "", "new", nil, nil)

	u, _ := db.FindUser(d, "grace")
	if !db.VerifyPassword(u.PasswordHash, "new") {
		t.Error("password should verify after update")
	}
}

func TestUpdateUser_AdminFlag(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "hank", "Hank", "pw", []string{"any"})

	tr := true
	db.UpdateUser(d, "hank", "", "", &tr, nil)

	u, _ := db.FindUser(d, "hank")
	if !u.IsAdmin {
		t.Error("expected is_admin=true after setting admin via isAdmin flag")
	}

	if !db.ContainsTeam(u.Teams, "admin") {
		t.Error("expected teams to contain admin after setting admin flag")
	}
}

func TestUpdateUser_TeamsDirectly(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "iris", "Iris", "pw", []string{"any"})
	db.UpdateUser(d, "iris", "", "", nil, []string{"platform", "database"})

	u, _ := db.FindUser(d, "iris")
	if !db.ContainsTeam(u.Teams, "platform") {
		t.Error("expected teams to contain platform")
	}

	if !db.ContainsTeam(u.Teams, "database") {
		t.Error("expected teams to contain database")
	}

	if u.IsAdmin {
		t.Error("expected is_admin=false for non-admin teams")
	}
}

func TestUpdateUser_NilAdminPreserves(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "ivy", "Ivy", "pw", []string{"admin"})
	db.UpdateUser(d, "ivy", "Ivy Updated", "", nil, nil)

	u, _ := db.FindUser(d, "ivy")
	if !u.IsAdmin {
		t.Error("is_admin should not be cleared when both isAdmin and teams params are nil")
	}
}

func TestUpdateUser_NotFound(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	err := db.UpdateUser(d, "nobody", "Name", "", nil, nil)
	if err == nil {
		t.Error("expected error updating nonexistent user")
	}
}

func TestUpdateUser_NoOp(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "jay", "Jay", "pw", []string{"any"})

	// All empty — should be a no-op (no error).
	if err := db.UpdateUser(d, "jay", "", "", nil, nil); err != nil {
		t.Fatalf("UpdateUser no-op: %v", err)
	}
}

func TestListUsers(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "b", "B", "pw", []string{"any"})
	db.AddUser(d, "a", "A", "pw", []string{"any"})
	db.AddUser(d, "c", "C", "pw", []string{"any"})

	users, err := db.ListUsers(d)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}

	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}

	// Should be alphabetically ordered.
	if users[0].Username != "a" || users[1].Username != "b" || users[2].Username != "c" {
		t.Errorf("unexpected order: %v", []string{users[0].Username, users[1].Username, users[2].Username})
	}
}

func TestListUsers_Empty(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	users, err := db.ListUsers(d)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}

	if users == nil {
		t.Error("expected non-nil empty slice")
	}

	if len(users) != 0 {
		t.Errorf("expected 0 users, got %d", len(users))
	}
}

func TestHasUsers(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	has, err := db.HasUsers(d)
	if err != nil || has {
		t.Errorf("HasUsers on empty db: err=%v, has=%v", err, has)
	}

	db.AddUser(d, "x", "X", "pw", []string{"any"})

	has, err = db.HasUsers(d)
	if err != nil || !has {
		t.Errorf("HasUsers after add: err=%v, has=%v", err, has)
	}
}

func TestCountAdmins(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	n, err := db.CountAdmins(d)
	if err != nil || n != 0 {
		t.Errorf("CountAdmins on empty db: err=%v, n=%d", err, n)
	}

	db.AddUser(d, "u1", "U1", "pw", []string{"any"})
	db.AddUser(d, "u2", "U2", "pw", []string{"admin"})
	db.AddUser(d, "u3", "U3", "pw", []string{"admin", "platform"})

	n, err = db.CountAdmins(d)
	if err != nil || n != 2 {
		t.Errorf("CountAdmins: err=%v, n=%d (want 2)", err, n)
	}
}

func TestRecordLogin(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "kim", "Kim", "pw", []string{"any"})

	if err := db.RecordLogin(d, "kim"); err != nil {
		t.Fatalf("RecordLogin: %v", err)
	}

	u, _ := db.FindUser(d, "kim")
	if u.LastLoginAt == "" {
		t.Error("last_login_at should be set after RecordLogin")
	}
}

// ---------------------------------------------------------------------------
// Team migration backfill
// ---------------------------------------------------------------------------

func TestMigration_AdminTeam(t *testing.T) {
	// Open a fresh DB; addColumnIfMissing populates teams from is_admin.
	// Since we insert directly via AddUser, verify that admin users have the
	// admin team and non-admin users have the any team.
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "adm", "Admin", "pw", []string{"admin"})
	db.AddUser(d, "reg", "Regular", "pw", []string{"any"})

	adm, _ := db.FindUser(d, "adm")
	reg, _ := db.FindUser(d, "reg")

	if !adm.IsAdmin {
		t.Error("admin user should have IsAdmin=true")
	}

	if !db.ContainsTeam(adm.Teams, "admin") {
		t.Error("admin user should have admin in teams")
	}

	if reg.IsAdmin {
		t.Error("regular user should have IsAdmin=false")
	}

	if !db.ContainsTeam(reg.Teams, "any") {
		t.Error("regular user should have any in teams")
	}
}

// ---------------------------------------------------------------------------
// Team CRUD
// ---------------------------------------------------------------------------

func TestCreateTeam_And_ListTeams(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.CreateTeam(d, "platform", "Platform engineering"); err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	teams, err := db.ListTeams(d)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}

	// Should include reserved teams + the new one.
	found := false

	for _, team := range teams {
		if team.Name == "platform" {
			found = true

			if team.Description != "Platform engineering" {
				t.Errorf("description: got %q, want %q", team.Description, "Platform engineering")
			}
		}
	}

	if !found {
		t.Error("expected platform team to be listed")
	}
}

func TestCreateTeam_Reserved(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.CreateTeam(d, "admin", ""); err == nil {
		t.Error("expected error creating reserved team 'admin'")
	}

	if err := db.CreateTeam(d, "any", ""); err == nil {
		t.Error("expected error creating reserved team 'any'")
	}
}

func TestDeleteTeam_Reserved(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.DeleteTeam(d, "admin"); err == nil {
		t.Error("expected error deleting reserved team 'admin'")
	}
}

func TestDeleteTeam_InUse(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateTeam(d, "myteam", "")
	db.AddUser(d, "u1", "U1", "pw", []string{"myteam"})

	if err := db.DeleteTeam(d, "myteam"); err == nil {
		t.Error("expected error deleting team that is in use by a user")
	}
}

func TestDeleteTeam_NotInUse(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateTeam(d, "unused", "")

	if err := db.DeleteTeam(d, "unused"); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}

	teams, _ := db.ListTeams(d)
	for _, team := range teams {
		if team.Name == "unused" {
			t.Error("expected team to be deleted")
		}
	}
}

func TestUpdateTeam_Description(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateTeam(d, "infra", "")

	if err := db.UpdateTeam(d, "infra", "", "Infrastructure team"); err != nil {
		t.Fatalf("UpdateTeam: %v", err)
	}

	teams, _ := db.ListTeams(d)
	for _, team := range teams {
		if team.Name == "infra" {
			if team.Description != "Infrastructure team" {
				t.Errorf("description: got %q, want %q", team.Description, "Infrastructure team")
			}
			
			return
		}
	}

	t.Error("infra team not found after update")
}

func TestUpdateTeam_Rename(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateTeam(d, "platform", "")
	db.AddUser(d, "u1", "U1", "pw", []string{"platform"})
	db.CreateProject(d, "proj", []string{"platform"})

	if err := db.UpdateTeam(d, "platform", "infra", ""); err != nil {
		t.Fatalf("UpdateTeam rename: %v", err)
	}

	// User should now have "infra" team.
	u, _ := db.FindUser(d, "u1")
	if !db.ContainsTeam(u.Teams, "infra") {
		t.Error("expected user teams to contain 'infra' after rename")
	}

	if db.ContainsTeam(u.Teams, "platform") {
		t.Error("expected user teams to NOT contain 'platform' after rename")
	}

	// Project should now have "infra" team.
	projects, _ := db.ListProjects(d)
	for _, p := range projects {
		if p.Name == "proj" {
			if !db.ContainsTeam(p.Teams, "infra") {
				t.Error("expected project teams to contain 'infra' after rename")
			}
		}
	}
}

func TestUpdateTeam_RenameReserved(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.UpdateTeam(d, "admin", "superadmin", ""); err == nil {
		t.Error("expected error renaming reserved team 'admin'")
	}
}

func TestUpdateTeam_DescriptionReserved(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	// Updating description of a reserved team should succeed.
	if err := db.UpdateTeam(d, "admin", "", "Full admin access"); err != nil {
		t.Fatalf("UpdateTeam description on reserved: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Issue CRUD
// ---------------------------------------------------------------------------

func TestCreateIssue_And_GetIssue(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, err := db.CreateIssue(d, "Bug #1", "desc", "alice", "bob", "High", "proj", "comp")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if issue == nil {
		t.Fatal("expected non-nil issue")
	}

	if issue.ID <= 0 {
		t.Errorf("expected positive ID, got %d", issue.ID)
	}

	if issue.Title != "Bug #1" {
		t.Errorf("title: got %q, want %q", issue.Title, "Bug #1")
	}

	if issue.Status != "Open" {
		t.Errorf("status: got %q, want %q", issue.Status, "Open")
	}

	if issue.Priority != "High" {
		t.Errorf("priority: got %q, want %q", issue.Priority, "High")
	}

	// New issues default to teams=["any"].
	if !db.ContainsTeam(issue.Teams, "any") {
		t.Error("new issue should default to teams=['any']")
	}

	// Verify GetIssue agrees.
	got, err := db.GetIssue(d, issue.ID)
	if err != nil || got == nil {
		t.Fatalf("GetIssue: err=%v, got=%v", err, got)
	}

	if got.ID != issue.ID {
		t.Errorf("GetIssue ID mismatch: got %d, want %d", got.ID, issue.ID)
	}
}

func TestCreateIssue_DefaultPriority(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, err := db.CreateIssue(d, "Title", "", "r", "", "", "", "")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if issue.Priority != "Medium" {
		t.Errorf("default priority: got %q, want %q", issue.Priority, "Medium")
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, err := db.GetIssue(d, 999)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	if issue != nil {
		t.Error("expected nil for nonexistent issue")
	}
}

func TestUpdateIssue(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	orig, _ := db.CreateIssue(d, "Original", "", "r", "", "Low", "p", "c")

	updated, err := db.UpdateIssue(d, orig.ID, "Updated Title", "new desc", "High", "Open", "assignee", "p", "c", nil, nil)
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	if updated.Title != "Updated Title" {
		t.Errorf("title: got %q, want %q", updated.Title, "Updated Title")
	}

	if updated.Priority != "High" {
		t.Errorf("priority: got %q, want %q", updated.Priority, "High")
	}

	if updated.Assignee != "assignee" {
		t.Errorf("assignee: got %q, want %q", updated.Assignee, "assignee")
	}
}

func TestUpdateIssue_Teams(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "", "Medium", "p", "c")

	if !db.ContainsTeam(issue.Teams, "any") {
		t.Error("new issue should default to teams=['any']")
	}

	// Update teams to ["platform", "database"].
	updated, err := db.UpdateIssue(d, issue.ID, "T", "", "Medium", "Open", "", "p", "c", nil, []string{"platform", "database"})
	if err != nil {
		t.Fatalf("UpdateIssue with teams: %v", err)
	}

	if !db.ContainsTeam(updated.Teams, "platform") {
		t.Error("expected teams to contain platform")
	}

	if db.ContainsTeam(updated.Teams, "any") {
		t.Error("expected 'any' to be replaced after explicit team update")
	}

	// Update with nil teams — should leave teams unchanged.
	resaved, _ := db.UpdateIssue(d, issue.ID, "T2", "", "Medium", "Open", "", "p", "c", nil, nil)
	if !db.ContainsTeam(resaved.Teams, "platform") {
		t.Error("teams should not change when nil is passed")
	}
}

func TestUpdateIssue_ResolvedAt(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "a", "Medium", "p", "c")

	if issue.ResolvedAt != "" {
		t.Errorf("new issue should have empty resolved_at, got %q", issue.ResolvedAt)
	}

	// Resolve: resolved_at should be set.
	resolved, err := db.UpdateIssue(d, issue.ID, "T", "", "Medium", "Resolved", "a", "p", "c", nil, nil)
	if err != nil {
		t.Fatalf("UpdateIssue to Resolved: %v", err)
	}

	if resolved.ResolvedAt == "" {
		t.Error("resolved_at should be set when transitioning to Resolved")
	}

	first := resolved.ResolvedAt

	// Re-save as Resolved: resolved_at should NOT be overwritten.
	resaved, _ := db.UpdateIssue(d, issue.ID, "T changed", "", "Medium", "Resolved", "a", "p", "c", nil, nil)
	if resaved.ResolvedAt != first {
		t.Errorf("resolved_at should not change on re-save: got %q, want %q", resaved.ResolvedAt, first)
	}

	// Re-open: resolved_at should be cleared.
	reopened, _ := db.UpdateIssue(d, issue.ID, "T changed", "", "Medium", "Open", "a", "p", "c", nil, nil)
	if reopened.ResolvedAt != "" {
		t.Errorf("resolved_at should be cleared when reopened, got %q", reopened.ResolvedAt)
	}
}

func TestDeleteIssue(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "", "Medium", "p", "c")
	db.CreateComment(d, issue.ID, "r", "a comment")

	if err := db.DeleteIssue(d, issue.ID); err != nil {
		t.Fatalf("DeleteIssue: %v", err)
	}

	got, err := db.GetIssue(d, issue.ID)
	if err != nil || got != nil {
		t.Errorf("GetIssue after delete: err=%v, got=%v", err, got)
	}

	// Comments should be cascade-deleted.
	comments, err := db.ListComments(d, issue.ID)
	if err != nil {
		t.Fatalf("ListComments after delete: %v", err)
	}

	if len(comments) != 0 {
		t.Errorf("expected comments deleted, got %d", len(comments))
	}
}

// ---------------------------------------------------------------------------
// ListIssues filtering and sorting
// ---------------------------------------------------------------------------

func TestListIssues_Empty(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issues, err := db.ListIssues(d, "", "", "", "", "", "", 0, 0, nil)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	if issues == nil {
		t.Error("expected non-nil slice")
	}
}

func TestListIssues_FilterByStatus(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	i1, _ := db.CreateIssue(d, "A", "", "r", "a", "Medium", "p", "c")
	i2, _ := db.CreateIssue(d, "B", "", "r", "a", "Medium", "p", "c")
	db.UpdateIssue(d, i2.ID, "B", "", "Medium", "Resolved", "a", "p", "c", nil, nil)

	open, _ := db.ListIssues(d, "open", "", "", "", "", "", 0, 0, nil)
	if len(open) != 1 || open[0].ID != i1.ID {
		t.Errorf("open filter: got %d issues, want 1 with id %d", len(open), i1.ID)
	}

	resolved, _ := db.ListIssues(d, "resolved", "", "", "", "", "", 0, 0, nil)
	if len(resolved) != 1 || resolved[0].ID != i2.ID {
		t.Errorf("resolved filter: got %d issues, want 1 with id %d", len(resolved), i2.ID)
	}
}

func TestListIssues_FilterByTeam(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	i1, _ := db.CreateIssue(d, "Platform issue", "", "r", "", "Medium", "p", "c")
	i2, _ := db.CreateIssue(d, "DB issue", "", "r", "", "Medium", "p", "c")

	// Assign different teams to the two issues.
	db.UpdateIssue(d, i1.ID, "Platform issue", "", "Medium", "Open", "", "p", "c", nil, []string{"platform"})
	db.UpdateIssue(d, i2.ID, "DB issue", "", "Medium", "Open", "", "p", "c", nil, []string{"database"})

	// User with "platform" team sees only i1.
	visible, _ := db.ListIssues(d, "", "", "", "", "", "", 0, 0, []string{"platform"})
	if len(visible) != 1 || visible[0].ID != i1.ID {
		t.Errorf("platform user: expected 1 issue (id %d), got %d", i1.ID, len(visible))
	}

	// User with "admin" team sees all.
	all, _ := db.ListIssues(d, "", "", "", "", "", "", 0, 0, []string{"admin"})
	if len(all) != 2 {
		t.Errorf("admin user: expected 2 issues, got %d", len(all))
	}

	// User with "any" team sees all.
	anyAll, _ := db.ListIssues(d, "", "", "", "", "", "", 0, 0, []string{"any"})
	if len(anyAll) != 2 {
		t.Errorf("any user: expected 2 issues, got %d", len(anyAll))
	}
}

func TestListIssues_FilterByPriority(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "H", "", "r", "", "High", "p", "c")
	db.CreateIssue(d, "L", "", "r", "", "Low", "p", "c")

	high, _ := db.ListIssues(d, "", "High", "", "", "", "", 0, 0, nil)
	if len(high) != 1 || high[0].Title != "H" {
		t.Errorf("priority filter High: got %d issues", len(high))
	}
}

func TestListIssues_FilterByProject(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "A", "", "r", "", "Medium", "proj-a", "c")
	db.CreateIssue(d, "B", "", "r", "", "Medium", "proj-b", "c")

	a, _ := db.ListIssues(d, "", "", "", "proj-a", "", "", 0, 0, nil)
	if len(a) != 1 {
		t.Errorf("project filter: got %d, want 1", len(a))
	}
}

func TestListIssues_Search(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "crash in login", "", "r", "", "Medium", "p", "c")
	db.CreateIssue(d, "UI glitch", "", "r", "", "Medium", "p", "c")

	results, _ := db.ListIssues(d, "", "", "login", "", "", "", 0, 0, nil)
	if len(results) != 1 {
		t.Errorf("search: got %d results, want 1", len(results))
	}
}

func TestListIssues_Pagination(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	for i := 0; i < 5; i++ {
		db.CreateIssue(d, fmt.Sprintf("Issue %d", i), "", "r", "", "Medium", "p", "c")
	}

	page, _ := db.ListIssues(d, "", "", "", "", "id", "asc", 2, 0, nil)
	if len(page) != 2 {
		t.Errorf("first page: got %d, want 2", len(page))
	}

	page2, _ := db.ListIssues(d, "", "", "", "", "id", "asc", 2, 2, nil)
	if len(page2) != 2 {
		t.Errorf("second page: got %d, want 2", len(page2))
	}

	// Verify no overlap.
	if page[0].ID == page2[0].ID {
		t.Error("pages should not overlap")
	}
}

func TestCountIssues(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "A", "", "r", "", "High", "p", "c")
	db.CreateIssue(d, "B", "", "r", "", "Low", "p", "c")

	n, err := db.CountIssues(d, "", "", "", "", nil)
	if err != nil || n != 2 {
		t.Errorf("CountIssues: err=%v, n=%d (want 2)", err, n)
	}

	n, err = db.CountIssues(d, "", "High", "", "", nil)
	if err != nil || n != 1 {
		t.Errorf("CountIssues High: err=%v, n=%d (want 1)", err, n)
	}
}

func TestListChanges(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	i1, _ := db.CreateIssue(d, "A", "", "r", "", "Medium", "p", "c")

	// Empty since returns nothing.
	results, err := db.ListChanges(d, "")
	if err != nil {
		t.Fatalf("ListChanges empty since: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results for empty since, got %d", len(results))
	}

	// since before all records returns the issue.
	results, err = db.ListChanges(d, "2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}

	if len(results) != 1 || results[0].ID != i1.ID {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------------

func TestCreateComment_And_ListComments(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "", "Medium", "p", "c")

	c, err := db.CreateComment(d, issue.ID, "alice", "first comment")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	if c.Body != "first comment" {
		t.Errorf("body: got %q", c.Body)
	}

	db.CreateComment(d, issue.ID, "bob", "second comment")

	comments, err := db.ListComments(d, issue.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}

	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}

	// Verify chronological order.
	if comments[0].Author != "alice" {
		t.Errorf("first comment author: got %q, want alice", comments[0].Author)
	}
}

func TestListComments_Empty(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "", "Medium", "p", "c")

	comments, err := db.ListComments(d, issue.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}

	if comments == nil {
		t.Error("expected non-nil empty slice")
	}
}

func TestDeleteComment(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "", "Medium", "p", "c")
	c, _ := db.CreateComment(d, issue.ID, "r", "body")

	if err := db.DeleteComment(d, c.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}

	comments, _ := db.ListComments(d, issue.ID)
	if len(comments) != 0 {
		t.Errorf("expected 0 comments after delete, got %d", len(comments))
	}
}

func TestIssue_CommentCount(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "", "Medium", "p", "c")

	got, _ := db.GetIssue(d, issue.ID)
	if got.CommentCount != 0 {
		t.Errorf("initial comment_count: got %d, want 0", got.CommentCount)
	}

	db.CreateComment(d, issue.ID, "r", "c1")
	db.CreateComment(d, issue.ID, "r", "c2")

	got, _ = db.GetIssue(d, issue.ID)
	if got.CommentCount != 2 {
		t.Errorf("comment_count after 2 comments: got %d, want 2", got.CommentCount)
	}
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

func TestCreateProject_And_ListProjects(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.CreateProject(d, "backend", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	projects, err := db.ListProjects(d)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	if len(projects) != 1 || projects[0].Name != "backend" {
		t.Errorf("unexpected projects: %v", projects)
	}

	// nil teams should default to "any".
	if !db.ContainsTeam(projects[0].Teams, "any") {
		t.Error("project with nil teams should default to ['any']")
	}
}

func TestCreateProject_WithTeams(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "platform-proj", []string{"platform", "admin"})

	projects, _ := db.ListProjects(d)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}

	if !db.ContainsTeam(projects[0].Teams, "platform") {
		t.Error("expected project to have platform team")
	}
}

func TestCreateProject_Idempotent(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "myproj", nil)

	if err := db.CreateProject(d, "myproj", nil); err != nil {
		t.Errorf("second CreateProject should be idempotent, got %v", err)
	}

	projects, _ := db.ListProjects(d)
	if len(projects) != 1 {
		t.Errorf("expected 1 project after duplicate create, got %d", len(projects))
	}
}

func TestSetProjectTeams(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "myproj", nil)

	if err := db.SetProjectTeams(d, "myproj", []string{"platform"}); err != nil {
		t.Fatalf("SetProjectTeams: %v", err)
	}

	projects, _ := db.ListProjects(d)
	if !db.ContainsTeam(projects[0].Teams, "platform") {
		t.Error("expected project to have platform team after SetProjectTeams")
	}

	if db.ContainsTeam(projects[0].Teams, "any") {
		t.Error("expected 'any' to be replaced after explicit SetProjectTeams")
	}
}

func TestAddComponent(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "myproj", nil)

	if err := db.AddComponent(d, "myproj", "api"); err != nil {
		t.Fatalf("AddComponent: %v", err)
	}

	comps, err := db.GetComponents(d, "myproj")
	if err != nil {
		t.Fatalf("GetComponents: %v", err)
	}

	if len(comps) != 1 || comps[0] != "api" {
		t.Errorf("unexpected components: %v", comps)
	}
}

func TestAddComponent_NoProject(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	err := db.AddComponent(d, "nonexistent", "comp")
	if err == nil {
		t.Error("expected error adding component to nonexistent project")
	}
}

func TestAddComponent_Idempotent(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "p", nil)
	db.AddComponent(d, "p", "c")

	if err := db.AddComponent(d, "p", "c"); err != nil {
		t.Errorf("duplicate AddComponent should be idempotent, got %v", err)
	}

	comps, _ := db.GetComponents(d, "p")
	if len(comps) != 1 {
		t.Errorf("expected 1 component after duplicate add, got %d", len(comps))
	}
}

func TestListProjects_WithComponents(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "alpha", nil)
	db.AddComponent(d, "alpha", "ui")
	db.AddComponent(d, "alpha", "api")
	db.CreateProject(d, "beta", nil)

	projects, err := db.ListProjects(d)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	// alphabetical order
	if projects[0].Name != "alpha" {
		t.Errorf("first project: got %q, want %q", projects[0].Name, "alpha")
	}

	if len(projects[0].Components) != 2 {
		t.Errorf("alpha components: got %d, want 2", len(projects[0].Components))
	}

	if len(projects[1].Components) != 0 {
		t.Errorf("beta components: got %d, want 0", len(projects[1].Components))
	}
}

func TestDeleteProject(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "p", nil)
	db.AddComponent(d, "p", "c")

	if err := db.DeleteProject(d, "p"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	projects, _ := db.ListProjects(d)
	if len(projects) != 0 {
		t.Error("expected 0 projects after delete")
	}

	comps, _ := db.GetComponents(d, "p")
	if len(comps) != 0 {
		t.Error("expected components deleted with project")
	}
}

func TestDeleteProject_ReferencedByIssue(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "p", nil)
	db.CreateIssue(d, "issue", "", "r", "", "Medium", "p", "c")

	err := db.DeleteProject(d, "p")
	if err == nil {
		t.Error("expected error deleting project referenced by issue")
	}

	if !strings.Contains(err.Error(), "referenced") {
		t.Errorf("error should mention 'referenced', got: %v", err)
	}
}

func TestDeleteComponent(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "p", nil)
	db.AddComponent(d, "p", "comp1")
	db.AddComponent(d, "p", "comp2")

	if err := db.DeleteComponent(d, "p", "comp1"); err != nil {
		t.Fatalf("DeleteComponent: %v", err)
	}

	comps, _ := db.GetComponents(d, "p")
	if len(comps) != 1 || comps[0] != "comp2" {
		t.Errorf("unexpected components after delete: %v", comps)
	}
}

func TestDeleteComponent_ReferencedByIssue(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "p", nil)
	db.AddComponent(d, "p", "comp")
	db.CreateIssue(d, "issue", "", "r", "", "Medium", "p", "comp")

	err := db.DeleteComponent(d, "p", "comp")
	if err == nil {
		t.Error("expected error deleting component referenced by issue")
	}
}

func TestGetComponents_Empty(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "empty", nil)

	comps, err := db.GetComponents(d, "empty")
	if err != nil {
		t.Fatalf("GetComponents: %v", err)
	}

	if comps == nil {
		t.Error("expected non-nil empty slice")
	}
}

func TestListProjects_Empty(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	projects, err := db.ListProjects(d)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	if projects == nil {
		t.Error("expected non-nil empty slice")
	}
}

// ---------------------------------------------------------------------------
// Helper: ProjectMatchesUserTeams
// ---------------------------------------------------------------------------

func TestProjectMatchesUserTeams(t *testing.T) {
	tests := []struct {
		projectTeams []string
		userTeams    []string
		want         bool
	}{
		{[]string{"any"}, []string{"platform"}, true},   // issue has any
		{[]string{"platform"}, []string{"any"}, true},   // user has any
		{[]string{"platform"}, []string{"admin"}, true}, // user has admin
		{[]string{"platform"}, []string{"platform"}, true},
		{[]string{"database"}, []string{"platform"}, false},
		{[]string{"platform", "database"}, []string{"platform"}, true},
	}
	for _, tc := range tests {
		got := db.ProjectMatchesUserTeams(tc.projectTeams, tc.userTeams)
		if got != tc.want {
			t.Errorf("ProjectMatchesUserTeams(%v, %v) = %v, want %v",
				tc.projectTeams, tc.userTeams, got, tc.want)
		}
	}
}
