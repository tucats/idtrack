package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/tucats/idtrack/db"
)

// ---------------------------------------------------------------------------
// extractTitle
// ---------------------------------------------------------------------------

func TestExtractTitle_H1(t *testing.T) {
	content := "# BUG-01 — for v := range ch over channel yields indices\n\nSome body text.\n"

	got := extractTitle(content)
	want := "BUG-01 — for v := range ch over channel yields indices"

	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractTitle_FirstSentenceFallback(t *testing.T) {
	content := "The LANGUAGE.md documents passing a function as an any parameter. It fails in practice.\n"

	got := extractTitle(content)
	want := "The LANGUAGE.md documents passing a function as an any parameter."

	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractTitle_SkipsCodeFenceWhenNoHeading(t *testing.T) {
	content := "```go\nfunc main() { fmt.Println(1.5) }\n```\n\nCalling a function stored in any fails. It should work.\n"

	got := extractTitle(content)
	want := "Calling a function stored in any fails."

	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// splitSections
// ---------------------------------------------------------------------------

func TestSplitSections_BoldLabels(t *testing.T) {
	content := "# Title\n\nDescription text here.\n\n**Reproducer:**\n\nSome code.\n\n**Resolution:**\n\nFixed it.\n"

	desc, comments := splitSections(content)

	if desc != "Description text here." {
		t.Errorf("description = %q", desc)
	}

	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}

	if comments[0].Label != "Reproducer" {
		t.Errorf("comments[0].Label = %q", comments[0].Label)
	}

	if comments[1].Label != "Resolution" {
		t.Errorf("comments[1].Label = %q", comments[1].Label)
	}
}

func TestSplitSections_MarkdownHeaders(t *testing.T) {
	content := "# Title\n\nIntro text.\n\n## Original behavior\n\nBroken.\n\n## Fix\n\nFixed.\n"

	desc, comments := splitSections(content)

	if desc != "Intro text." {
		t.Errorf("description = %q", desc)
	}

	if len(comments) != 2 || comments[0].Label != "Original behavior" || comments[1].Label != "Fix" {
		t.Fatalf("unexpected comments: %+v", comments)
	}
}

func TestSplitSections_NoBoundaries(t *testing.T) {
	content := "# Title\n\nJust one paragraph, no sections at all.\n"

	desc, comments := splitSections(content)

	if desc != "Just one paragraph, no sections at all." {
		t.Errorf("description = %q", desc)
	}

	if len(comments) != 0 {
		t.Errorf("got %d comments, want 0", len(comments))
	}
}

func TestSplitSections_IgnoresHeadersInsideCodeFence(t *testing.T) {
	content := "# Title\n\nIntro.\n\n```text\n## not a real section\n```\n\n**Resolution:**\n\nDone.\n"

	desc, comments := splitSections(content)

	if desc != "Intro.\n\n```text\n## not a real section\n```" {
		t.Errorf("description = %q", desc)
	}

	if len(comments) != 1 || comments[0].Label != "Resolution" {
		t.Fatalf("unexpected comments: %+v", comments)
	}
}

// ---------------------------------------------------------------------------
// detectStatus / detectPriority
// ---------------------------------------------------------------------------

func TestDetectStatus_ResolutionLabel(t *testing.T) {
	_, comments := splitSections("# T\n\nBody.\n\n**Resolution (June 2026):**\n\nFixed in bytecode/range.go.\n")

	status, matched := detectStatus(comments, "")
	if !matched || status != statusResolved {
		t.Errorf("got (%q, %v), want (Resolved, true)", status, matched)
	}
}

func TestDetectStatus_StatusLabel(t *testing.T) {
	_, comments := splitSections("# T\n\nBody.\n\n**Status: RESOLVED**\n\nmore text\n")

	status, matched := detectStatus(comments, "")
	if !matched || status != statusResolved {
		t.Errorf("got (%q, %v), want (Resolved, true)", status, matched)
	}
}

func TestDetectStatus_NoSignal(t *testing.T) {
	_, comments := splitSections("# T\n\nBody.\n\n**Reproducer:**\n\ncode here\n")

	_, matched := detectStatus(comments, "Body. Reproducer: code here")
	if matched {
		t.Error("expected no status signal to be found")
	}
}

func TestDetectStatus_DoesNotMatchClosedOverVariable(t *testing.T) {
	// Regression: "closed over" is common Go closure phrasing and must not
	// be mistaken for an issue-resolution signal.
	content := "The function closed over the loop variable, capturing the wrong value on each iteration."

	_, comments := splitSections("# T\n\n" + content + "\n")

	_, matched := detectStatus(comments, content)
	if matched {
		t.Error("\"closed over\" should not be treated as a resolved-status signal")
	}
}

func TestDetectPriority_SeverityLabel(t *testing.T) {
	_, comments := splitSections("# T\n\nBody.\n\n**Severity:** HIGH\n\nmore\n")

	priority, matched := detectPriority(comments)
	if !matched || priority != "High" {
		t.Errorf("got (%q, %v), want (High, true)", priority, matched)
	}
}

func TestDetectPriority_RiskLabel(t *testing.T) {
	_, comments := splitSections("# T\n\nBody.\n\n**Risk:** Medium — some consequence\n\nmore\n")

	priority, matched := detectPriority(comments)
	if !matched || priority != "Medium" {
		t.Errorf("got (%q, %v), want (Medium, true)", priority, matched)
	}
}

func TestDetectPriority_NoSignal(t *testing.T) {
	_, comments := splitSections("# T\n\nBody.\n\n**Reproducer:**\n\ncode\n")

	_, matched := detectPriority(comments)
	if matched {
		t.Error("expected no priority signal to be found")
	}
}

// ---------------------------------------------------------------------------
// inferProjectComponent
// ---------------------------------------------------------------------------

func testProjects() []db.Project {
	return []db.Project{
		{Name: "ego", Components: []string{"bytecode", "http"}},
		{Name: "other", Components: []string{"widgets"}},
	}
}

func TestInferProjectComponent_StrongFilenameAndBodyMatch(t *testing.T) {
	content := "Bytecode execution of the range instruction is wrong.\n\nThe bytecode interpreter mishandles bytecode ranges."
	_, comments := splitSections("# T\n\n" + content + "\n")

	result := inferProjectComponent("BYTECODE-RANGE-1.md", "range instruction bug", content, comments, testProjects(), "other", "widgets")

	if result.Project != "ego" || result.ProjectSource != "inferred" {
		t.Errorf("project = %q (%s), want ego (inferred)", result.Project, result.ProjectSource)
	}

	if result.Component != "bytecode" || result.ComponentSource != "inferred" {
		t.Errorf("component = %q (%s), want bytecode (inferred)", result.Component, result.ComponentSource)
	}
}

func TestInferProjectComponent_FallsBackToDefaults(t *testing.T) {
	content := "A generic description with no topic-specific keywords at all."
	_, comments := splitSections("# T\n\n" + content + "\n")

	result := inferProjectComponent("ISSUE-1.md", "generic issue", content, comments, testProjects(), "other", "widgets")

	if result.Project != "other" || result.ProjectSource != "default" {
		t.Errorf("project = %q (%s), want other (default)", result.Project, result.ProjectSource)
	}

	if result.Component != "widgets" || result.ComponentSource != "default" {
		t.Errorf("component = %q (%s), want widgets (default)", result.Component, result.ComponentSource)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: parseIngestFiles + runIngestTx, including atomicity
// ---------------------------------------------------------------------------

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	return path
}

func setupIngestDB(t *testing.T) *sql.DB {
	t.Helper()

	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	t.Cleanup(func() { d.Close() })

	if err := db.AddUser(d, "alice", "Alice", "pw", []string{"any"}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	if err := db.CreateProject(d, "ego", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := db.AddComponent(d, "ego", "bytecode"); err != nil {
		t.Fatalf("AddComponent: %v", err)
	}

	return d
}

func TestIngestEndToEnd_Success(t *testing.T) {
	d := setupIngestDB(t)
	dir := t.TempDir()

	projects, err := db.ListProjects(d)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	f1 := writeTempFile(t, dir, "BUG-01.md", "# Channel range yields indices\n\nThe range loop is broken.\n\n**Severity:** HIGH\n\n**Resolution:**\n\nFixed in bytecode/range.go.\n")
	f2 := writeTempFile(t, dir, "BUG-02.md", "# Another bug\n\nSomething else is broken.\n")

	plans, fails := parseIngestFiles([]string{f1, f2}, projects, "ego", "bytecode", statusOpen, "Medium")
	if len(fails) != 0 {
		t.Fatalf("unexpected parse failures: %v", fails)
	}

	n, err := runIngestTx(d, plans, "alice", "alice")
	if err != nil {
		t.Fatalf("runIngestTx: %v", err)
	}

	if n != 2 {
		t.Errorf("got %d issues created, want 2", n)
	}

	issues, err := db.ListIssues(d, "", "", "", "", "id", "asc", 0, 0, nil)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("got %d issues in db, want 2", len(issues))
	}

	if issues[0].Status != statusResolved {
		t.Errorf("issue 1 status = %q, want Resolved", issues[0].Status)
	}

	if issues[0].ResolvedAt == "" {
		t.Error("resolved issue should have resolved_at set")
	}

	if issues[0].Priority != "High" {
		t.Errorf("issue 1 priority = %q, want High", issues[0].Priority)
	}

	comments, err := db.ListComments(d, issues[0].ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}

	// Both the "**Severity:**" and "**Resolution:**" boundaries become their
	// own comment sections — severity driving priority inference doesn't
	// exclude it from also being posted as a comment.
	if len(comments) != 2 {
		t.Errorf("got %d comments, want 2", len(comments))
	}

	if issues[1].Status != statusOpen {
		t.Errorf("issue 2 status = %q, want Open", issues[1].Status)
	}
}

func TestIngestEndToEnd_MissingFileAbortsBeforeAnyWrite(t *testing.T) {
	d := setupIngestDB(t)
	dir := t.TempDir()

	projects, err := db.ListProjects(d)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	f1 := writeTempFile(t, dir, "BUG-01.md", "# Good file\n\nThis one parses fine.\n")
	missing := filepath.Join(dir, "does-not-exist.md")

	plans, fails := parseIngestFiles([]string{f1, missing}, projects, "ego", "bytecode", statusOpen, "Medium")

	if len(fails) != 1 {
		t.Fatalf("got %d failures, want 1", len(fails))
	}

	// A real CLI run would abort here without ever calling runIngestTx.
	// Verify that no plans leaked through for the failed file, and that the
	// database is untouched.
	if len(plans) != 1 {
		t.Errorf("got %d plans, want 1 (failures must not stop other files from being reported, but must not be silently included either)", len(plans))
	}

	issues, err := db.ListIssues(d, "", "", "", "", "id", "asc", 0, 0, nil)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	if len(issues) != 0 {
		t.Errorf("got %d issues, want 0 — no file should have been ingested", len(issues))
	}
}

func TestRunIngestTx_RollsBackOnMidBatchFailure(t *testing.T) {
	d := setupIngestDB(t)

	// Two plans; the second one carries a comment. After the first plan's
	// issue is created inside the transaction, drop the comments table out
	// from under it so the second plan's comment insert hits a genuine SQL
	// error. This must roll back the whole transaction, including the first
	// plan's already-inserted issue.
	plans := []ingestPlan{
		{Path: "ok.md", Title: "Good issue", Format: "text", Description: "fine", Status: statusOpen, Priority: "Medium", Project: "ego", Component: "bytecode"},
		{
			Path: "broken.md", Title: "Second issue", Format: "text", Description: "fine too",
			Status: statusOpen, Priority: "Medium", Project: "ego", Component: "bytecode",
			Comments: []section{{Label: "Note", Body: "a comment that will fail to insert"}},
		},
	}

	if _, err := d.Exec(`DROP TABLE comments`); err != nil {
		t.Fatalf("dropping comments table: %v", err)
	}

	_, err := runIngestTx(d, plans, "alice", "alice")
	if err == nil {
		t.Fatal("expected an error once the comments table is gone")
	}

	// Recreate the table (ListIssues' comment_count subquery needs it to
	// exist) purely so the post-condition check below can run.
	if _, err := d.Exec(`CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, issue_id INTEGER NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("recreating comments table: %v", err)
	}

	issues, err := db.ListIssues(d, "", "", "", "", "id", "asc", 0, 0, nil)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}

	if len(issues) != 0 {
		t.Errorf("got %d issues after a rolled-back transaction, want 0 (including the first plan's issue)", len(issues))
	}
}
