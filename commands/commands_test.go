package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// parseBackupSize
// ---------------------------------------------------------------------------

func TestParseBackupSize_Zero(t *testing.T) {
	for _, s := range []string{"0", "off", "", "OFF", "Off"} {
		n, err := parseBackupSize(s)
		if err != nil {
			t.Errorf("parseBackupSize(%q): unexpected error: %v", s, err)
		}

		if n != 0 {
			t.Errorf("parseBackupSize(%q): got %d, want 0", s, n)
		}
	}
}

func TestParseBackupSize_Bytes(t *testing.T) {
	n, err := parseBackupSize("1024b")
	if err != nil {
		t.Fatalf("parseBackupSize(1024b): %v", err)
	}

	if n != 1024 {
		t.Errorf("got %d, want 1024", n)
	}
}

func TestParseBackupSize_Kilobytes(t *testing.T) {
	n, err := parseBackupSize("4kb")
	if err != nil {
		t.Fatalf("parseBackupSize(4kb): %v", err)
	}

	if n != 4*1024 {
		t.Errorf("got %d, want %d", n, 4*1024)
	}
}

func TestParseBackupSize_Megabytes(t *testing.T) {
	n, err := parseBackupSize("100mb")
	if err != nil {
		t.Fatalf("parseBackupSize(100mb): %v", err)
	}

	if n != 100*1024*1024 {
		t.Errorf("got %d, want %d", n, 100*1024*1024)
	}
}

func TestParseBackupSize_Gigabytes(t *testing.T) {
	n, err := parseBackupSize("2gb")
	if err != nil {
		t.Fatalf("parseBackupSize(2gb): %v", err)
	}

	if n != 2*1024*1024*1024 {
		t.Errorf("got %d, want %d", n, 2*1024*1024*1024)
	}
}

func TestParseBackupSize_Terabytes(t *testing.T) {
	n, err := parseBackupSize("1tb")
	if err != nil {
		t.Fatalf("parseBackupSize(1tb): %v", err)
	}

	if n != 1<<40 {
		t.Errorf("got %d, want %d", n, int64(1<<40))
	}
}

func TestParseBackupSize_CaseInsensitive(t *testing.T) {
	cases := []string{"500MB", "500Mb", "500mB"}
	want := int64(500 * 1024 * 1024)

	for _, s := range cases {
		n, err := parseBackupSize(s)
		if err != nil {
			t.Errorf("parseBackupSize(%q): %v", s, err)

			continue
		}

		if n != want {
			t.Errorf("parseBackupSize(%q): got %d, want %d", s, n, want)
		}
	}
}

func TestParseBackupSize_Decimal(t *testing.T) {
	// 0.5 GB = 512 MB
	n, err := parseBackupSize("0.5gb")
	if err != nil {
		t.Fatalf("parseBackupSize(0.5gb): %v", err)
	}

	if n != 512*1024*1024 {
		t.Errorf("got %d, want %d", n, 512*1024*1024)
	}
}

func TestParseBackupSize_DecimalDotOnly(t *testing.T) {
	n, err := parseBackupSize(".5gb")
	if err != nil {
		t.Fatalf("parseBackupSize(.5gb): %v", err)
	}

	if n != 512*1024*1024 {
		t.Errorf("got %d, want %d", n, 512*1024*1024)
	}
}

func TestParseBackupSize_NoSuffix(t *testing.T) {
	// No suffix — value is bytes.
	n, err := parseBackupSize("2048")
	if err != nil {
		t.Fatalf("parseBackupSize(2048): %v", err)
	}

	if n != 2048 {
		t.Errorf("got %d, want 2048", n)
	}
}

func TestParseBackupSize_TbNotConfusedWithB(t *testing.T) {
	// "1tb" must not be parsed as "1t" with suffix "b".
	n, err := parseBackupSize("1tb")
	if err != nil {
		t.Fatalf("parseBackupSize(1tb): %v", err)
	}

	if n != 1<<40 {
		t.Errorf("got %d, want %d (terabyte)", n, int64(1<<40))
	}
}

func TestParseBackupSize_GbNotConfusedWithB(t *testing.T) {
	n, err := parseBackupSize("1gb")
	if err != nil {
		t.Fatalf("parseBackupSize(1gb): %v", err)
	}

	if n != 1<<30 {
		t.Errorf("got %d, want %d (gigabyte)", n, int64(1<<30))
	}
}

func TestParseBackupSize_Invalid(t *testing.T) {
	cases := []string{"abc", "1xx", "1.2.3mb", "-1mb"}
	for _, s := range cases {
		_, err := parseBackupSize(s)
		if err == nil {
			t.Errorf("parseBackupSize(%q): expected error, got nil", s)
		}
	}
}

func TestParseBackupSize_Whitespace(t *testing.T) {
	n, err := parseBackupSize("  100mb  ")
	if err != nil {
		t.Fatalf("parseBackupSize with whitespace: %v", err)
	}

	if n != 100*1024*1024 {
		t.Errorf("got %d, want %d", n, 100*1024*1024)
	}
}

// ---------------------------------------------------------------------------
// loadDefaults
// ---------------------------------------------------------------------------

func TestLoadDefaults_MissingFile(t *testing.T) {
	// Override HOME so loadDefaults looks at a temp directory with no defaults.json.
	tmp := t.TempDir()
	orig := os.Getenv("HOME")
	os.Setenv("HOME", tmp)

	defer os.Setenv("HOME", orig)

	d := loadDefaults()

	// Missing file should return zero-value struct without error.
	if d.Port != 0 || d.Database != "" {
		t.Errorf("expected zero defaults for missing file, got port=%d db=%q", d.Port, d.Database)
	}
}

func TestLoadDefaults_ValidFile(t *testing.T) {
	tmp := t.TempDir()
	idtrackDir := filepath.Join(tmp, ".idtrack")
	os.MkdirAll(idtrackDir, 0700)

	want := defaults{
		Port:     9443,
		Database: filepath.Join(tmp, "idtrack.db"),
	}

	data, _ := json.MarshalIndent(want, "", "  ")
	os.WriteFile(filepath.Join(idtrackDir, "defaults.json"), append(data, '\n'), 0600)

	orig := os.Getenv("HOME")
	os.Setenv("HOME", tmp)

	defer os.Setenv("HOME", orig)

	got := loadDefaults()

	if got.Port != want.Port {
		t.Errorf("port: got %d, want %d", got.Port, want.Port)
	}

	if got.Database != want.Database {
		t.Errorf("database: got %q, want %q", got.Database, want.Database)
	}
}

func TestLoadDefaults_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	idtrackDir := filepath.Join(tmp, ".idtrack")
	os.MkdirAll(idtrackDir, 0700)

	os.WriteFile(filepath.Join(idtrackDir, "defaults.json"), []byte("{invalid json}"), 0600)

	orig := os.Getenv("HOME")
	os.Setenv("HOME", tmp)

	defer os.Setenv("HOME", orig)

	// Should return zero defaults without panicking.
	d := loadDefaults()
	if d.Port != 0 {
		t.Errorf("expected zero defaults for invalid JSON, got port=%d", d.Port)
	}
}

func TestLoadDefaults_RelativeDBMigrated(t *testing.T) {
	tmp := t.TempDir()
	idtrackDir := filepath.Join(tmp, ".idtrack")
	os.MkdirAll(idtrackDir, 0700)

	// Write a relative database path — migration should convert it to absolute.
	d := defaults{Port: 8443, Database: "relative/idtrack.db"}
	data, _ := json.MarshalIndent(d, "", "  ")
	os.WriteFile(filepath.Join(idtrackDir, "defaults.json"), append(data, '\n'), 0600)

	orig := os.Getenv("HOME")
	os.Setenv("HOME", tmp)

	defer os.Setenv("HOME", orig)

	got := loadDefaults()

	if !filepath.IsAbs(got.Database) {
		t.Errorf("expected absolute path after migration, got %q", got.Database)
	}
}
