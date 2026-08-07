package server

import (
	"bytes"

	"github.com/yuin/goldmark"
)

// mdRenderer is a package-level goldmark instance reused across requests —
// goldmark.Markdown is safe for concurrent use once configured, so there is
// no need to construct one per call.
var mdRenderer = goldmark.New()

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
