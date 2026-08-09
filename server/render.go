package server

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdRenderer is a package-level goldmark instance reused across requests —
// goldmark.Markdown is safe for concurrent use once configured, so there is
// no need to construct one per call. extension.GFM adds table, strikethrough,
// autolinking, and task-list support on top of goldmark's CommonMark-only
// default, matching the Github-flavored markdown authors of issue reports
// and comments already expect (e.g. "| col | col |" tables).
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderFormatted converts text to HTML according to format, for display in
// contexts (the issue description, comment bodies) that render per the
// issue's chosen format. "text" is returned unchanged — the frontend already
// escapes and preserves whitespace for plain text — so callers should treat
// an empty return as "no rendering needed" and fall back to their own
// escaping. "html" is passed through verbatim: choosing that format is an
// explicit, authenticated opt-in to trusting the content as-is, the same
// trust boundary as every other write in this single-tenant issue tracker.
func renderFormatted(format, text string) string {
	switch format {
	case "markdown":
		var buf bytes.Buffer
		if err := mdRenderer.Convert([]byte(text), &buf); err != nil {
			return ""
		}

		return buf.String()
	case "html":
		return text
	default:
		return ""
	}
}

// handleRenderPreview renders arbitrary text server-side for the live
// Edit/Preview toggle on the issue detail description field. Unlike
// handleGetIssue/handleUpdateIssue, the text here need not be persisted or
// even belong to an existing issue — it lets the frontend preview unsaved
// edits using the same goldmark renderer as the saved view, instead of
// duplicating markdown parsing in JavaScript.
func (s *srv) handleRenderPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Format string `json:"format"`
		Text   string `json:"text"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)

		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"html": renderFormatted(body.Format, body.Text)})
}
