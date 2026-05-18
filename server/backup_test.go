package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// parseBackupTime
// ---------------------------------------------------------------------------

func TestParseBackupTime_Valid(t *testing.T) {
	name := "idtrack-20260517T143000.db"

	got, err := parseBackupTime(name)
	if err != nil {
		t.Fatalf("parseBackupTime(%q): %v", name, err)
	}

	want := time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseBackupTime_Invalid(t *testing.T) {
	_, err := parseBackupTime("not-a-backup.db")
	if err == nil {
		t.Error("expected error for non-backup filename")
	}
}

// ---------------------------------------------------------------------------
// listBackups
// ---------------------------------------------------------------------------

func TestListBackups_Empty(t *testing.T) {
	dir := t.TempDir()

	names, err := listBackups(dir)
	if err != nil {
		t.Fatalf("listBackups: %v", err)
	}

	if len(names) != 0 {
		t.Errorf("expected 0 backups, got %d", len(names))
	}
}

func TestListBackups_Sorted(t *testing.T) {
	dir := t.TempDir()

	// Create files out of alphabetical order.
	files := []string{
		"idtrack-20260517T120000.db",
		"idtrack-20260515T080000.db",
		"idtrack-20260516T100000.db",
	}

	for _, f := range files {
		os.WriteFile(filepath.Join(dir, f), []byte("data"), 0600)
	}

	names, err := listBackups(dir)
	if err != nil {
		t.Fatalf("listBackups: %v", err)
	}

	if len(names) != 3 {
		t.Fatalf("expected 3 backups, got %d", len(names))
	}

	// Alphabetical = chronological for timestamp-based names.
	if names[0] != "idtrack-20260515T080000.db" {
		t.Errorf("first: got %q", names[0])
	}

	if names[2] != "idtrack-20260517T120000.db" {
		t.Errorf("last: got %q", names[2])
	}
}

func TestListBackups_IgnoresNonBackupFiles(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "idtrack-20260517T120000.db"), []byte("db"), 0600)
	os.WriteFile(filepath.Join(dir, "somethingelse.db"), []byte("x"), 0600)
	os.WriteFile(filepath.Join(dir, "idtrack-noext"), []byte("y"), 0600)

	names, err := listBackups(dir)
	if err != nil {
		t.Fatalf("listBackups: %v", err)
	}

	if len(names) != 1 {
		t.Errorf("expected 1 backup, got %d: %v", len(names), names)
	}
}

// ---------------------------------------------------------------------------
// newBackupPath
// ---------------------------------------------------------------------------

func TestNewBackupPath_Format(t *testing.T) {
	dir := "/tmp/backups"
	path := newBackupPath(dir)

	base := filepath.Base(path)

	if !hasPrefix(base, backupFilePrefix) {
		t.Errorf("unexpected prefix: %q", base)
	}

	if !hasSuffix(base, backupFileSuffix) {
		t.Errorf("unexpected suffix: %q", base)
	}

	// The embedded timestamp should be parseable.
	_, err := parseBackupTime(base)
	if err != nil {
		t.Errorf("parseBackupTime(%q): %v", base, err)
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

func hasSuffix(s, sfx string) bool {
	return len(s) >= len(sfx) && s[len(s)-len(sfx):] == sfx
}

// ---------------------------------------------------------------------------
// ageBackups — count-based pruning
// ---------------------------------------------------------------------------

func TestAgeBackups_CountPruning(t *testing.T) {
	dir := t.TempDir()
	s := &srv{backupCount: 2}

	// Create 5 backup files.
	for i := 0; i < 5; i++ {
		ts := time.Now().UTC().Add(time.Duration(-i) * time.Hour)
		name := fmt.Sprintf("idtrack-%s.db", ts.Format(backupTimeLayout))
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600)
	}

	s.ageBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 2 {
		t.Errorf("expected 2 backups after count pruning, got %d", len(names))
	}
}

func TestAgeBackups_AgePruning(t *testing.T) {
	dir := t.TempDir()
	s := &srv{backupAge: 24 * time.Hour}

	// Create one old backup (48h ago) and one recent one.
	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)

	oldName := fmt.Sprintf("idtrack-%s.db", old.Format(backupTimeLayout))
	recentName := fmt.Sprintf("idtrack-%s.db", recent.Format(backupTimeLayout))

	os.WriteFile(filepath.Join(dir, oldName), []byte("x"), 0600)
	os.WriteFile(filepath.Join(dir, recentName), []byte("x"), 0600)

	s.ageBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 1 {
		t.Errorf("expected 1 backup after age pruning, got %d: %v", len(names), names)
	}

	if names[0] != recentName {
		t.Errorf("expected recent backup to survive, got %q", names[0])
	}
}

func TestAgeBackups_NoLimits_NoChange(t *testing.T) {
	dir := t.TempDir()
	s := &srv{} // no limits set

	for i := 0; i < 5; i++ {
		ts := time.Now().UTC().Add(time.Duration(-i) * time.Hour)
		name := fmt.Sprintf("idtrack-%s.db", ts.Format(backupTimeLayout))
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600)
	}

	s.ageBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 5 {
		t.Errorf("expected 5 backups unchanged, got %d", len(names))
	}
}

// ---------------------------------------------------------------------------
// sizeBackups — density algorithm
// ---------------------------------------------------------------------------

// writeBackupFile creates a fake backup file with a given timestamp and size.
func writeBackupFile(t *testing.T, dir string, ts time.Time, sizeBytes int) string {
	t.Helper()

	name := fmt.Sprintf("idtrack-%s.db", ts.Format(backupTimeLayout))
	data := make([]byte, sizeBytes)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatalf("writeBackupFile: %v", err)
	}

	return name
}

func TestSizeBackups_Disabled(t *testing.T) {
	dir := t.TempDir()
	s := &srv{backupSize: 0}

	// Create files that would be deleted if limit were active.
	for i := 2; i <= 5; i++ {
		ts := time.Now().UTC().Add(time.Duration(-i) * time.Hour)
		writeBackupFile(t, dir, ts, 1024)
	}

	s.sizeBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 4 {
		t.Errorf("sizeBackups(0) should be a no-op, got %d files", len(names))
	}
}

func TestSizeBackups_WithinLimit(t *testing.T) {
	dir := t.TempDir()
	s := &srv{backupSize: 10 * 1024 * 1024} // 10 MB

	// Create two small files (< limit).
	for i := 2; i <= 3; i++ {
		ts := time.Now().UTC().Add(time.Duration(-i) * time.Hour)
		writeBackupFile(t, dir, ts, 1024)
	}

	s.sizeBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 2 {
		t.Errorf("within-limit: should keep all files, got %d", len(names))
	}
}

func TestSizeBackups_Phase1_HourlyExtras(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// Two files in the 1-hour bucket (ages 1h and 1.5h).
	ts1 := now.Add(-90 * time.Minute) // 1.5h old
	ts2 := now.Add(-70 * time.Minute) // ~1.2h old

	name1 := writeBackupFile(t, dir, ts1, 5000)
	name2 := writeBackupFile(t, dir, ts2, 5000)

	// Set limit below total (10000) to force deletion.
	s := &srv{backupSize: 6000}
	s.sizeBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 1 {
		t.Errorf("expected 1 file after phase-1 thinning, got %d: %v", len(names), names)
	}

	// Oldest (ts1) should be deleted; newest (ts2) kept.
	_ = name1 // deleted
	_ = name2 // kept

	if names[0] != filepath.Base(newBackupPathForTime(dir, ts2)) {
		// Verify by checking the surviving file's name ends with ts2's timestamp.
		ts2str := ts2.Format(backupTimeLayout)
		if !hasSuffix(names[0], ".db") || !contains(names[0], ts2str) {
			t.Errorf("expected newest file to survive, got %q", names[0])
		}
	}
}

func TestSizeBackups_Phase4_OldestDailyKeeper(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	// One file per day for 3 days ago (each is a keeper).
	day2 := now.Add(-2 * 24 * time.Hour)
	day3 := now.Add(-3 * 24 * time.Hour)

	writeBackupFile(t, dir, day2, 5000)
	writeBackupFile(t, dir, day3, 5000)

	// Limit below total (10000); neither file is within 23h so phase 1/2 won't
	// remove them. Phase 4 should remove the oldest daily keeper.
	s := &srv{backupSize: 6000}
	s.sizeBackups(dir)

	names, _ := listBackups(dir)
	if len(names) != 1 {
		t.Errorf("expected 1 file after phase-4 thinning, got %d: %v", len(names), names)
	}

	// day3 (oldest) should be deleted; day2 (newer) kept.
	day2str := day2.Format(backupTimeLayout)
	if !contains(names[0], day2str) {
		t.Errorf("expected day2 to survive, got %q (want ts containing %s)", names[0], day2str)
	}
}

// ---------------------------------------------------------------------------
// copyFile
// ---------------------------------------------------------------------------

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")

	data := []byte("hello backup")
	os.WriteFile(src, data, 0600)

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != string(data) {
		t.Errorf("copied data mismatch: got %q, want %q", got, data)
	}
}

func TestCopyFile_SrcMissing(t *testing.T) {
	dir := t.TempDir()

	err := copyFile(filepath.Join(dir, "missing.db"), filepath.Join(dir, "dst.db"))
	if err == nil {
		t.Error("expected error for missing source")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newBackupPathForTime generates the backup path for a specific time (test helper).
func newBackupPathForTime(dir string, t time.Time) string {
	name := backupFilePrefix + t.UTC().Format(backupTimeLayout) + backupFileSuffix
	return filepath.Join(dir, name)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
