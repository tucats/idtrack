// This file serves idtrack's static, unauthenticated assets: the single-page
// app's HTML/CSS/JS, and the user manual rendered from Markdown. All of them
// are read from s.static, an fs.FS (filesystem interface) that in production
// is backed by a Go embed.FS. embed.FS comes from the standard library
// "embed" package: a "//go:embed resources" directive elsewhere in the
// codebase (main.go) tells the Go compiler to read the resources/ directory
// at build time and bake its contents directly into the compiled binary as
// a virtual, read-only filesystem. The practical effect is that deploying
// idtrack is copying one executable file — there is no separate step to
// upload HTML/CSS/JS/certificate files alongside it, and fs.ReadFile below
// reads from memory, not from disk.
package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// serveRoot redirects bare "/" to the app page. Any other unrecognized path
// returns 404 rather than silently serving the app, which avoids confusing the
// browser when a sub-path is requested.
func (s *srv) serveRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "" {
		http.Redirect(w, r, s.appPath(), http.StatusFound)

		return
	}

	http.NotFound(w, r)
}

// serveHTML reads the single HTML file from the embedded filesystem and writes
// it to the response. All three static handlers (HTML/CSS/JS) follow the same
// pattern: read from embedded FS, set the correct Content-Type, write the bytes.
//
// The embedded HTML hardcodes its CSS/JS/manual links as absolute root paths
// (e.g. href="/assets/idtrack/idtrack.css") because that path is baked into
// the binary at compile time, long before a base path is known. When a base
// path is configured, those three literal strings are rewritten here with the
// prefix spliced in before the page is sent. When basePath is "" (the
// default) this branch is skipped entirely, so the response is byte-for-byte
// identical to before this feature existed.
func (s *srv) serveHTML(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.static, "resources/idtrack.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	if s.basePath != "" {
		html := string(data)
		html = strings.Replace(html, `href="/assets/idtrack/idtrack.css"`, `href="`+s.basePath+`/assets/idtrack/idtrack.css"`, 1)
		html = strings.Replace(html, `src="/assets/idtrack/idtrack.js"`, `src="`+s.basePath+`/assets/idtrack/idtrack.js"`, 1)
		html = strings.Replace(html, `href="/manual"`, `href="`+s.basePath+`/manual"`, 1)
		data = []byte(html)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// serveCSS serves the embedded stylesheet unchanged.
func (s *srv) serveCSS(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.static, "resources/idtrack.css")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(data)
}

// serveJS serves the embedded frontend script, minified on every request.
// Minify (see minify.go) strips comments and collapses whitespace so the
// browser downloads a smaller payload; shortenNames is passed false so the
// function/variable names visible in the browser's dev tools still match
// the source, which is useful when debugging the running app in production.
func (s *srv) serveJS(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.static, "resources/idtrack.js")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	// The embedded script declares "const BASE_PATH = '';" as a sentinel —
	// every API call in the frontend is prefixed with this constant, so
	// substituting its value here is what makes the whole app aware of a
	// configured base path at runtime, without a build step. Must happen
	// before Minify, which only guarantees byte-for-byte preservation of
	// existing string literals, not the literal source text this replace
	// matches against.
	if s.basePath != "" {
		data = bytes.Replace(data, []byte(`const BASE_PATH = '';`), []byte(`const BASE_PATH = '`+s.basePath+`';`), 1)
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

	// goldmark renders the Markdown source into HTML. WithAutoHeadingID generates
	// id attributes on heading elements so TOC anchor links work.
	var body bytes.Buffer

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	if err := md.Convert(src, &body); err != nil {
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
