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
	"os"
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
	sessions        *sessionStore
	mu              sync.Mutex
	backupMu        sync.RWMutex
	onboardingToken string
	statusHasUsers  bool          // cached result of db.HasUsers (S-09)
	statusCachedAt  time.Time     // zero value forces refresh on first call
	dbPath          string        // absolute path to the SQLite database file
	backupInterval  time.Duration // 0 = backups disabled
	backupCount     int           // 0 = no count limit
	backupAge       time.Duration // 0 = no age limit
	backupSize      int64         // 0 = no size limit
	certFile        string        // absolute path to TLS cert file; empty = use embedded cert
	keyFile         string        // absolute path to TLS key file; empty = use embedded key
}

// Start wires up all routes, loads the TLS certificate, opens a TCP listener,
// and begins serving HTTPS requests. It blocks until the server encounters a
// fatal error. All routes are registered on a fresh http.ServeMux so there is
// no shared global mux that could interfere with tests.
//
// Go 1.22+ route patterns support an HTTP method prefix, e.g. "GET /path".
// The mux dispatches based on both method and path, so registering
// "GET /api/issues" and "POST /api/issues" as separate patterns is fine.
func Start(database *sql.DB, port int, static fs.FS, version, buildTime string, idleTimeout int, appName, appDescription string, dbPath string, backupInterval time.Duration, backupCount int, backupAge time.Duration, backupSize int64, certFile, keyFile string) error {
	s := &srv{
		database:       database,
		static:         static,
		version:        version,
		buildTime:      buildTime,
		idleTimeout:    idleTimeout,
		appName:        appName,
		appDescription: appDescription,
		loginLimiter:   newRateLimiter(),
		sessions:       newSessionStore(),
		dbPath:         dbPath,
		backupInterval: backupInterval,
		backupCount:    backupCount,
		backupAge:      backupAge,
		backupSize:     backupSize,
		certFile:       certFile,
		keyFile:        keyFile,
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

	// Login / logout / onboarding are public endpoints that manage session cookies.
	// They do not go through the auth middleware — login and onboarding need to
	// run before a session exists; logout needs to run even when the session is
	// already expired. Routes that accept a JSON body are wrapped with requireJSON
	// (S-11); logout has no body so it is excluded.
	mux.Handle("POST /api/login", requireJSON(http.HandlerFunc(s.handleLogin)))
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.Handle("POST /api/onboarding", requireJSON(http.HandlerFunc(s.handleOnboarding)))

	// Authenticated API endpoints are wrapped with s.auth(), which validates the
	// session cookie on every request and stores the *db.User in the context.
	// JSON-body endpoints are additionally wrapped with requireJSON (S-11).
	mux.Handle("GET /api/users", s.auth(http.HandlerFunc(s.handleListUsers)))
	mux.Handle("POST /api/users", s.auth(requireJSON(http.HandlerFunc(s.handleCreateUser))))
	mux.Handle("PUT /api/users/{username}", s.auth(requireJSON(http.HandlerFunc(s.handleUpdateUser))))
	mux.Handle("DELETE /api/users/{username}", s.auth(http.HandlerFunc(s.handleDeleteUser)))
	mux.Handle("GET /api/projects", s.auth(http.HandlerFunc(s.handleListProjects)))
	mux.Handle("POST /api/projects", s.auth(requireJSON(http.HandlerFunc(s.handleCreateProject))))
	mux.Handle("POST /api/projects/{project}/components", s.auth(requireJSON(http.HandlerFunc(s.handleCreateComponent))))
	mux.Handle("DELETE /api/projects/{project}", s.auth(http.HandlerFunc(s.handleDeleteProject)))
	mux.Handle("DELETE /api/projects/{project}/components/{component}", s.auth(http.HandlerFunc(s.handleDeleteComponent)))

	mux.Handle("GET /api/issues", s.auth(http.HandlerFunc(s.handleListIssues)))
	// /changes must be registered before /{id} so the literal path takes
	// priority over the wildcard. (Go 1.22+ routing always prefers literals,
	// but explicit ordering makes the intent clear.)
	mux.Handle("GET /api/issues/changes", s.auth(http.HandlerFunc(s.handleListChanges)))
	mux.Handle("POST /api/issues", s.auth(requireJSON(http.HandlerFunc(s.handleCreateIssue))))
	mux.Handle("GET /api/issues/{id}", s.auth(http.HandlerFunc(s.handleGetIssue)))
	mux.Handle("PUT /api/issues/{id}", s.auth(requireJSON(http.HandlerFunc(s.handleUpdateIssue))))
	mux.Handle("DELETE /api/issues/{id}", s.auth(http.HandlerFunc(s.handleDeleteIssue)))
	mux.Handle("POST /api/issues/{id}/comments", s.auth(requireJSON(http.HandlerFunc(s.handleCreateComment))))
	mux.Handle("DELETE /api/issues/{id}/comments/{cid}", s.auth(http.HandlerFunc(s.handleDeleteComment)))

	// Read the TLS certificate and key. If a file name was provided from the defaults
	// data, use that as the name. Otherwise, read the values from the embedded filesystem.
	var (
		certData []byte
		keyData  []byte
		err      error
	)

	if certFile != "" {
		certData, err = os.ReadFile(certFile)
		if err != nil {
			return fmt.Errorf("reading TLS cert: %w", err)
		}

		log.Printf("idtrack using cert file: %s", certFile)
	} else {
		certData, err = fs.ReadFile(static, "resources/https-server.crt")
		if err != nil {
			return fmt.Errorf("reading TLS cert: %w", err)
		}
	}

	if keyFile != "" {
		keyData, err = os.ReadFile(keyFile)
		if err != nil {
			return fmt.Errorf("reading TLS key: %w", err)
		}

		log.Printf("idtrack using key file: %s", keyFile)
	} else {
		keyData, err = fs.ReadFile(static, "resources/https-server.key")
		if err != nil {
			return fmt.Errorf("reading TLS key: %w", err)
		}
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

	if s.backupInterval > 0 {
		s.startBackups()
	}

	log.Printf("idtrack listening on https://localhost%s", addr)

	// gzipHandler sits at the outermost layer so it can inspect and compress
	// any response — JSON API payloads, static assets, the manual page — before
	// it leaves the server. secureHeaders and limitBody sit inside it so their
	// headers are set on the pre-compression ResponseWriter. quiesce holds a
	// read-lock on backupMu for each request so the backup goroutine can pause
	// the server briefly by acquiring the write lock.
	handler := gzipHandler(secureHeaders(limitBody(s.quiesce(mux))))

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
