// Package server implements the HTTPS web server and all HTTP handler functions
// for idtrack. It exposes both the static single-page app (HTML/CSS/JS) and a
// JSON REST API consumed by that app.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/tucats/idtrack/db"
	"github.com/yuin/goldmark"
)

// contextKey is a private type used as the key when storing values in a
// request context. Using a named type (rather than a raw string) prevents
// accidental collisions with keys set by other packages that also use strings.
type contextKey string

// ctxUser is the specific key under which the authenticated *db.User is stored
// in the request context by the auth middleware.
const ctxUser contextKey = "user"

// srv holds the shared dependencies that all handler methods need. Attaching
// handlers as methods on a struct (rather than using global variables) keeps
// the code easy to test and avoids package-level state.
type srv struct {
	database        *sql.DB
	static          fs.FS
	version         string
	buildTime       string
	idleTimeout     int
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
func Start(database *sql.DB, port int, static fs.FS, version, buildTime string, idleTimeout int) error {
	s := &srv{database: database, static: static, version: version, buildTime: buildTime, idleTimeout: idleTimeout}

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
	return http.Serve(tlsLn, mux)
}

// auth is a middleware constructor. It returns a new http.Handler that:
//  1. Reads Basic Auth credentials from the request.
//  2. Looks up the user in the database and checks the password hash.
//  3. On success, stores the *db.User in the request context and calls next.
//  4. On failure, responds with 401 Unauthorized.
//
// The password stored in the database (and sent by the browser) is already a
// SHA-256 hex hash — we compare hashes directly, never plaintext passwords.
func (s *srv) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, hash, ok := r.BasicAuth()
		if !ok {
			jsonError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		username = strings.ToLower(username)

		user, err := db.FindUser(s.database, username)
		if err != nil || user == nil || user.PasswordHash != hash {
			jsonError(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		// context.WithValue returns a new context derived from the request's
		// context with the user embedded under the ctxUser key. We replace the
		// request's context so the handler receives it via r.Context().
		ctx := context.WithValue(r.Context(), ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// currentUser retrieves the *db.User stored by the auth middleware from the
// request context. It returns nil for unauthenticated requests (though in
// practice only authenticated handlers call this). The type assertion
// ".(type)" with a second "ok" return would panic without the comma-ok form;
// using the two-value form here makes nil the safe zero value on failure.
func currentUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(ctxUser).(*db.User)
	return u
}

// ── Static file handlers ──────────────────────────────────────────────────────

// serveRoot redirects bare "/" to the app page. Any other unrecognised path
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
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Write(data)
}

// ── Status / Onboarding ───────────────────────────────────────────────────────

func (s *srv) handleStatus(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := db.HasUsers(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if hasUsers {
		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"onboarding":   false,
			"idle_timeout": s.idleTimeout,
		})
		return
	}

	s.mu.Lock()
	if s.onboardingToken == "" {
		s.onboardingToken = uuid.New().String()
	}
	token := s.onboardingToken
	s.mu.Unlock()

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"onboarding":   true,
		"token":        token,
		"idle_timeout": s.idleTimeout,
	})
}

func (s *srv) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	marker, token, ok := r.BasicAuth()
	if !ok || marker != "onboarding" {
		jsonError(w, "invalid token", http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	valid := s.onboardingToken != "" && s.onboardingToken == token
	s.mu.Unlock()

	if !valid {
		jsonError(w, "invalid or expired onboarding token", http.StatusUnauthorized)
		return
	}

	hasUsers, err := db.HasUsers(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if hasUsers {
		s.mu.Lock()
		s.onboardingToken = ""
		s.mu.Unlock()
		jsonError(w, "onboarding already complete", http.StatusConflict)
		return
	}

	var body struct {
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		PasswordHash string `json:"password_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Username = strings.ToLower(strings.TrimSpace(body.Username))
	if body.Username == "" {
		jsonError(w, "username is required", http.StatusBadRequest)
		return
	}
	if body.PasswordHash == "" {
		jsonError(w, "password is required", http.StatusBadRequest)
		return
	}
	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" {
		displayName = body.Username
	}

	if err := db.AddUser(s.database, body.Username, displayName, body.PasswordHash, true); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.onboardingToken = ""
	s.mu.Unlock()

	db.RecordLogin(s.database, body.Username)

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"username":     body.Username,
		"display_name": displayName,
		"is_admin":     true,
	})
}

// ── Version ───────────────────────────────────────────────────────────────────

// handleVersion is a public (no-auth) endpoint that returns the server's
// version string and build timestamp. Useful for health checks and debugging.
func (s *srv) handleVersion(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{
		"version":    s.version,
		"build_time": s.buildTime,
	})
}

// ── Auth endpoint ─────────────────────────────────────────────────────────────

// handleLogin validates Basic Auth credentials, records the login timestamp,
// and returns the user's display name and admin flag so the browser can
// personalise the UI. It is the only endpoint that calls db.RecordLogin — we
// do not update last_login_at on every authenticated request to keep overhead low.
func (s *srv) handleLogin(w http.ResponseWriter, r *http.Request) {
	username, hash, ok := r.BasicAuth()
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	username = strings.ToLower(username)

	user, err := db.FindUser(s.database, username)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if user == nil || user.PasswordHash != hash {
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	db.RecordLogin(s.database, user.Username)

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"username":     user.Username,
		"display_name": user.DisplayName,
		"is_admin":     user.IsAdmin,
	})
}

// ── Projects ──────────────────────────────────────────────────────────────────

// handleListProjects returns all projects with their component lists. Available
// to all authenticated users (not admin-only) because the frontend needs the
// full project tree to populate dropdowns for every user.
func (s *srv) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := db.ListProjects(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"projects": projects})
}

// handleCreateProject creates a new project. Admin-only because project
// structure is considered global configuration that ordinary users shouldn't change.
func (s *srv) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := db.CreateProject(s.database, body.Name); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleCreateComponent adds a named component to an existing project.
// The project name is extracted from the URL path using r.PathValue(), which
// is Go 1.22's built-in way to access named path parameters (e.g. {project}).
func (s *srv) handleCreateComponent(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	project := r.PathValue("project")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := db.AddComponent(s.database, project, body.Name); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleDeleteProject removes a project and all its components. The db layer
// returns a 409-worthy error when issues still reference the project, which we
// surface to the client so the user knows which issues to reassign first.
func (s *srv) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	project := r.PathValue("project")
	if err := db.DeleteProject(s.database, project); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteComponent removes a single component from a project. Returns 409
// when issues still reference the project/component combination.
func (s *srv) handleDeleteComponent(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	project := r.PathValue("project")
	component := r.PathValue("component")
	if err := db.DeleteComponent(s.database, project, component); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── Users ─────────────────────────────────────────────────────────────────────

// handleCreateUser creates a new user account. Admin-only. Returns 409 Conflict
// if the username is already taken (unlike the CLI "--add" which upserts).
func (s *srv) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		PasswordHash string `json:"password_hash"` // pre-hashed by the browser
		IsAdmin      bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Username = strings.ToLower(strings.TrimSpace(body.Username))
	if body.Username == "" {
		jsonError(w, "username is required", http.StatusBadRequest)
		return
	}
	if body.PasswordHash == "" {
		jsonError(w, "password is required", http.StatusBadRequest)
		return
	}
	// Prevent duplicate usernames via the API (the DB layer's upsert is only
	// used by the CLI; the API enforces strict create semantics).
	existing, err := db.FindUser(s.database, body.Username)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		jsonError(w, "username already exists", http.StatusConflict)
		return
	}
	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" {
		displayName = body.Username // default display name to login name
	}
	if err := db.AddUser(s.database, body.Username, displayName, body.PasswordHash, body.IsAdmin); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]bool{"ok": true})
}

// handleUpdateUser modifies an existing user. Admin-only. The is_admin flag is
// always passed through even if the client didn't intend to change it, because
// the JSON body always includes a zero-value bool for unset fields. This is
// intentional — the admin UI always sends the current value.
func (s *srv) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	username := strings.ToLower(r.PathValue("username"))
	var body struct {
		DisplayName  string `json:"display_name"`
		PasswordHash string `json:"password_hash"`
		IsAdmin      bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	isAdmin := body.IsAdmin
	if err := db.UpdateUser(s.database, username, body.DisplayName, body.PasswordHash, &isAdmin); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteUser removes a user account. Admin-only. An admin cannot delete
// their own account to prevent accidentally locking everyone out.
func (s *srv) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	username := strings.ToLower(r.PathValue("username"))
	if username == currentUser(r).Username {
		jsonError(w, "cannot delete your own account", http.StatusBadRequest)
		return
	}
	if err := db.DeleteUser(s.database, username); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListUsers returns all users. Available to all authenticated users (not
// admin-only) because the frontend needs the user list to populate assignee
// dropdowns and resolve display names for all logged-in users.
func (s *srv) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := db.ListUsers(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"users": users})
}

// ── Issues list / create ──────────────────────────────────────────────────────

// handleListIssues reads optional query parameters and delegates filtering and
// sorting to db.ListIssues. All filtering is done in SQL rather than in Go to
// keep memory usage low for large issue lists.
func (s *srv) handleListIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	issues, err := db.ListIssues(
		s.database,
		q.Get("status"),
		q.Get("priority"),
		q.Get("search"),
		q.Get("sort"),
		q.Get("order"),
	)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issues": issues,
		"total":  len(issues), // included so the client can show a count without iterating
	})
}

// handleCreateIssue creates a new issue. The reporter is always set to the
// authenticated user's username — clients cannot spoof the reporter field.
func (s *srv) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Assignee    string `json:"assignee"`
		Project     string `json:"project"`
		Component   string `json:"component"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}

	reporter := currentUser(r).Username
	issue, err := db.CreateIssue(s.database, body.Title, body.Description, reporter, body.Assignee, body.Priority, body.Project, body.Component)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]interface{}{"issue": issue})
}

// ── Single issue: get / update / delete ───────────────────────────────────────

// handleGetIssue returns a single issue together with all of its comments in
// one response, so the frontend can display the full detail view without a
// second round-trip.
func (s *srv) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return // issueID already wrote the error response
	}

	issue, err := db.GetIssue(s.database, id)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}

	comments, err := db.ListComments(s.database, id)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"issue":    issue,
		"comments": comments,
	})
}

// handleUpdateIssue replaces all editable fields of an issue. All fields must
// be sent in the request body — this is a full replacement (PUT semantics), not
// a partial update (PATCH semantics).
func (s *srv) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Status      string `json:"status"`
		Assignee    string `json:"assignee"`
		Project     string `json:"project"`
		Component   string `json:"component"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}

	issue, err := db.UpdateIssue(s.database, id, body.Title, body.Description, body.Priority, body.Status, body.Assignee, body.Project, body.Component)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	if issue == nil {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"issue": issue})
}

// handleDeleteIssue permanently removes an issue and all its comments.
// Admin-only because deletions are irreversible.
func (s *srv) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	id, ok := issueID(w, r)
	if !ok {
		return
	}
	if err := db.DeleteIssue(s.database, id); err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── Comments ──────────────────────────────────────────────────────────────────

// handleCreateComment adds a comment to an existing issue. The author is always
// set to the authenticated user — clients cannot post as someone else.
func (s *srv) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
	}

	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		jsonError(w, "comment body is required", http.StatusBadRequest)
		return
	}

	author := currentUser(r).Username
	comment, err := db.CreateComment(s.database, id, author, body.Body)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]interface{}{"comment": comment})
}

// handleDeleteComment removes a single comment by its ID. Admin-only.
// The comment ID (cid) is a separate path parameter from the issue ID (id).
func (s *srv) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).IsAdmin {
		jsonError(w, "forbidden", http.StatusForbidden)
		return
	}
	raw := r.PathValue("cid")
	cid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cid <= 0 {
		jsonError(w, "invalid comment id", http.StatusBadRequest)
		return
	}
	if err := db.DeleteComment(s.database, cid); err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── Manual ────────────────────────────────────────────────────────────────────

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

// ── Helpers ───────────────────────────────────────────────────────────────────

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
