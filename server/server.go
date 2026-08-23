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
	"strings"
	"sync"
	"time"
)

// srv holds the shared dependencies that all handler methods need: the
// database handle, embedded static assets, in-memory session/rate-limit
// state, and server-wide configuration. Every HTTP handler in this package is
// declared as a method on *srv (e.g. "func (s *srv) handleLogin(...)")
// instead of a free function. This is a common Go pattern for giving request
// handlers access to shared state without resorting to package-level global
// variables: each handler receives its dependencies through the receiver "s"
// the same way it would through constructor-injected fields in other
// languages. It also makes the server trivially testable — a test can build
// its own *srv with a temporary database and in-memory session store instead
// of relying on global mutable state that would leak between test cases.
type srv struct {
	database        *sql.DB
	static          fs.FS // embedded (or, in tests, on-disk) filesystem holding resources/*
	version         string
	buildTime       string
	idleTimeout     int
	appName         string
	appDescription  string
	loginLimiter    *rateLimiter
	sessions        *sessionStore
	mu              sync.Mutex   // guards onboardingToken, statusHasUsers, and statusCachedAt below
	backupMu        sync.RWMutex // request-quiescing lock; see quiesce() and doBackup() in backup.go
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
	insecure        bool          // true = listen with plain HTTP, no TLS at all (e.g. behind a TLS-terminating reverse proxy)
	basePath        string        // URL prefix every route is mounted under; "" = mounted at the origin root (see appPath)
}

// appPath returns the path at which the single-page app itself is served.
// When basePath is unset this preserves the original hardcoded "/idtrack"
// path for backward compatibility. When basePath is set, the app is mounted
// exactly there (rather than at basePath+"/idtrack") so that, for example,
// configuring "/idtrack" as the base path makes the whole app — page,
// assets, and API — reachable under that single prefix, matching one nginx
// location block, instead of leaving the page at "/idtrack/idtrack".
func (s *srv) appPath() string {
	if s.basePath == "" {
		return "/idtrack"
	}

	return s.basePath
}

// Start wires up all routes, loads the TLS certificate, opens a TCP listener,
// and begins serving HTTPS requests. It blocks until the server encounters a
// fatal error. All routes are registered on a fresh http.ServeMux so there is
// no shared global mux that could interfere with tests.
//
// Go 1.22+ route patterns support an HTTP method prefix, e.g. "GET /path".
// The mux dispatches based on both method and path, so registering
// "GET /api/issues" and "POST /api/issues" as separate patterns is fine.
// The static parameter is typed as the fs.FS interface rather than the
// concrete embed.FS so this package never needs to import "embed" itself.
// In production main.go passes an embed.FS populated at compile time by a
// "//go:embed resources" directive — the contents of the resources/ directory
// (HTML, CSS, JS, the manual, and the fallback TLS cert/key) are compiled
// directly into the binary, so there is nothing to copy or mount at deploy
// time beyond the single executable. Because the parameter is an interface,
// tests can substitute any other fs.FS (such as os.DirFS) without changing
// this function.
func Start(database *sql.DB, port int, static fs.FS, version, buildTime string, idleTimeout int, appName, appDescription string, dbPath string, backupInterval time.Duration, backupCount int, backupAge time.Duration, backupSize int64, certFile, keyFile string, insecure bool, basePath string) error {
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
		insecure:       insecure,
		basePath:       basePath,
	}

	mux := http.NewServeMux()

	// route builds a mux pattern ("METHOD /path...") with s.basePath spliced
	// in between the method and the path, e.g. route("GET /api/version")
	// with basePath "/tracker" yields "GET /tracker/api/version". When
	// basePath is "" (the default) every pattern is byte-for-byte identical
	// to its unprefixed form, so this is a no-op for existing deployments.
	// It must NOT be used for the app page route itself — see appPath above,
	// which already resolves to basePath (not basePath+"/idtrack") once a
	// base path is configured.
	route := func(pattern string) string {
		method, path, _ := strings.Cut(pattern, " ")

		return method + " " + s.basePath + path
	}

	// Every pattern below is a Go 1.22+ "enhanced" ServeMux pattern: an HTTP
	// method prefix ("GET ", "POST ", ...) plus a path that may contain
	// "{name}" wildcard segments (e.g. "/api/issues/{id}"). Before Go 1.22 the
	// standard library mux only matched on path prefix and every handler had
	// to check r.Method and parse path segments itself (or projects reached
	// for a third-party router). Now the mux does both jobs: it dispatches to
	// the handler whose method and path both match, and a wildcard segment's
	// matched text is retrieved inside the handler via r.PathValue("id") (see
	// issueID in helpers.go for an example). No external routing package is
	// used anywhere in idtrack because of this.
	//
	// Static asset routes — no authentication required for the browser to load
	// the page and its assets. "GET /" is intentionally never prefixed with
	// basePath: it exists purely so a bare hit on the origin root redirects to
	// the app (see serveRoot), regardless of where the app itself is mounted.
	mux.HandleFunc("GET "+s.appPath(), s.serveHTML)
	mux.HandleFunc(route("GET /assets/idtrack/idtrack.css"), s.serveCSS)
	mux.HandleFunc(route("GET /assets/idtrack/idtrack.js"), s.serveJS)
	mux.HandleFunc("GET /", s.serveRoot)

	// Public informational endpoints — no auth required
	mux.HandleFunc(route("GET /api/version"), s.handleVersion)
	mux.HandleFunc(route("GET /api/status"), s.handleStatus)
	mux.HandleFunc(route("GET /manual"), s.handleManual)

	// Login / logout / onboarding are public endpoints that manage session cookies.
	// They do not go through the auth middleware — login and onboarding need to
	// run before a session exists; logout needs to run even when the session is
	// already expired. Routes that accept a JSON body are wrapped with requireJSON
	// (S-11); logout has no body so it is excluded.
	mux.Handle(route("POST /api/login"), requireJSON(http.HandlerFunc(s.handleLogin)))
	mux.HandleFunc(route("POST /api/logout"), s.handleLogout)
	mux.Handle(route("POST /api/onboarding"), requireJSON(http.HandlerFunc(s.handleOnboarding)))

	// Authenticated API endpoints are wrapped with s.auth(), which validates the
	// session cookie on every request and stores the *db.User in the context.
	// JSON-body endpoints are additionally wrapped with requireJSON (S-11).
	mux.Handle(route("GET /api/users"), s.auth(http.HandlerFunc(s.handleListUsers)))
	mux.Handle(route("POST /api/users"), s.auth(requireJSON(http.HandlerFunc(s.handleCreateUser))))
	mux.Handle(route("PUT /api/users/{username}"), s.auth(requireJSON(http.HandlerFunc(s.handleUpdateUser))))
	mux.Handle(route("DELETE /api/users/{username}"), s.auth(http.HandlerFunc(s.handleDeleteUser)))
	mux.Handle(route("GET /api/teams"), s.auth(http.HandlerFunc(s.handleListTeams)))
	mux.Handle(route("POST /api/teams"), s.auth(requireJSON(http.HandlerFunc(s.handleCreateTeam))))
	mux.Handle(route("DELETE /api/teams/{name}"), s.auth(http.HandlerFunc(s.handleDeleteTeam)))
	mux.Handle(route("PUT /api/teams/{name}"), s.auth(requireJSON(http.HandlerFunc(s.handleUpdateTeam))))

	mux.Handle(route("GET /api/projects"), s.auth(http.HandlerFunc(s.handleListProjects)))
	mux.Handle(route("POST /api/projects"), s.auth(requireJSON(http.HandlerFunc(s.handleCreateProject))))
	mux.Handle(route("PUT /api/projects/{project}/teams"), s.auth(requireJSON(http.HandlerFunc(s.handleUpdateProjectTeams))))
	mux.Handle(route("POST /api/projects/{project}/components"), s.auth(requireJSON(http.HandlerFunc(s.handleCreateComponent))))
	mux.Handle(route("DELETE /api/projects/{project}"), s.auth(http.HandlerFunc(s.handleDeleteProject)))
	mux.Handle(route("DELETE /api/projects/{project}/components/{component}"), s.auth(http.HandlerFunc(s.handleDeleteComponent)))

	mux.Handle(route("POST /api/render"), s.auth(requireJSON(http.HandlerFunc(s.handleRenderPreview))))

	mux.Handle(route("GET /api/issues"), s.auth(http.HandlerFunc(s.handleListIssues)))
	// /changes must be registered before /{id} so the literal path takes
	// priority over the wildcard. (Go 1.22+ routing always prefers literals,
	// but explicit ordering makes the intent clear.)
	mux.Handle(route("GET /api/issues/changes"), s.auth(http.HandlerFunc(s.handleListChanges)))
	mux.Handle(route("POST /api/issues"), s.auth(requireJSON(http.HandlerFunc(s.handleCreateIssue))))
	mux.Handle(route("GET /api/issues/{id}"), s.auth(http.HandlerFunc(s.handleGetIssue)))
	mux.Handle(route("PUT /api/issues/{id}"), s.auth(requireJSON(http.HandlerFunc(s.handleUpdateIssue))))
	mux.Handle(route("DELETE /api/issues/{id}"), s.auth(http.HandlerFunc(s.handleDeleteIssue)))
	mux.Handle(route("POST /api/issues/{id}/comments"), s.auth(requireJSON(http.HandlerFunc(s.handleCreateComment))))
	mux.Handle(route("DELETE /api/issues/{id}/comments/{cid}"), s.auth(http.HandlerFunc(s.handleDeleteComment)))

	addr := fmt.Sprintf(":%d", port)

	// Open a plain TCP listener first, then (unless running insecure) wrap it
	// with TLS. This two-step approach (rather than http.ListenAndServeTLS)
	// lets us get a nice error message if the port is already in use before we
	// try to start serving.
	rawLn, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	var ln net.Listener

	if insecure {
		// Plain HTTP: no cert/key needed at all. This mode is intended for
		// deployments where a reverse proxy (e.g. nginx) terminates TLS and
		// forwards plaintext HTTP to idtrack on a private network/loopback.
		ln = rawLn

		log.Printf("idtrack listening on http://localhost%s (insecure — no TLS; a reverse proxy is expected to provide TLS termination)", addr)
	} else {
		// Read the TLS certificate and key. If a file name was provided from
		// the defaults data, use that as the name. Otherwise, read the values
		// from the embedded filesystem.
		var certData, keyData []byte

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
		ln = tls.NewListener(rawLn, tlsCfg)

		log.Printf("idtrack listening on https://localhost%s", addr)
	}

	if s.backupInterval > 0 {
		s.startBackups()
	}

	// Each of gzipHandler, secureHeaders, limitBody, and s.quiesce is
	// "middleware": a function with the signature func(http.Handler) http.Handler
	// that takes a handler and returns a new handler wrapping it. A middleware
	// typically does some work, calls the wrapped ("inner") handler, and
	// optionally does more work with the result. Nesting them like this —
	// gzipHandler(secureHeaders(limitBody(s.quiesce(mux)))) — builds a chain
	// that every request passes through outside-in on the way to mux
	// (gzipHandler runs first, then secureHeaders, then limitBody, then
	// quiesce, then the matched route handler) and back outside-out on the
	// way to the client (the same order in reverse). This "decorator" style
	// composition is idiomatic Go for cross-cutting concerns — logging,
	// auth, compression — that many handlers need without each handler
	// having to remember to apply them itself.
	//
	// gzipHandler sits at the outermost layer so it can inspect and compress
	// any response — JSON API payloads, static assets, the manual page — before
	// it leaves the server. secureHeaders and limitBody sit inside it so their
	// headers are set on the pre-compression ResponseWriter. quiesce holds a
	// read-lock on backupMu for each request so the backup goroutine can pause
	// the server briefly by acquiring the write lock (see quiesce in backup.go
	// for the full reader/writer-lock explanation).
	handler := gzipHandler(secureHeaders(limitBody(s.quiesce(mux))))

	// Use an explicit http.Server so we can set read/write/idle timeouts.
	// Without timeouts, slow-loris clients can hold goroutines open indefinitely.
	httpSrv := &http.Server{
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return httpSrv.Serve(ln)
}
