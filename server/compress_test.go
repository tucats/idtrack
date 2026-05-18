package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newGzipRequest builds a GET request with Accept-Encoding: gzip.
func newGzipRequest(t *testing.T) *http.Request {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")

	return r
}

// decompress reads the gzip-compressed body from a recorder.
func decompress(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()

	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}

	defer gr.Close()

	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("reading gzip: %v", err)
	}

	return data
}

// handlerWith returns an http.Handler that writes the given body.
func handlerWith(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	})
}

func TestGzipHandler_SmallBodyNotCompressed(t *testing.T) {
	body := "small" // well under gzipThreshold

	h := gzipHandler(handlerWith(body))
	r := newGzipRequest(t)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Error("small body should not be gzip-encoded")
	}

	if w.Body.String() != body {
		t.Errorf("body: got %q, want %q", w.Body.String(), body)
	}
}

func TestGzipHandler_LargeBodyCompressed(t *testing.T) {
	// Body larger than gzipThreshold (1400 bytes).
	body := strings.Repeat("a", gzipThreshold+1)

	h := gzipHandler(handlerWith(body))
	r := newGzipRequest(t)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("large body should be gzip-encoded, got Content-Encoding: %q", enc)
	}

	// Verify the decompressed body matches the original.
	got := string(decompress(t, w))
	if got != body {
		t.Errorf("decompressed body mismatch: len got=%d, want=%d", len(got), len(body))
	}
}

func TestGzipHandler_NoAcceptEncoding_Passthrough(t *testing.T) {
	body := strings.Repeat("x", gzipThreshold+1)

	h := gzipHandler(handlerWith(body))
	r := httptest.NewRequest(http.MethodGet, "/", nil) // no Accept-Encoding
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if enc := w.Header().Get("Content-Encoding"); enc == "gzip" {
		t.Error("should not compress when Accept-Encoding: gzip absent")
	}

	if w.Body.String() != body {
		t.Errorf("body passthrough failed")
	}
}

func TestGzipHandler_VaryHeader(t *testing.T) {
	h := gzipHandler(handlerWith("hello"))
	r := newGzipRequest(t)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if vary := w.Header().Get("Vary"); vary != "Accept-Encoding" {
		t.Errorf("Vary header: got %q, want %q", vary, "Accept-Encoding")
	}
}

func TestGzipHandler_VaryHeaderWithoutGzip(t *testing.T) {
	// Vary must be set even when the client doesn't ask for gzip.
	h := gzipHandler(handlerWith("hello"))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if vary := w.Header().Get("Vary"); vary != "Accept-Encoding" {
		t.Errorf("Vary header (no gzip): got %q, want %q", vary, "Accept-Encoding")
	}
}

func TestGzipHandler_ExactThresholdNotCompressed(t *testing.T) {
	// Exactly at threshold — not compressed (threshold is minimum for compression).
	body := strings.Repeat("b", gzipThreshold-1)

	h := gzipHandler(handlerWith(body))
	r := newGzipRequest(t)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Error("body at threshold-1 should not be compressed")
	}
}

func TestGzipHandler_CustomStatusCode(t *testing.T) {
	// Handler writes a non-200 status code; gzip wrapper should propagate it.
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, strings.Repeat("x", gzipThreshold+1))
	}))

	r := newGzipRequest(t)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestGzipHandler_ContentLengthRemovedOnCompress(t *testing.T) {
	// When compressed, Content-Length must be removed (size changes).
	h := gzipHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := strings.Repeat("y", gzipThreshold+1)
		w.Header().Set("Content-Length", "9999")
		io.WriteString(w, body)
	}))

	r := newGzipRequest(t)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r)

	if w.Header().Get("Content-Length") == "9999" {
		t.Error("Content-Length should be removed when compressing")
	}
}

func TestBufferingWriter_WriteHeader(t *testing.T) {
	inner := httptest.NewRecorder()
	bw := &bufferingWriter{ResponseWriter: inner}

	bw.WriteHeader(http.StatusCreated)

	// Status is buffered — not yet sent to the underlying ResponseWriter.
	if inner.Code != 200 {
		t.Errorf("WriteHeader should be buffered, but inner got %d", inner.Code)
	}

	if bw.status != http.StatusCreated {
		t.Errorf("buffered status: got %d, want %d", bw.status, http.StatusCreated)
	}
}

func TestBufferingWriter_Write(t *testing.T) {
	inner := httptest.NewRecorder()
	bw := &bufferingWriter{ResponseWriter: inner}

	data := []byte("hello")
	n, err := bw.Write(data)

	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if n != len(data) {
		t.Errorf("n: got %d, want %d", n, len(data))
	}

	if !bytes.Equal(bw.buf.Bytes(), data) {
		t.Errorf("buf: got %q, want %q", bw.buf.Bytes(), data)
	}

	// Data not yet written to inner.
	if inner.Body.Len() != 0 {
		t.Error("data should be buffered, not written to inner yet")
	}
}

func TestBufferingWriter_Flush_Small(t *testing.T) {
	inner := httptest.NewRecorder()
	bw := &bufferingWriter{ResponseWriter: inner, status: http.StatusOK}

	bw.buf.WriteString("tiny")
	bw.flush(inner)

	if inner.Body.String() != "tiny" {
		t.Errorf("small body: got %q, want %q", inner.Body.String(), "tiny")
	}

	if inner.Header().Get("Content-Encoding") == "gzip" {
		t.Error("small body should not be compressed")
	}
}
