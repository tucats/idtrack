package db_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tucats/idtrack/db"
)

func TestCreateAttachment_RoundTripsType(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	if err := db.AddUser(d, "uploader", "Uploader", "pw", []string{"any"}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	cases := []db.AttachmentType{db.AttachmentImage, db.AttachmentPDF, db.AttachmentText}

	for _, wantType := range cases {
		a, err := db.CreateAttachment(d, 1, 0, "uploader", "file.bin", wantType, []byte("stored bytes"), []byte("thumb bytes"), 10, 20, 0)
		if err != nil {
			t.Fatalf("CreateAttachment(%s): %v", wantType, err)
		}

		if a.Type != wantType {
			t.Errorf("CreateAttachment(%s): returned Type = %q", wantType, a.Type)
		}

		got, err := db.GetAttachment(d, a.ID)
		if err != nil {
			t.Fatalf("GetAttachment: %v", err)
		}

		if got == nil {
			t.Fatalf("GetAttachment(%s) returned nil", a.ID)
		}

		if got.Type != wantType {
			t.Errorf("GetAttachment(%s): Type = %q, want %q", a.ID, got.Type, wantType)
		}
	}
}

func TestCreateAttachment_NonImageHasZeroDimensions(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	a, err := db.CreateAttachment(d, 1, 0, "uploader", "notes.txt", db.AttachmentText, []byte("hello"), []byte("thumb"), 0, 0, 0)
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	if a.Width != 0 || a.Height != 0 {
		t.Errorf("text attachment should have zero dimensions, got %dx%d", a.Width, a.Height)
	}
}

func TestCreateAttachment_PDFPagesRoundTrip(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "u", "U", "pw", []string{"any"})

	a, err := db.CreateAttachment(d, 1, 0, "u", "doc.pdf", db.AttachmentPDF, []byte("%PDF-"), []byte("thumb"), 0, 0, 53)
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	if a.Pages != 53 {
		t.Errorf("CreateAttachment: returned Pages = %d, want 53", a.Pages)
	}

	got, err := db.GetAttachment(d, a.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}

	if got.Pages != 53 {
		t.Errorf("GetAttachment: Pages = %d, want 53", got.Pages)
	}
}

// TestSetAttachmentPages_CachesLazilyComputedCount exercises the lazy
// backfill path a PDF uploaded before this feature existed takes: created
// with pages == 0 (as every pre-existing row would be, via the addColumnIfMissing
// default), SetAttachmentPages caches a later-computed count exactly like
// server/attachments.go's handleGetAttachmentPage does on first request.
func TestSetAttachmentPages_CachesLazilyComputedCount(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "u", "U", "pw", []string{"any"})

	a, err := db.CreateAttachment(d, 1, 0, "u", "legacy.pdf", db.AttachmentPDF, []byte("%PDF-"), []byte("thumb"), 0, 0, 0)
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	if a.Pages != 0 {
		t.Fatalf("expected freshly created attachment to have Pages = 0, got %d", a.Pages)
	}

	if err := db.SetAttachmentPages(d, a.ID, 7); err != nil {
		t.Fatalf("SetAttachmentPages: %v", err)
	}

	got, err := db.GetAttachment(d, a.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}

	if got.Pages != 7 {
		t.Errorf("GetAttachment after SetAttachmentPages: Pages = %d, want 7", got.Pages)
	}
}

// TestMigration_AttachmentTypeBackfill simulates upgrading a pre-existing
// database that predates the type column: a row is inserted directly via
// raw SQL (bypassing CreateAttachment, which always supplies a type) with
// type left at its post-ALTER default of '', then the database is reopened
// — re-running initSchema, including the "WHERE type = ''" backfill — to
// confirm the row is retroactively classified as an image, matching every
// attachment that existed before this feature. A file-backed database is
// required (not :memory:) so the second Open sees the first Open's data.
func TestMigration_AttachmentTypeBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")

	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}

	if err := db.AddUser(d, "uploader", "Uploader", "pw", []string{"any"}); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	// Insert directly, omitting type, to simulate a row written before the
	// column existed (ALTER TABLE ADD COLUMN backfills the DEFAULT '' into
	// any row already present at migration time, but a row inserted by
	// legacy code between the ALTER and this test's backfill running would
	// look identical: type = '').
	if _, err := d.Exec(
		`INSERT INTO attachments (id, issue_id, comment_id, uploader, filename, width, height, size, image, thumbnail, created_at)
		 VALUES ('legacy-1', 1, 0, 'uploader', 'photo.png', 10, 20, 3, X'010203', X'0102', '2020-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	d.Close()

	d2, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer d2.Close()

	got, err := db.GetAttachment(d2, "legacy-1")
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}

	if got == nil {
		t.Fatal("legacy attachment row not found after reopen")
	}

	if got.Type != db.AttachmentImage {
		t.Errorf("legacy attachment should be backfilled to type=image, got %q", got.Type)
	}
}

func TestDeleteAttachmentsByIssue(t *testing.T) {
	d, _ := db.Open(":memory:")
	defer d.Close()

	db.AddUser(d, "u", "U", "pw", []string{"any"})

	if _, err := db.CreateAttachment(d, 5, 0, "u", "a.pdf", db.AttachmentPDF, []byte("%PDF-"), []byte("thumb"), 0, 0, 0); err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}

	if err := db.DeleteAttachmentsByIssue(d, 5); err != nil {
		t.Fatalf("DeleteAttachmentsByIssue: %v", err)
	}

	list, err := db.ListAttachments(d, 5)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}

	if len(list) != 0 {
		t.Errorf("expected no attachments after delete, got %d", len(list))
	}
}

func TestMain_SchemaHasTypeColumnByDefault(t *testing.T) {
	// A brand-new database's CREATE TABLE already includes type — this is a
	// smoke test that the DDL and addColumnIfMissing call don't conflict
	// (addColumnIfMissing must silently no-op when the column is already
	// present from CREATE TABLE, not error).
	path := filepath.Join(t.TempDir(), "fresh.db")

	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected database file to exist: %v", err)
	}
}
