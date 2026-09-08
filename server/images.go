package server

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg" // registers the JPEG decoder with image.Decode
	"image/png"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/tucats/idtrack/db"
)

// thumbnailMaxEdge is the longest edge, in pixels, of a generated image
// thumbnail. The other edge is scaled to preserve the source image's aspect
// ratio.
const thumbnailMaxEdge = 320

// maxImagePixels caps the decoded pixel count (width * height) of an
// uploaded image, checked before any resize work is done. This guards
// against a decompression-bomb-style upload: a small file that decodes to
// an enormous in-memory bitmap. 40 megapixels comfortably covers any real
// phone or camera photo (a 45MP full-frame camera is the practical upper
// bound most users will ever upload) while bounding worst-case memory use.
const maxImagePixels = 40_000_000

// errUnsupportedAttachment is returned by processUploadedAttachment when the
// input doesn't sniff as any supported kind (image, PDF, or text — see
// detectAttachmentKind) or, for an image specifically, doesn't decode.
// Handlers map it to 415 Unsupported Media Type.
var errUnsupportedAttachment = errors.New("unsupported or invalid attachment data")

// errImageTooLarge is returned by processUploadedImage when the decoded
// image exceeds maxImagePixels.
var errImageTooLarge = errors.New("image dimensions too large")

// processUploadedAttachment sniffs the kind of file data represents and
// dispatches to the matching per-kind processor, returning the bytes to
// store as the attachment's blob (stored []byte) plus a generated
// thumbnail. width/height are only meaningful for AttachmentImage (0
// otherwise). filename is never used for kind detection — only to caption
// the icon/generic thumbnails with something the uploader recognizes — see
// detectAttachmentKind's doc comment on why acceptance never trusts a
// client-declared Content-Type or a file extension.
func processUploadedAttachment(data []byte, filename string) (kind db.AttachmentType, stored, thumb []byte, width, height int, err error) {
	switch detectAttachmentKind(data) {
	case db.AttachmentImage:
		stored, thumb, width, height, err = processUploadedImage(data)
		if err != nil {
			return "", nil, nil, 0, 0, err
		}

		return db.AttachmentImage, stored, thumb, width, height, nil

	case db.AttachmentPDF:
		thumb, err = genericThumbnail("PDF", filename)
		if err != nil {
			return "", nil, nil, 0, 0, err
		}

		return db.AttachmentPDF, data, thumb, 0, 0, nil

	case db.AttachmentText:
		thumb, err = textThumbnail(data)
		if err != nil {
			return "", nil, nil, 0, 0, err
		}

		return db.AttachmentText, data, thumb, 0, 0, nil

	default:
		return "", nil, nil, 0, 0, errUnsupportedAttachment
	}
}

// detectAttachmentKind sniffs data's kind by content alone — never by a
// client-declared Content-Type or the upload's filename/extension, the same
// rule this file has always applied to images (see processUploadedImage's
// doc comment on why HEIC isn't accepted just because a client claims it).
// Order matters only for efficiency: the PDF magic-byte check is cheapest,
// then the (comparatively expensive) image decode attempt, then the text
// heuristic as the final fallback before rejecting outright.
func detectAttachmentKind(data []byte) db.AttachmentType {
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return db.AttachmentPDF
	}

	if _, _, err := image.Decode(bytes.NewReader(data)); err == nil {
		return db.AttachmentImage
	}

	if looksLikeText(data) {
		return db.AttachmentText
	}

	return ""
}

// looksLikeText is a conservative binary-vs-text heuristic: valid UTF-8 and
// free of NUL bytes, the same signal git and most editors use to guess
// whether a file is binary. An empty upload is never classified as text —
// there's nothing to preview.
func looksLikeText(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}

	return utf8.Valid(data)
}

// processUploadedImage decodes raw upload bytes, re-encodes them as PNG for
// canonical storage, and generates a PNG thumbnail.
//
// Only PNG and JPEG are supported today. HEIC (the format modern iPhones
// capture in by default) is deliberately not handled: the only Go HEIC
// decoders (jdeng/goheif, adrium/goheif) bundle libde265 via cgo, which
// would break idtrack's CGO_ENABLED=0 static build and complicate cross-
// compilation (see CLAUDE.md's "Docker containers require --foreground"
// section for the same build-portability concern applied elsewhere). A
// client uploading a HEIC file gets a clear 415 rather than a silent
// failure; converting HEIC to PNG/JPEG client-side before upload, or adding
// a HEIC decoder deliberately later, are both still open options.
func processUploadedImage(data []byte) (pngBytes, thumbBytes []byte, width, height int, err error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, 0, 0, errUnsupportedAttachment
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w <= 0 || h <= 0 {
		return nil, nil, 0, 0, errUnsupportedAttachment
	}

	if w*h > maxImagePixels {
		return nil, nil, 0, 0, errImageTooLarge
	}

	var fullBuf bytes.Buffer
	if err := png.Encode(&fullBuf, img); err != nil {
		return nil, nil, 0, 0, err
	}

	thumb := scaleToThumbnail(img, w, h)

	var thumbBuf bytes.Buffer
	if err := png.Encode(&thumbBuf, thumb); err != nil {
		return nil, nil, 0, 0, err
	}

	return fullBuf.Bytes(), thumbBuf.Bytes(), w, h, nil
}

// scaleToThumbnail returns a copy of img scaled so its longest edge is
// thumbnailMaxEdge pixels, preserving aspect ratio. If img is already
// smaller than thumbnailMaxEdge on both edges, it is returned unscaled —
// thumbnails never upscale a small source image.
func scaleToThumbnail(img image.Image, w, h int) image.Image {
	if w <= thumbnailMaxEdge && h <= thumbnailMaxEdge {
		return img
	}

	var tw, th int
	if w >= h {
		tw = thumbnailMaxEdge
		th = int(float64(h) * float64(thumbnailMaxEdge) / float64(w))
	} else {
		th = thumbnailMaxEdge
		tw = int(float64(w) * float64(thumbnailMaxEdge) / float64(h))
	}

	if tw < 1 {
		tw = 1
	}

	if th < 1 {
		th = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)

	return dst
}

// ---- Generic icon thumbnail (PDF today; the fallback for any future kind
// that has no content-derived thumbnail of its own) ----

// genericThumbSize is the edge length, in pixels, of the square canvas used
// for the generic icon+filename thumbnail. Square, so scaling it down into
// the fixed-size CSS thumbnail box (72x72, object-fit: cover — see
// .attachment-thumb in idtrack.css) never crops it.
const genericThumbSize = 320

var (
	genericIconBg      = color.RGBA{0xf3, 0xf4, 0xf6, 0xff} // canvas background
	genericIconPage    = color.RGBA{0xff, 0xff, 0xff, 0xff} // document body
	genericIconBorder  = color.RGBA{0x9c, 0xa3, 0xaf, 0xff} // document outline
	genericIconFold    = color.RGBA{0xe5, 0xe7, 0xeb, 0xff} // folded-corner shading
	genericBadgeBg     = color.RGBA{0xdc, 0x26, 0x26, 0xff} // strong red "badge" behind the label — high-contrast, reads at a glance
	genericBadgeText   = color.RGBA{0xff, 0xff, 0xff, 0xff}
	genericCaptionText = color.RGBA{0x6b, 0x72, 0x80, 0xff}
)

// genericBadgeScale is how many times basicfont.Face7x13's native 7x13
// glyph size the label (e.g. "PDF") is blown up before being drawn onto
// the badge — the bitmap font reads as too small/low-contrast at
// genericThumbSize on its own, so the label is rendered small, then
// nearest-neighbor scaled up (see drawScaledLabel) for a crisp, clearly
// legible result rather than a blurry one a smooth interpolator would give
// a 1-bit-per-pixel font.
const genericBadgeScale = 4

// genericBadgePadding is the margin, in already-scaled pixels, between the
// enlarged label and the edge of its colored badge rectangle.
const genericBadgePadding = 16

// genericThumbnail renders the fallback icon used for an attachment kind
// with no content-derived preview: a document silhouette with a bold,
// high-contrast colored badge carrying label (e.g. "PDF") centered on the
// page, and the original filename captioned underneath — so entries stay
// distinguishable, and their kind unambiguous at a glance, in the
// thumbnail grid without opening each one.
func genericThumbnail(label, filename string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, genericThumbSize, genericThumbSize))
	draw.Draw(img, img.Bounds(), &image.Uniform{genericIconBg}, image.Point{}, draw.Src)

	page := drawDocumentIcon(img)
	cx, cy := (page.Min.X+page.Max.X)/2, (page.Min.Y+page.Max.Y)/2

	drawBadgeLabel(img, label, cx, cy)
	drawCenteredText(img, truncateMonospace(displayFilename(filename), genericThumbSize-2*textPadding), genericCaptionY, genericCaptionText)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

const genericCaptionY = genericThumbSize - 30

// drawDocumentIcon paints a plain white page with a bordered outline and a
// shaded, dog-eared top-right corner onto img — just enough of a "document"
// silhouette to read as a file icon at thumbnail size. Returns the page's
// rectangle so the caller can center further content (the badge label) on
// it.
func drawDocumentIcon(img *image.RGBA) image.Rectangle {
	const (
		marginX = 90
		top     = 40
		bottom  = 110
		fold    = 34
	)

	b := img.Bounds()
	page := image.Rect(b.Min.X+marginX, b.Min.Y+top, b.Max.X-marginX, b.Max.Y-bottom)

	draw.Draw(img, page, &image.Uniform{genericIconPage}, image.Point{}, draw.Src)
	drawRectBorder(img, page, genericIconBorder)

	for y := 0; y < fold; y++ {
		for x := 0; x < fold-y; x++ {
			img.Set(page.Max.X-fold+x, page.Min.Y+y, genericIconFold)
		}
	}

	return page
}

// drawBadgeLabel paints a solid, high-contrast rectangle centered at
// (cx, cy) sized to fit label at genericBadgeScale, then draws label in
// genericBadgeText on top via drawScaledLabel.
func drawBadgeLabel(img *image.RGBA, label string, cx, cy int) {
	natW := font.MeasureString(basicfont.Face7x13, label).Round()
	natH := basicfont.Face7x13.Height

	badgeW := natW*genericBadgeScale + 2*genericBadgePadding
	badgeH := natH*genericBadgeScale + 2*genericBadgePadding
	badge := image.Rect(cx-badgeW/2, cy-badgeH/2, cx-badgeW/2+badgeW, cy-badgeH/2+badgeH)

	draw.Draw(img, badge, &image.Uniform{genericBadgeBg}, image.Point{}, draw.Src)
	drawScaledLabel(img, label, cx, cy, genericBadgeScale, genericBadgeText)
}

// drawScaledLabel renders s at basicfont.Face7x13's native size onto a
// small offscreen bitmap, nearest-neighbor scales it up by scale (crisp
// and blocky, rather than the blur a smooth interpolator would produce
// from a 1-bit-per-pixel font), and composites the result centered at
// (cx, cy) on img in fg.
func drawScaledLabel(img *image.RGBA, s string, cx, cy, scale int, fg color.Color) {
	natW := font.MeasureString(basicfont.Face7x13, s).Round()
	natH := basicfont.Face7x13.Height

	small := image.NewRGBA(image.Rect(0, 0, natW, natH))
	drawText(small, s, 0, basicfont.Face7x13.Ascent, fg)

	scaled := image.NewRGBA(image.Rect(0, 0, natW*scale, natH*scale))
	draw.NearestNeighbor.Scale(scaled, scaled.Bounds(), small, small.Bounds(), draw.Over, nil)

	sw, sh := scaled.Bounds().Dx(), scaled.Bounds().Dy()
	dst := image.Rect(cx-sw/2, cy-sh/2, cx-sw/2+sw, cy-sh/2+sh)
	draw.Draw(img, dst, scaled, image.Point{}, draw.Over)
}

func drawRectBorder(img *image.RGBA, r image.Rectangle, c color.Color) {
	for x := r.Min.X; x < r.Max.X; x++ {
		img.Set(x, r.Min.Y, c)
		img.Set(x, r.Max.Y-1, c)
	}

	for y := r.Min.Y; y < r.Max.Y; y++ {
		img.Set(r.Min.X, y, c)
		img.Set(r.Max.X-1, y, c)
	}
}

// ---- Text thumbnail ----

// textThumbLines is how many lines of a text attachment are rendered into
// its thumbnail.
const textThumbLines = 45

// textThumbCols is how many characters wide each rendered line is, chosen
// to match textThumbWidth against basicfont.Face7x13's fixed 7px advance.
const textThumbCols = 60

const (
	textCharWidth  = 7  // basicfont.Face7x13.Advance
	textLineHeight = 15 // glyph height (13) plus a little line spacing
	textPadding    = 10
	textThumbWidth = textThumbCols*textCharWidth + 2*textPadding
)

var (
	textThumbBg = color.RGBA{0xff, 0xff, 0xff, 0xff}
	textThumbFg = color.RGBA{0x1f, 0x29, 0x37, 0xff}
)

// textThumbnail renders the first textThumbLines lines of data (a text
// attachment's raw bytes) as a monospace-text image, so the thumbnail grid
// shows an actual preview of the file's content rather than a generic icon.
func textThumbnail(data []byte) ([]byte, error) {
	lines := extractLines(data, textThumbLines, textThumbCols)

	height := 2*textPadding + len(lines)*textLineHeight
	if height < textLineHeight {
		height = textLineHeight
	}

	img := image.NewRGBA(image.Rect(0, 0, textThumbWidth, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{textThumbBg}, image.Point{}, draw.Src)

	y := textPadding + 11 // baseline offset within the first line's row
	for _, line := range lines {
		drawText(img, line, textPadding, y, textThumbFg)
		y += textLineHeight
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// extractLines returns up to maxLines lines from data (split on '\n', with
// any trailing '\r' trimmed for CRLF input), each sanitized and truncated
// to maxCols runes via sanitizeLine.
func extractLines(data []byte, maxLines, maxCols int) []string {
	// SplitN(..., maxLines+1) only needs to locate the first maxLines
	// newlines — the "+1" bucket absorbs the remainder of a larger file
	// without it being individually re-split, so this stays cheap
	// regardless of total file size.
	rawLines := strings.SplitN(string(data), "\n", maxLines+1)
	if len(rawLines) > maxLines {
		rawLines = rawLines[:maxLines]
	}

	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		lines = append(lines, sanitizeLine(strings.TrimSuffix(l, "\r"), maxCols))
	}

	return lines
}

// sanitizeLine truncates s to maxCols runes and replaces any rune outside
// basicfont.Face7x13's printable-ASCII range with a space, so a stray
// control byte or non-ASCII character can't render as a blank/garbled
// glyph or throw off column alignment.
func sanitizeLine(s string, maxCols int) string {
	r := []rune(s)
	if len(r) > maxCols {
		r = r[:maxCols]
	}

	for i, c := range r {
		if c < 0x20 || c > 0x7e {
			r[i] = ' '
		}
	}

	return string(r)
}

// ---- Shared text-drawing helpers ----

func drawText(img *image.RGBA, s string, x, y int, c color.Color) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

// drawCenteredText draws s horizontally centered within img at baseline y.
func drawCenteredText(img *image.RGBA, s string, y int, c color.Color) {
	width := font.MeasureString(basicfont.Face7x13, s).Round()
	x := (img.Bounds().Dx() - width) / 2

	if x < 0 {
		x = 0
	}

	drawText(img, s, x, y, c)
}

// truncateMonospace shortens s (if needed) to fit within maxWidthPx when
// rendered with basicfont.Face7x13's fixed 7px-per-rune advance, appending
// "..." (basicfont has no glyph for the single-rune ellipsis) when it does.
func truncateMonospace(s string, maxWidthPx int) string {
	maxChars := maxWidthPx / textCharWidth

	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}

	if maxChars <= 3 {
		return string(r[:maxChars])
	}

	return string(r[:maxChars-3]) + "..."
}

// displayFilename returns name, or a generic placeholder when the upload
// carried no filename at all.
func displayFilename(name string) string {
	if name == "" {
		return "file"
	}

	return name
}
