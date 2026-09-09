package server

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// readTestdataPDF reads a fixture PDF from server/testdata — see that
// directory's provenance note (borrowed from github.com/tucats/pdf-viewer's
// own MIT-licensed test corpus, same author, same license) for why these
// specific well-formed files exist here rather than a hand-rolled fake PDF.
func readTestdataPDF(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}

	return data
}

func TestPdfPageCount(t *testing.T) {
	cases := []struct {
		file string
		want int
	}{
		{"minimal-blank-page.pdf", 1},
		{"two-pages.pdf", 2},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got, err := pdfPageCount(readTestdataPDF(t, tc.file))
			if err != nil {
				t.Fatalf("pdfPageCount: %v", err)
			}

			if got != tc.want {
				t.Errorf("pdfPageCount(%s) = %d, want %d", tc.file, got, tc.want)
			}
		})
	}
}

func TestPdfPageCount_Malformed(t *testing.T) {
	if _, err := pdfPageCount([]byte("%PDF-1.7\nnot a real pdf\n%%EOF")); err == nil {
		t.Error("expected an error parsing malformed PDF content, got nil")
	}
}

func TestRenderPDFPage(t *testing.T) {
	data := readTestdataPDF(t, "two-pages.pdf")

	for _, pageIndex := range []int{0, 1} {
		img, err := renderPDFPage(data, pageIndex, pdfPageThumbMaxEdge)
		if err != nil {
			t.Fatalf("renderPDFPage(page %d): %v", pageIndex, err)
		}

		b := img.Bounds()
		if b.Dx() == 0 || b.Dy() == 0 {
			t.Errorf("renderPDFPage(page %d) returned an empty image", pageIndex)
		}

		if b.Dx() > pdfPageThumbMaxEdge || b.Dy() > pdfPageThumbMaxEdge {
			t.Errorf("renderPDFPage(page %d) = %dx%d, exceeds max edge %d", pageIndex, b.Dx(), b.Dy(), pdfPageThumbMaxEdge)
		}

		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Errorf("encoding rendered page %d as PNG: %v", pageIndex, err)
		}
	}
}

func TestRenderPDFPage_OutOfRange(t *testing.T) {
	data := readTestdataPDF(t, "minimal-blank-page.pdf")

	if _, err := renderPDFPage(data, 5, pdfPageThumbMaxEdge); err == nil {
		t.Error("expected an error rendering an out-of-range page index, got nil")
	}
}
