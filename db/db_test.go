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
	if err := db.AddUser(d, "u", "U", "pass", false); err != nil {
		t.Fatalf("AddUser after reopen: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Password helpers
// ---------------------------------------------------------------------------

func TestIsLegacyHash(t *testing.T) {
	legacy := fmt.Sprintf("%x", sha256.Sum256([]byte("password")))

	if !db.IsLegacyHash(legacy) {
		t.Error("expected 64-char SHA-256 hex to be recognised as legacy")
	}

	if db.IsLegacyHash("$2a$10$somebcrypthash") {
		t.Error("bcrypt hash should not be recognised as legacy")
	}

	if db.IsLegacyHash("short") {
		t.Error("short string should not be recognised as legacy")
	}
}

func TestVerifyPassword_Bcrypt(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.AddUser(d, "alice", "Alice", "secret", false); err != nil {
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

	if err := db.AddUser(d, "bob", "Bob", "pass", false); err != nil {
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

	if err := db.AddUser(d, "carol", "Carol Smith", "pw", true); err != nil {
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
		t.Error("expected is_admin=true")
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

	db.AddUser(d, "dave", "Dave", "pass1", false)
	db.AddUser(d, "dave", "Dave Updated", "pass2", true)

	u, _ := db.FindUser(d, "dave")
	if u.DisplayName != "Dave Updated" {
		t.Errorf("display_name after upsert: got %q, want %q", u.DisplayName, "Dave Updated")
	}

	if !u.IsAdmin {
		t.Error("expected is_admin=true after upsert")
	}
}

func TestDeleteUser(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "eve", "Eve", "pw", false)

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

	db.AddUser(d, "frank", "Frank", "pw", false)

	if err := db.UpdateUser(d, "frank", "Franklin", "", nil); err != nil {
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

	db.AddUser(d, "grace", "Grace", "old", false)
	db.UpdateUser(d, "grace", "", "new", nil)

	u, _ := db.FindUser(d, "grace")
	if !db.VerifyPassword(u.PasswordHash, "new") {
		t.Error("password should verify after update")
	}
}

func TestUpdateUser_AdminFlag(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "hank", "Hank", "pw", false)

	tr := true
	db.UpdateUser(d, "hank", "", "", &tr)

	u, _ := db.FindUser(d, "hank")
	if !u.IsAdmin {
		t.Error("expected is_admin=true after update")
	}
}

func TestUpdateUser_NilAdminPreserves(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "ivy", "Ivy", "pw", true)
	db.UpdateUser(d, "ivy", "Ivy Updated", "", nil)

	u, _ := db.FindUser(d, "ivy")
	if !u.IsAdmin {
		t.Error("is_admin should not be cleared when isAdmin param is nil")
	}
}

func TestUpdateUser_NotFound(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	err := db.UpdateUser(d, "nobody", "Name", "", nil)
	if err == nil {
		t.Error("expected error updating nonexistent user")
	}
}

func TestUpdateUser_NoOp(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "jay", "Jay", "pw", false)

	// All empty — should be a no-op (no error).
	if err := db.UpdateUser(d, "jay", "", "", nil); err != nil {
		t.Fatalf("UpdateUser no-op: %v", err)
	}
}

func TestListUsers(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "b", "B", "pw", false)
	db.AddUser(d, "a", "A", "pw", false)
	db.AddUser(d, "c", "C", "pw", false)

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

	db.AddUser(d, "x", "X", "pw", false)

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

	db.AddUser(d, "u1", "U1", "pw", false)
	db.AddUser(d, "u2", "U2", "pw", true)
	db.AddUser(d, "u3", "U3", "pw", true)

	n, err = db.CountAdmins(d)
	if err != nil || n != 2 {
		t.Errorf("CountAdmins: err=%v, n=%d (want 2)", err, n)
	}
}

func TestRecordLogin(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "kim", "Kim", "pw", false)

	if err := db.RecordLogin(d, "kim"); err != nil {
		t.Fatalf("RecordLogin: %v", err)
	}

	u, _ := db.FindUser(d, "kim")
	if u.LastLoginAt == "" {
		t.Error("last_login_at should be set after RecordLogin")
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

	updated, err := db.UpdateIssue(d, orig.ID, "Updated Title", "new desc", "High", "Open", "assignee", "p", "c")
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

func TestUpdateIssue_ResolvedAt(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	issue, _ := db.CreateIssue(d, "T", "", "r", "a", "Medium", "p", "c")

	if issue.ResolvedAt != "" {
		t.Errorf("new issue should have empty resolved_at, got %q", issue.ResolvedAt)
	}

	// Resolve: resolved_at should be set.
	resolved, err := db.UpdateIssue(d, issue.ID, "T", "", "Medium", "Resolved", "a", "p", "c")
	if err != nil {
		t.Fatalf("UpdateIssue to Resolved: %v", err)
	}

	if resolved.ResolvedAt == "" {
		t.Error("resolved_at should be set when transitioning to Resolved")
	}

	first := resolved.ResolvedAt

	// Re-save as Resolved: resolved_at should NOT be overwritten.
	resaved, _ := db.UpdateIssue(d, issue.ID, "T changed", "", "Medium", "Resolved", "a", "p", "c")
	if resaved.ResolvedAt != first {
		t.Errorf("resolved_at should not change on re-save: got %q, want %q", resaved.ResolvedAt, first)
	}

	// Re-open: resolved_at should be cleared.
	reopened, _ := db.UpdateIssue(d, issue.ID, "T changed", "", "Medium", "Open", "a", "p", "c")
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

	issues, err := db.ListIssues(d, "", "", "", "", "", "", 0, 0)
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
	db.UpdateIssue(d, i2.ID, "B", "", "Medium", "Resolved", "a", "p", "c")

	open, _ := db.ListIssues(d, "open", "", "", "", "", "", 0, 0)
	if len(open) != 1 || open[0].ID != i1.ID {
		t.Errorf("open filter: got %d issues, want 1 with id %d", len(open), i1.ID)
	}

	resolved, _ := db.ListIssues(d, "resolved", "", "", "", "", "", 0, 0)
	if len(resolved) != 1 || resolved[0].ID != i2.ID {
		t.Errorf("resolved filter: got %d issues, want 1 with id %d", len(resolved), i2.ID)
	}
}

func TestListIssues_FilterByPriority(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "H", "", "r", "", "High", "p", "c")
	db.CreateIssue(d, "L", "", "r", "", "Low", "p", "c")

	high, _ := db.ListIssues(d, "", "High", "", "", "", "", 0, 0)
	if len(high) != 1 || high[0].Title != "H" {
		t.Errorf("priority filter High: got %d issues", len(high))
	}
}

func TestListIssues_FilterByProject(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "A", "", "r", "", "Medium", "proj-a", "c")
	db.CreateIssue(d, "B", "", "r", "", "Medium", "proj-b", "c")

	a, _ := db.ListIssues(d, "", "", "", "proj-a", "", "", 0, 0)
	if len(a) != 1 {
		t.Errorf("project filter: got %d, want 1", len(a))
	}
}

func TestListIssues_Search(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateIssue(d, "crash in login", "", "r", "", "Medium", "p", "c")
	db.CreateIssue(d, "UI glitch", "", "r", "", "Medium", "p", "c")

	results, _ := db.ListIssues(d, "", "", "login", "", "", "", 0, 0)
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

	page, _ := db.ListIssues(d, "", "", "", "", "id", "asc", 2, 0)
	if len(page) != 2 {
		t.Errorf("first page: got %d, want 2", len(page))
	}

	page2, _ := db.ListIssues(d, "", "", "", "", "id", "asc", 2, 2)
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

	n, err := db.CountIssues(d, "", "", "", "")
	if err != nil || n != 2 {
		t.Errorf("CountIssues: err=%v, n=%d (want 2)", err, n)
	}

	n, err = db.CountIssues(d, "", "High", "", "")
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

	if err := db.CreateProject(d, "backend"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	projects, err := db.ListProjects(d)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	if len(projects) != 1 || projects[0].Name != "backend" {
		t.Errorf("unexpected projects: %v", projects)
	}
}

func TestCreateProject_Idempotent(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "myproj")

	if err := db.CreateProject(d, "myproj"); err != nil {
		t.Errorf("second CreateProject should be idempotent, got %v", err)
	}

	projects, _ := db.ListProjects(d)
	if len(projects) != 1 {
		t.Errorf("expected 1 project after duplicate create, got %d", len(projects))
	}
}

func TestAddComponent(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.CreateProject(d, "myproj")

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

	db.CreateProject(d, "p")
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

	db.CreateProject(d, "alpha")
	db.AddComponent(d, "alpha", "ui")
	db.AddComponent(d, "alpha", "api")
	db.CreateProject(d, "beta")

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

	db.CreateProject(d, "p")
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

	db.CreateProject(d, "p")
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

	db.CreateProject(d, "p")
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

	db.CreateProject(d, "p")
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

	db.CreateProject(d, "empty")

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
