package server

import (
	"encoding/json"
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

// jsonResponse sets the Content-Type header, writes the HTTP status code, and
// encodes v as JSON into the response body. All successful API responses go
// through this helper to ensure consistent formatting.
func jsonResponse(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code) // must be called after setting headers and before writing the body
	json.NewEncoder(w).Encode(v)
}

// jsonError sends a JSON body of the form {"error": "message"} with the given
// HTTP status code. All error responses go through this helper.
func jsonError(w http.ResponseWriter, msg string, code int) {
	jsonResponse(w, code, map[string]string{"error": msg})
}
