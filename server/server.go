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

	"github.com/go-webauthn/webauthn/webauthn"
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
	database           *sql.DB
	static             fs.FS // embedded (or, in tests, on-disk) filesystem holding resources/*
	version            string
	buildTime          string
	idleTimeout        int
	appName            string
	appDescription     string
	loginLimiter       *rateLimiter
	sessions           *sessionStore
	mu                 sync.Mutex   // guards onboardingToken, statusHasUsers, and statusCachedAt below
	backupMu           sync.RWMutex // request-quiescing lock; see quiesce() and doBackup() in backup.go
	onboardingToken    string
	statusHasUsers     bool                   // cached result of db.HasUsers (S-09)
	statusCachedAt     time.Time              // zero value forces refresh on first call
	dbPath             string                 // absolute path to the SQLite database file
	backupInterval     time.Duration          // 0 = backups disabled
	backupCount        int                    // 0 = no count limit
	backupAge          time.Duration          // 0 = no age limit
	backupSize         int64                  // 0 = no size limit
	certFile           string                 // absolute path to TLS cert file; empty = use embedded cert
	keyFile            string                 // absolute path to TLS key file; empty = use embedded key
	insecure           bool                   // true = listen with plain HTTP, no TLS at all (e.g. behind a TLS-terminating reverse proxy)
	basePath           string                 // URL prefix every route is mounted under; "" = mounted at the origin root (see appPath)
	webauthnEnabled    bool                   // true = passkey login is turned on for this instance (see webauthn.go); gates both route registration below and the status response's webauthn_enabled field
	webauthn           *webauthn.WebAuthn     // nil unless webauthnEnabled; the go-webauthn library instance configured with the operator's RP ID/origin
	registerCeremonies *webauthnCeremonyStore // in-flight passkey-registration ceremonies, keyed by username; nil unless webauthnEnabled
	loginCeremonies    *webauthnCeremonyStore // in-flight passkey-login ceremonies, keyed by a random ceremony ID; nil unless webauthnEnabled
	apnsKeyPath        string                 // absolute path to the APNs .p8 auth key; "" = push notifications are off (see notify.go, added in a later phase)
	apnsKeyID          string                 // APNs auth key ID, from the Apple Developer portal
	apnsTeamID         string                 // Apple Developer Team ID
	apnsTopic          string                 // APNs topic, i.e. the app's bundle id (e.g. "com.tucats.idtrack")
	apnsSandbox        bool                   // true = talk to APNs' sandbox environment instead of production
}

// Config bundles every setting Start needs. Introduced when the parameter
// list this replaces reached 19 positional arguments (already unwieldy) and
// push notifications were about to add five more — a config struct scales to
// new settings without every future feature adding another position to a
// long, error-prone (all-same-type, order-dependent) call site. Field names
// mirror the srv struct fields they populate one-to-one; see the doc comments
// on those fields for what each one means.
type Config struct {
	Database         *sql.DB
	Port             int
	Static           fs.FS
	Version          string
	BuildTime        string
	IdleTimeout      int
	AppName          string
	AppDescription   string
	DBPath           string
	BackupInterval   time.Duration
	BackupCount      int
	BackupAge        time.Duration
	BackupSize       int64
	CertFile         string
	KeyFile          string
	Insecure         bool
	BasePath         string
	WebAuthnEnabled  bool
	WebAuthnRPID     string
	WebAuthnRPOrigin string
	ApnsKeyPath      string
	ApnsKeyID        string
	ApnsTeamID       string
	ApnsTopic        string
	ApnsSandbox      bool
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
func Start(cfg Config) error {
	s := &srv{
		database:        cfg.Database,
		static:          cfg.Static,
		version:         cfg.Version,
		buildTime:       cfg.BuildTime,
		idleTimeout:     cfg.IdleTimeout,
		appName:         cfg.AppName,
		appDescription:  cfg.AppDescription,
		loginLimiter:    newRateLimiter(),
		sessions:        newSessionStore(),
		dbPath:          cfg.DBPath,
		backupInterval:  cfg.BackupInterval,
		backupCount:     cfg.BackupCount,
		backupAge:       cfg.BackupAge,
		backupSize:      cfg.BackupSize,
		certFile:        cfg.CertFile,
		keyFile:         cfg.KeyFile,
		insecure:        cfg.Insecure,
		basePath:        cfg.BasePath,
		webauthnEnabled: cfg.WebAuthnEnabled,
		apnsKeyPath:     cfg.ApnsKeyPath,
		apnsKeyID:       cfg.ApnsKeyID,
		apnsTeamID:      cfg.ApnsTeamID,
		apnsTopic:       cfg.ApnsTopic,
		apnsSandbox:     cfg.ApnsSandbox,
	}

	// s.webauthn (and the two ceremony stores) are only constructed when the
	// feature is turned on. This means every /api/webauthn/* handler in
	// webauthn.go can assume s.webauthn is non-nil without a nil check —
	// they are simply never reachable otherwise, since the routes below are
	// only registered inside this same "if webauthnEnabled" branch.
	if cfg.WebAuthnEnabled {
		if cfg.WebAuthnRPID == "" || cfg.WebAuthnRPOrigin == "" {
			return fmt.Errorf("webauthn is enabled but rp-id/rp-origin are not both configured — see 'idtrack default --webauthn-rp-id' and '--webauthn-rp-origin'")
		}

		rpDisplayName := cfg.AppName
		if rpDisplayName == "" {
			rpDisplayName = "idtrack"
		}

		wa, err := webauthn.New(&webauthn.Config{
			RPID:          cfg.WebAuthnRPID,
			RPDisplayName: rpDisplayName,
			RPOrigins:     []string{cfg.WebAuthnRPOrigin},
		})
		if err != nil {
			return fmt.Errorf("configuring webauthn: %w", err)
		}

		s.webauthn = wa
		s.registerCeremonies = newWebAuthnCeremonyStore()
		s.loginCeremonies = newWebAuthnCeremonyStore()
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

	// Passkey (WebAuthn) routes exist on the mux at all only when the
	// operator has turned the feature on (see webauthnEnabled above) — a
	// request to any of these on a build/config where it is off gets
	// whatever the mux already returns for any other undefined path (404,
	// or 405 if a same-path-different-method catch-all like "GET /" exists
	// — see serveRoot below), identical to an idtrack version that predates
	// this feature entirely, rather than some bespoke "feature disabled"
	// response.
	// login/begin and /finish are public for the same reason /api/login is:
	// establishing a session is exactly what they are for, so no session can
	// be expected to exist yet. register/begin, /finish, the credential
	// list, and the delete route all require an existing session, because
	// registering or managing a passkey is something only an already-known
	// user can do to their own account (see webauthn.go for the full
	// request/response shapes).
	if cfg.WebAuthnEnabled {
		mux.HandleFunc(route("POST /api/webauthn/login/begin"), s.handleWebAuthnLoginBegin)
		mux.Handle(route("POST /api/webauthn/login/finish"), requireJSON(http.HandlerFunc(s.handleWebAuthnLoginFinish)))
		mux.Handle(route("POST /api/webauthn/register/begin"), s.auth(http.HandlerFunc(s.handleWebAuthnRegisterBegin)))
		mux.Handle(route("POST /api/webauthn/register/finish"), s.auth(requireJSON(http.HandlerFunc(s.handleWebAuthnRegisterFinish))))
		mux.Handle(route("GET /api/webauthn/credentials"), s.auth(http.HandlerFunc(s.handleWebAuthnListCredentials)))
		mux.Handle(route("DELETE /api/webauthn/credentials/{id}"), s.auth(http.HandlerFunc(s.handleWebAuthnDeleteCredential)))
	}

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

	// Image attachments (server/attachments.go). The two upload routes carry
	// a multipart file, not JSON, so they are wrapped with requireMultipart
	// instead of requireJSON; limitBody (server/middleware.go) also grants
	// them a much larger body-size ceiling than every other POST/PUT route,
	// keyed off the shared "/attachments" path suffix.
	mux.Handle(route("POST /api/issues/{id}/attachments"), s.auth(requireMultipart(http.HandlerFunc(s.handleCreateIssueAttachment))))
	mux.Handle(route("POST /api/issues/{id}/comments/{cid}/attachments"), s.auth(requireMultipart(http.HandlerFunc(s.handleCreateCommentAttachment))))
	mux.Handle(route("GET /api/issues/{id}/attachments"), s.auth(http.HandlerFunc(s.handleListAttachments)))
	mux.Handle(route("GET /api/attachments/{aid}"), s.auth(http.HandlerFunc(s.handleGetAttachmentImage)))
	mux.Handle(route("GET /api/attachments/{aid}/thumbnail"), s.auth(http.HandlerFunc(s.handleGetAttachmentThumbnail)))
	mux.Handle(route("DELETE /api/attachments/{aid}"), s.auth(http.HandlerFunc(s.handleDeleteAttachment)))

	// Push notifications (server/notifications.go, notify.go). Self-service,
	// like the WebAuthn credential routes above: every handler operates only
	// on currentUser(r), never a username from the path/body. Always
	// registered regardless of whether APNs is actually configured (see
	// s.apns in notify.go) — token/prefs bookkeeping is harmless even when
	// the server has no way to act on it yet, matching how the rest of the
	// API stays available even when a given feature's config is unset.
	mux.Handle(route("POST /api/notifications/token"), s.auth(requireJSON(http.HandlerFunc(s.handleRegisterNotificationToken))))
	mux.Handle(route("DELETE /api/notifications/token/{token}"), s.auth(http.HandlerFunc(s.handleUnregisterNotificationToken)))
	mux.Handle(route("GET /api/notifications/prefs"), s.auth(http.HandlerFunc(s.handleGetNotificationPrefs)))
	mux.Handle(route("PUT /api/notifications/prefs"), s.auth(requireJSON(http.HandlerFunc(s.handleUpdateNotificationPrefs))))
	mux.Handle(route("POST /api/notifications/badge/reset"), s.auth(requireJSON(http.HandlerFunc(s.handleResetNotificationBadge))))

	addr := fmt.Sprintf(":%d", cfg.Port)

	// Open a plain TCP listener first, then (unless running insecure) wrap it
	// with TLS. This two-step approach (rather than http.ListenAndServeTLS)
	// lets us get a nice error message if the port is already in use before we
	// try to start serving.
	rawLn, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	var ln net.Listener

	if cfg.Insecure {
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

		if cfg.CertFile != "" {
			certData, err = os.ReadFile(cfg.CertFile)
			if err != nil {
				return fmt.Errorf("reading TLS cert: %w", err)
			}

			log.Printf("idtrack using cert file: %s", cfg.CertFile)
		} else {
			certData, err = fs.ReadFile(cfg.Static, "resources/https-server.crt")
			if err != nil {
				return fmt.Errorf("reading TLS cert: %w", err)
			}
		}

		if cfg.KeyFile != "" {
			keyData, err = os.ReadFile(cfg.KeyFile)
			if err != nil {
				return fmt.Errorf("reading TLS key: %w", err)
			}

			log.Printf("idtrack using key file: %s", cfg.KeyFile)
		} else {
			keyData, err = fs.ReadFile(cfg.Static, "resources/https-server.key")
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
