package server

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg" // registers the JPEG decoder with image.Decode
	"image/png"

	"golang.org/x/image/draw"
)

// thumbnailMaxEdge is the longest edge, in pixels, of a generated thumbnail.
// The other edge is scaled to preserve the source image's aspect ratio.
const thumbnailMaxEdge = 320

// maxImagePixels caps the decoded pixel count (width * height) of an
// uploaded image, checked before any resize work is done. This guards
// against a decompression-bomb-style upload: a small file that decodes to
// an enormous in-memory bitmap. 40 megapixels comfortably covers any real
// phone or camera photo (a 45MP full-frame camera is the practical upper
// bound most users will ever upload) while bounding worst-case memory use.
const maxImagePixels = 40_000_000

// errUnsupportedImage is returned by processUploadedImage when the input
// does not decode as a supported format (PNG or JPEG — see the HEIC note
// below) or exceeds maxImagePixels. Handlers map it to 415 Unsupported
// Media Type / 400 Bad Request as appropriate.
var errUnsupportedImage = errors.New("unsupported or invalid image data")

// errImageTooLarge is returned by processUploadedImage when the decoded
// image exceeds maxImagePixels.
var errImageTooLarge = errors.New("image dimensions too large")

// processUploadedImage decodes raw upload bytes, re-encodes them as PNG for
// canonical storage, and generates a PNG thumbnail. It never trusts a
// client-supplied Content-Type header — acceptance is determined solely by
// whether the bytes actually decode as a registered image.Decode format.
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
		return nil, nil, 0, 0, errUnsupportedImage
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w <= 0 || h <= 0 {
		return nil, nil, 0, 0, errUnsupportedImage
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
