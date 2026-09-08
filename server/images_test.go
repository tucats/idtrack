package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/tucats/idtrack/db"
)

func samplePNG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	return buf.Bytes()
}

func TestDetectAttachmentKind(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want db.AttachmentType
	}{
		{"pdf magic bytes", []byte("%PDF-1.4\n...rest of file..."), db.AttachmentPDF},
		{"png image", samplePNG(t), db.AttachmentImage},
		{"plain text", []byte("hello, this is a plain text file\nwith two lines\n"), db.AttachmentText},
		{"empty", []byte{}, ""},
		{"binary garbage", []byte{0x00, 0x01, 0xff, 0xfe, 0x00, 0x10, 0x20}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectAttachmentKind(tc.data); got != tc.want {
				t.Errorf("detectAttachmentKind(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestProcessUploadedAttachment_Image(t *testing.T) {
	kind, stored, thumb, width, height, err := processUploadedAttachment(samplePNG(t), "photo.png")
	if err != nil {
		t.Fatalf("processUploadedAttachment: %v", err)
	}

	if kind != db.AttachmentImage {
		t.Errorf("kind = %q, want image", kind)
	}

	if width != 4 || height != 4 {
		t.Errorf("dimensions = %dx%d, want 4x4", width, height)
	}

	if len(stored) == 0 || len(thumb) == 0 {
		t.Error("expected non-empty stored and thumbnail bytes")
	}
}

func TestProcessUploadedAttachment_PDF(t *testing.T) {
	pdfBytes := []byte("%PDF-1.7\nfake pdf content for testing\n%%EOF")

	kind, stored, thumb, width, height, err := processUploadedAttachment(pdfBytes, "report.pdf")
	if err != nil {
		t.Fatalf("processUploadedAttachment: %v", err)
	}

	if kind != db.AttachmentPDF {
		t.Errorf("kind = %q, want pdf", kind)
	}

	if !bytes.Equal(stored, pdfBytes) {
		t.Error("PDF attachment should store the raw original bytes unchanged")
	}

	if width != 0 || height != 0 {
		t.Errorf("PDF dimensions should be 0x0, got %dx%d", width, height)
	}

	if _, err := png.Decode(bytes.NewReader(thumb)); err != nil {
		t.Errorf("PDF thumbnail should be a valid PNG: %v", err)
	}
}

func TestProcessUploadedAttachment_Text(t *testing.T) {
	textBytes := []byte("line one\nline two\nline three\n")

	kind, stored, thumb, _, _, err := processUploadedAttachment(textBytes, "notes.txt")
	if err != nil {
		t.Fatalf("processUploadedAttachment: %v", err)
	}

	if kind != db.AttachmentText {
		t.Errorf("kind = %q, want text", kind)
	}

	if !bytes.Equal(stored, textBytes) {
		t.Error("text attachment should store the raw original bytes unchanged")
	}

	if _, err := png.Decode(bytes.NewReader(thumb)); err != nil {
		t.Errorf("text thumbnail should be a valid PNG: %v", err)
	}
}

func TestProcessUploadedAttachment_Rejected(t *testing.T) {
	_, _, _, _, _, err := processUploadedAttachment([]byte{0x00, 0x01, 0x02, 0xff, 0xfe}, "mystery.bin")
	if err != errUnsupportedAttachment {
		t.Errorf("err = %v, want errUnsupportedAttachment", err)
	}
}

func TestExtractLines_CapsAtMaxLinesAndCols(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("this line is deliberately much longer than the sixty column cap so it should be truncated\n")
	}

	lines := extractLines([]byte(sb.String()), 45, 60)

	if len(lines) != 45 {
		t.Fatalf("expected 45 lines, got %d", len(lines))
	}

	for i, l := range lines {
		if len([]rune(l)) > 60 {
			t.Errorf("line %d has %d runes, want <= 60", i, len([]rune(l)))
		}
	}
}

func TestExtractLines_FewerLinesThanCap(t *testing.T) {
	lines := extractLines([]byte("only\ntwo lines\n"), 45, 60)

	// "only\ntwo lines\n" splits into ["only", "two lines", ""] on '\n';
	// all three are returned since the input has fewer than the 45-line cap.
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), lines)
	}

	if lines[0] != "only" || lines[1] != "two lines" {
		t.Errorf("unexpected line content: %q", lines)
	}
}

func TestTruncateMonospace(t *testing.T) {
	short := truncateMonospace("short.txt", 700) // plenty of room
	if short != "short.txt" {
		t.Errorf("short name should be unchanged, got %q", short)
	}

	long := truncateMonospace("a-very-long-filename-that-will-not-fit-in-the-caption-width.txt", 70) // 10 chars
	if len([]rune(long)) > 10 {
		t.Errorf("truncated name should fit within 10 chars, got %q (%d runes)", long, len([]rune(long)))
	}

	if !strings.HasSuffix(long, "...") {
		t.Errorf("truncated name should end with ..., got %q", long)
	}
}
