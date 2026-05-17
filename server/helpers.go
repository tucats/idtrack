package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
)

// issueID parses the {id} path parameter from the request URL. It writes a
// 400 Bad Request response and returns (0, false) if the value is missing,
// non-numeric, or not a positive integer. Callers should return immediately
// when ok is false — the error response has already been sent.
func issueID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid issue id", http.StatusBadRequest)

		return 0, false
	}

	return id, true
}

// jsonResponse sets the Content-Type and Cache-Control headers, writes the
// HTTP status code, and encodes v as JSON into the response body. All API
// responses go through this helper to ensure consistent formatting.
// Cache-Control: no-store prevents authenticated responses from being
// cached by browsers or intermediate proxies.
func jsonResponse(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code) // must be called after setting headers and before writing the body
	json.NewEncoder(w).Encode(v)
}

// jsonError sends a JSON body of the form {"error": "message"} with the given
// HTTP status code. All error responses go through this helper.
func jsonError(w http.ResponseWriter, msg string, code int) {
	jsonResponse(w, code, map[string]string{"error": msg})
}

// internalError logs the full error detail server-side (for debugging) and
// sends a generic "server error" response to the client. Use this whenever
// a raw database or internal error would otherwise be forwarded to the client,
// to avoid leaking schema names, file paths, or driver internals.
func internalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	jsonError(w, "server error", http.StatusInternalServerError)
}
