package server

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipThreshold is the minimum response body size in bytes before gzip
// compression is applied. 1400 bytes is one standard Ethernet MTU payload
// (1500-byte frame minus 40 bytes of TCP/IP headers), so anything below this
// fits in a single TCP segment and cannot be made faster by compression.
const gzipThreshold = 1400

// gzipHandler compresses responses whose body meets gzipThreshold bytes and
// whose requester advertises Accept-Encoding: gzip. Responses below the
// threshold are written uncompressed — the CPU overhead of compressing a
// payload that already fits in one TCP segment is never worth the savings.
//
// Every response receives Vary: Accept-Encoding so intermediate caches and
// CDN proxies never serve a compressed response to a client that did not
// advertise gzip support.
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")

		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			
			return
		}

		bw := &bufferingWriter{ResponseWriter: w}
		next.ServeHTTP(bw, r)
		bw.flush(w)
	})
}

// bufferingWriter captures the response body written by a handler so that
// gzipHandler can inspect the final size before deciding whether to compress.
type bufferingWriter struct {
	http.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (bw *bufferingWriter) WriteHeader(status int) {
	// Do not forward yet — Content-Encoding must be set before the status
	// line and headers are sent to the client.
	bw.status = status
}

func (bw *bufferingWriter) Write(p []byte) (int, error) {
	return bw.buf.Write(p)
}

// flush writes the buffered response to w, compressing if the body is large
// enough to make compression worthwhile.
func (bw *bufferingWriter) flush(w http.ResponseWriter) {
	body := bw.buf.Bytes()

	status := bw.status
	if status == 0 {
		status = http.StatusOK
	}

	if len(body) >= gzipThreshold {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length") // length changes after compression
		w.WriteHeader(status)

		gz := gzip.NewWriter(w)
		gz.Write(body) //nolint:errcheck — write to in-memory buffer, error impossible
		gz.Close()     //nolint:errcheck

		return
	}

	w.WriteHeader(status)
	w.Write(body) //nolint:errcheck
}
