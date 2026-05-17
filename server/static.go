package server

import (
	"bytes"
	"io/fs"
	"net/http"

	"github.com/yuin/goldmark"
)

// serveRoot redirects bare "/" to the app page. Any other unrecognized path
// returns 404 rather than silently serving the app, which avoids confusing the
// browser when a sub-path is requested.
func (s *srv) serveRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "" {
		http.Redirect(w, r, "/idtrack", http.StatusFound)

		return
	}

	http.NotFound(w, r)
}

// serveHTML reads the single HTML file from the embedded filesystem and writes
// it to the response. All three static handlers (HTML/CSS/JS) follow the same
// pattern: read from embedded FS, set the correct Content-Type, write the bytes.
func (s *srv) serveHTML(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.static, "resources/idtrack.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *srv) serveCSS(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.static, "resources/idtrack.css")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(data)
}

func (s *srv) serveJS(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.static, "resources/idtrack.js")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	// Let's minify this before sending along so the server browser
	// doesn't have to deal with comments, etc.
	data = Minify(data, false)

	// Send the data to the browser!
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(data)
}

// handleManual renders MANUAL.md from the embedded filesystem as a styled HTML
// page. It uses the goldmark library to convert Markdown to HTML, then wraps
// the result in a minimal HTML document with inline CSS for readability.
// Dark mode is supported via the CSS prefers-color-scheme media query.
func (s *srv) handleManual(w http.ResponseWriter, r *http.Request) {
	src, err := fs.ReadFile(s.static, "resources/MANUAL.md")
	if err != nil {
		http.Error(w, "manual not found", http.StatusNotFound)

		return
	}

	// goldmark.Convert renders the Markdown source into HTML, writing the
	// output into the bytes.Buffer. A bytes.Buffer satisfies io.Writer.
	var body bytes.Buffer

	if err := goldmark.Convert(src, &body); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)

		return
	}

	page := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>idtrack — User Manual</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
    max-width:860px;margin:2rem auto;padding:0 1.5rem;line-height:1.6;color:#222}
  h1{border-bottom:2px solid #0066cc;padding-bottom:.4rem;color:#0055aa}
  h2{border-bottom:1px solid #ccc;padding-bottom:.2rem;margin-top:2rem}
  h3{margin-top:1.5rem;color:#333}
  code{background:#f4f4f4;padding:.15em .3em;border-radius:3px;font-size:.9em}
  pre{background:#f4f4f4;padding:1rem;border-radius:4px;overflow-x:auto}
  pre code{background:none;padding:0}
  table{border-collapse:collapse;width:100%}
  th,td{border:1px solid #ccc;padding:.4rem .7rem;text-align:left}
  th{background:#f0f0f0}
  blockquote{border-left:4px solid #0066cc;margin:0;padding:.5rem 1rem;background:#f0f6ff;border-radius:0 4px 4px 0}
  a{color:#0066cc}
  @media(prefers-color-scheme:dark){
    body{background:#1a1a1a;color:#e8e8e8}
    h1{color:#66aaff;border-color:#66aaff}
    h2{border-color:#444}
    h3{color:#ccc}
    code,pre{background:#2a2a2a}
    th{background:#2a2a2a}
    th,td{border-color:#444}
    blockquote{background:#1a2a3a;border-color:#66aaff}
  }
</style>
</head>
<body>
` + body.String() + `
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(page))
}
