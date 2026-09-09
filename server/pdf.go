package server

import (
	"bytes"
	"context"
	"image"
	"time"

	pdfviewer "github.com/tucats/pdf-viewer"
)

// pdfPageThumbMaxEdge is the longest edge, in pixels, of a PDF's generated
// first-page thumbnail — matches thumbnailMaxEdge (server/images.go), the
// same size used for image-attachment thumbnails, so every kind's thumbnail
// grid entry is visually consistent.
const pdfPageThumbMaxEdge = thumbnailMaxEdge

// pdfPageViewMaxEdge is the longest edge, in pixels, of a PDF page rendered
// for the full-size attachment viewer (server/attachments.go's
// handleGetAttachmentPage) — larger than the thumbnail size so text is
// actually legible, but still bounded so a large/complex page can't produce
// an unreasonably large PNG to encode and transfer. The viewer's CSS
// (.av-image) already caps the on-screen display size responsively in CSS
// pixels, but a HiDPI/Retina display renders those at 2x (or more) actual
// device pixels — 1600 was sized for a 1x display and looked visibly soft
// on a Retina Mac, so this is doubled to 3200 to stay sharp at a 2x device
// pixel ratio (the common case; a 3x phone display can still slightly
// upscale, the same tradeoff every fixed-resolution raster image makes).
// 3200 stays comfortably under pdf-viewer's own 64-megapixel Render/
// Thumbnail cap even for an unusually elongated page.
const pdfPageViewMaxEdge = 3200

// pdfRenderTimeout bounds how long a single page render (thumbnail or full
// view) is allowed to take. pdf-viewer's own maxRenderPixels cap already
// guards against a pathologically large output image, but a page can still
// be slow to interpret/rasterize for other reasons (a pathological content
// stream); this is defense in depth against a single malicious or corrupt
// upload tying up a request goroutine indefinitely — the same "never fully
// trust attachment content" posture as maxImagePixels and the HEIC/decode
// checks in server/images.go.
const pdfRenderTimeout = 10 * time.Second

// openPDFDocument parses data as a PDF document. It always opts into font
// substitution (pdfviewer.WithFontSubstitution) with system font
// directories enabled (FontSubstitution's zero value already means "scan
// platform-default font directories" — see that type's DisableSystemDefaults
// field — so passing the zero value here explicitly turns substitution on
// rather than leaving the document with none at all): a PDF referencing a
// font it doesn't embed a program for is common enough in real-world files
// that rendering it with pdf-viewer's built-in notdefGlyph placeholder boxes
// instead of a real substituted outline would make many ordinary documents
// look badly broken.
func openPDFDocument(data []byte) (*pdfviewer.Document, error) {
	return pdfviewer.Open(bytes.NewReader(data), int64(len(data)), pdfviewer.WithFontSubstitution(pdfviewer.FontSubstitution{}))
}

// pdfPageCount opens data as a PDF and returns its page count. Opening a
// document only parses its structure and page tree — it does not render any
// page content — so this is cheap relative to actually rendering a page.
func pdfPageCount(data []byte) (int, error) {
	doc, err := openPDFDocument(data)
	if err != nil {
		return 0, err
	}
	defer doc.Close()

	return doc.PageCount(), nil
}

// renderPDFPage renders the zero-based pageIndex page of the PDF in data,
// scaled (preserving aspect ratio) so its longer edge is at most maxEdge
// pixels — the same bounded-thumbnail behavior Page.Thumbnail always uses,
// just parameterized so both the small grid thumbnail and the larger
// full-page view (server/attachments.go's handleGetAttachmentPage) share
// this one implementation rather than each hand-rolling the render call.
func renderPDFPage(data []byte, pageIndex, maxEdge int) (image.Image, error) {
	doc, err := openPDFDocument(data)
	if err != nil {
		return nil, err
	}
	defer doc.Close()

	page, err := doc.Page(pageIndex)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pdfRenderTimeout)
	defer cancel()

	return page.Thumbnail(ctx, pdfviewer.ThumbnailOptions{MaxDimension: maxEdge})
}
