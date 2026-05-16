package server

import (
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

	"github.com/tucats/idtrack/db"
)

type contextKey string

const ctxUser contextKey = "user"

type srv struct {
	database *sql.DB
	static   fs.FS
}

func Start(database *sql.DB, port int, static fs.FS) error {
	s := &srv{database: database, static: static}

	mux := http.NewServeMux()

	// Static asset routes
	mux.HandleFunc("GET /idtrack", s.serveHTML)
	mux.HandleFunc("GET /assets/idtrack/idtrack.css", s.serveCSS)
	mux.HandleFunc("GET /assets/idtrack/idtrack.js", s.serveJS)
	mux.HandleFunc("GET /", s.serveRoot)

	// Auth endpoint (uses Basic auth to validate, no separate middleware wrapping)
	mux.HandleFunc("POST /api/login", s.handleLogin)

	// Authenticated API endpoints
	mux.Handle("GET /api/users", s.auth(http.HandlerFunc(s.handleListUsers)))

	mux.Handle("GET /api/issues", s.auth(http.HandlerFunc(s.handleListIssues)))
	mux.Handle("POST /api/issues", s.auth(http.HandlerFunc(s.handleCreateIssue)))
	mux.Handle("GET /api/issues/{id}", s.auth(http.HandlerFunc(s.handleGetIssue)))
	mux.Handle("PUT /api/issues/{id}", s.auth(http.HandlerFunc(s.handleUpdateIssue)))
	mux.Handle("DELETE /api/issues/{id}", s.auth(http.HandlerFunc(s.handleDeleteIssue)))
	mux.Handle("POST /api/issues/{id}/comments", s.auth(http.HandlerFunc(s.handleCreateComment)))

	certData, err := fs.ReadFile(static, "resources/https-server.crt")
	if err != nil {
		return fmt.Errorf("reading TLS cert: %w", err)
	}
	keyData, err := fs.ReadFile(static, "resources/https-server.key")
	if err != nil {
		return fmt.Errorf("reading TLS key: %w", err)
	}

	cert, err := tls.X509KeyPair(certData, keyData)
	if err != nil {
		return fmt.Errorf("loading TLS credentials: %w", err)
	}

	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	addr := fmt.Sprintf(":%d", port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	tlsLn := tls.NewListener(ln, tlsCfg)

	log.Printf("idtrack listening on https://localhost%s", addr)
	return http.Serve(tlsLn, mux)
}

// auth is the Basic-auth middleware. It validates credentials on every request
// and places the authenticated user in the request context.
func (s *srv) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, hash, ok := r.BasicAuth()
		if !ok {
			jsonError(w, "authentication required", http.StatusUnauthorized)
			return
		}

		user, err := db.FindUser(s.database, username)
		if err != nil || user == nil || user.PasswordHash != hash {
			jsonError(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func currentUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(ctxUser).(*db.User)
	return u
}

// ── Static file handlers ──────────────────────────────────────────────────────

func (s *srv) serveRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "" {
		http.Redirect(w, r, "/idtrack", http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

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

// ── Auth endpoint ─────────────────────────────────────────────────────────────

func (s *srv) handleLogin(w http.ResponseWriter, r *http.Request) {
	username, hash, ok := r.BasicAuth()
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

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

	jsonResponse(w, http.StatusOK, map[string]string{
		"username":     user.Username,
		"display_name": user.DisplayName,
	})
}

// ── Users ─────────────────────────────────────────────────────────────────────

func (s *srv) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := db.ListUsers(s.database)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"users": users})
}

// ── Issues list / create ──────────────────────────────────────────────────────

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
		"total":  len(issues),
	})
}

func (s *srv) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
		Assignee    string `json:"assignee"`
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
	issue, err := db.CreateIssue(s.database, body.Title, body.Description, reporter, body.Assignee, body.Priority)
	if err != nil {
		jsonError(w, "server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]interface{}{"issue": issue})
}

// ── Single issue: get / update / delete ───────────────────────────────────────

func (s *srv) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	id, ok := issueID(w, r)
	if !ok {
		return
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		jsonError(w, "title is required", http.StatusBadRequest)
		return
	}

	issue, err := db.UpdateIssue(s.database, id, body.Title, body.Description, body.Priority, body.Status, body.Assignee)
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

func (s *srv) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
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

// ── Helpers ───────────────────────────────────────────────────────────────────

func issueID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid issue id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func jsonResponse(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	jsonResponse(w, code, map[string]string{"error": msg})
}
