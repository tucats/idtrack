// Package server implements the HTTPS web server and all HTTP handler functions
// for idtrack. It exposes both the static single-page app (HTML/CSS/JS) and a
// JSON REST API consumed by that app.
package server

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// srv holds the shared dependencies that all handler methods need. Attaching
// handlers as methods on a struct (rather than using global variables) keeps
// the code easy to test and avoids package-level state.
type srv struct {
	database        *sql.DB
	static          fs.FS
	version         string
	buildTime       string
	idleTimeout     int
	appName         string
	appDescription  string
	loginLimiter    *rateLimiter
	mu              sync.Mutex
	onboardingToken string
}

// Start wires up all routes, loads the TLS certificate, opens a TCP listener,
// and begins serving HTTPS requests. It blocks until the server encounters a
// fatal error. All routes are registered on a fresh http.ServeMux so there is
// no shared global mux that could interfere with tests.
//
// Go 1.22+ route patterns support an HTTP method prefix, e.g. "GET /path".
// The mux dispatches based on both method and path, so registering
// "GET /api/issues" and "POST /api/issues" as separate patterns is fine.
func Start(database *sql.DB, port int, static fs.FS, version, buildTime string, idleTimeout int, appName, appDescription string) error {
	s := &srv{
		database:       database,
		static:         static,
		version:        version,
		buildTime:      buildTime,
		idleTimeout:    idleTimeout,
		appName:        appName,
		appDescription: appDescription,
		loginLimiter:   newRateLimiter(),
	}

	mux := http.NewServeMux()

	// Static asset routes — no authentication required for the browser to load
	// the page and its assets.
	mux.HandleFunc("GET /idtrack", s.serveHTML)
	mux.HandleFunc("GET /assets/idtrack/idtrack.css", s.serveCSS)
	mux.HandleFunc("GET /assets/idtrack/idtrack.js", s.serveJS)
	mux.HandleFunc("GET /", s.serveRoot)

	// Public informational endpoints — no auth required
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /manual", s.handleManual)

	// Login validates credentials and records the login timestamp. It reads
	// Basic Auth credentials directly rather than going through the auth
	// middleware because the middleware would reject invalid credentials with
	// 401 before we could return a descriptive error.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/onboarding", s.handleOnboarding)

	// Authenticated API endpoints are wrapped with s.auth(), which is a
	// middleware function that validates Basic Auth on every request and stores
	// the authenticated user in the request context. Handlers use currentUser()
	// to retrieve that value.
	mux.Handle("GET /api/users", s.auth(http.HandlerFunc(s.handleListUsers)))
	mux.Handle("POST /api/users", s.auth(http.HandlerFunc(s.handleCreateUser)))
	mux.Handle("PUT /api/users/{username}", s.auth(http.HandlerFunc(s.handleUpdateUser)))
	mux.Handle("DELETE /api/users/{username}", s.auth(http.HandlerFunc(s.handleDeleteUser)))
	mux.Handle("GET /api/projects", s.auth(http.HandlerFunc(s.handleListProjects)))
	mux.Handle("POST /api/projects", s.auth(http.HandlerFunc(s.handleCreateProject)))
	mux.Handle("POST /api/projects/{project}/components", s.auth(http.HandlerFunc(s.handleCreateComponent)))
	mux.Handle("DELETE /api/projects/{project}", s.auth(http.HandlerFunc(s.handleDeleteProject)))
	mux.Handle("DELETE /api/projects/{project}/components/{component}", s.auth(http.HandlerFunc(s.handleDeleteComponent)))

	mux.Handle("GET /api/issues", s.auth(http.HandlerFunc(s.handleListIssues)))
	mux.Handle("POST /api/issues", s.auth(http.HandlerFunc(s.handleCreateIssue)))
	mux.Handle("GET /api/issues/{id}", s.auth(http.HandlerFunc(s.handleGetIssue)))
	mux.Handle("PUT /api/issues/{id}", s.auth(http.HandlerFunc(s.handleUpdateIssue)))
	mux.Handle("DELETE /api/issues/{id}", s.auth(http.HandlerFunc(s.handleDeleteIssue)))
	mux.Handle("POST /api/issues/{id}/comments", s.auth(http.HandlerFunc(s.handleCreateComment)))
	mux.Handle("DELETE /api/issues/{id}/comments/{cid}", s.auth(http.HandlerFunc(s.handleDeleteComment)))

	// Read the TLS certificate and key from the embedded filesystem. Both
	// files are compiled into the binary, so deployment is a single file copy.
	certData, err := fs.ReadFile(static, "resources/https-server.crt")
	if err != nil {
		return fmt.Errorf("reading TLS cert: %w", err)
	}

	keyData, err := fs.ReadFile(static, "resources/https-server.key")
	if err != nil {
		return fmt.Errorf("reading TLS key: %w", err)
	}

	// X509KeyPair parses the PEM-encoded certificate and key into a struct
	// that the TLS stack can use.
	cert, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return fmt.Errorf("loading TLS credentials: %w", err)
	}

	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	addr := fmt.Sprintf(":%d", port)

	// Open a plain TCP listener first, then wrap it with TLS. This two-step
	// approach (rather than http.ListenAndServeTLS) lets us get a nice error
	// message if the port is already in use before we try to start serving.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	tlsLn := tls.NewListener(ln, tlsCfg)

	log.Printf("idtrack listening on https://localhost%s", addr)

	// secureHeaders and limitBody wrap the entire mux so every response gets
	// security headers and every request body is capped before any handler runs.
	handler := secureHeaders(limitBody(mux))

	// Use an explicit http.Server so we can set read/write/idle timeouts.
	// Without timeouts, slow-loris clients can hold goroutines open indefinitely.
	httpSrv := &http.Server{
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return httpSrv.Serve(tlsLn)
}
