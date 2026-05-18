package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tucats/idtrack/db"
)

// ---------------------------------------------------------------------------
// handleVersion
// ---------------------------------------------------------------------------

func TestHandleVersion(t *testing.T) {
	s := newTestSrv(t)
	s.version = "1.2-3"
	s.buildTime = "20260101120000"

	r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	w := httptest.NewRecorder()

	s.handleVersion(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string

	json.NewDecoder(w.Body).Decode(&resp)

	if resp["version"] != "1.2-3" {
		t.Errorf("version: got %q, want %q", resp["version"], "1.2-3")
	}
}

// ---------------------------------------------------------------------------
// handleStatus
// ---------------------------------------------------------------------------

func TestHandleStatus_NoUsers(t *testing.T) {
	s := newTestSrv(t)

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()

	s.handleStatus(w, r)

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	if resp["onboarding"] != true {
		t.Errorf("expected onboarding=true when no users, got %v", resp["onboarding"])
	}

	if resp["token"] == nil {
		t.Error("expected token in onboarding response")
	}
}

func TestHandleStatus_WithUsers(t *testing.T) {
	s := newTestSrv(t)
	db.AddUser(s.database, "admin", "Admin", "pw", true)

	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()

	s.handleStatus(w, r)

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	if resp["onboarding"] != false {
		t.Errorf("expected onboarding=false when users exist, got %v", resp["onboarding"])
	}
}

// ---------------------------------------------------------------------------
// handleLogin / handleLogout
// ---------------------------------------------------------------------------

func TestHandleLogin_Success(t *testing.T) {
	s := newTestSrv(t)
	db.AddUser(s.database, "alice", "Alice", "secret", false)

	body := `{"username":"alice","password":"secret"}`
	r := jsonReq(t, http.MethodPost, "/api/login", body, "")
	r.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()

	s.handleLogin(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	if resp["username"] != "alice" {
		t.Errorf("username: got %v, want alice", resp["username"])
	}

	// Check session cookie was set.
	found := false

	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName {
			found = true

			break
		}
	}

	if !found {
		t.Error("session cookie not set after login")
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	s := newTestSrv(t)
	db.AddUser(s.database, "bob", "Bob", "correct", false)

	body := `{"username":"bob","password":"wrong"}`
	r := jsonReq(t, http.MethodPost, "/api/login", body, "")
	r.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()

	s.handleLogin(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleLogin_UnknownUser(t *testing.T) {
	s := newTestSrv(t)

	body := `{"username":"nobody","password":"pw"}`
	r := jsonReq(t, http.MethodPost, "/api/login", body, "")
	r.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()

	s.handleLogin(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleLogin_RateLimited(t *testing.T) {
	s := newTestSrv(t)
	db.AddUser(s.database, "carol", "Carol", "pw", false)

	ip := "10.10.10.10"
	// Fill up the rate limiter.
	for i := 0; i < loginRateLimit; i++ {
		s.loginLimiter.recordFailure(ip)
	}

	body := `{"username":"carol","password":"pw"}`
	r := jsonReq(t, http.MethodPost, "/api/login", body, "")
	r.RemoteAddr = ip + ":9999"
	w := httptest.NewRecorder()

	s.handleLogin(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusTooManyRequests)
	}
}

func TestHandleLogout_ClearsCookie(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "dave", false)

	r := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})

	w := httptest.NewRecorder()

	s.handleLogout(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	// Session should be deleted.
	_, ok := s.sessions.lookup(token)
	if ok {
		t.Error("session should be deleted after logout")
	}
}

// ---------------------------------------------------------------------------
// handleOnboarding
// ---------------------------------------------------------------------------

func TestHandleOnboarding_Success(t *testing.T) {
	s := newTestSrv(t)

	// Get a fresh onboarding token from status.
	statusReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusRec := httptest.NewRecorder()
	s.handleStatus(statusRec, statusReq)

	var statusResp map[string]interface{}

	json.NewDecoder(statusRec.Body).Decode(&statusResp)
	onboardToken := statusResp["token"].(string)

	body := `{"username":"admin","password":"adminpw","display_name":"Admin"}`
	r := jsonReq(t, http.MethodPost, "/api/onboarding", body, "")
	r.SetBasicAuth("onboarding", onboardToken)

	w := httptest.NewRecorder()

	s.handleOnboarding(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestHandleOnboarding_WrongToken(t *testing.T) {
	s := newTestSrv(t)

	body := `{"username":"admin","password":"adminpw"}`
	r := jsonReq(t, http.MethodPost, "/api/onboarding", body, "")
	r.SetBasicAuth("onboarding", "bad-token")

	w := httptest.NewRecorder()

	s.handleOnboarding(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleOnboarding_AlreadyHasUsers(t *testing.T) {
	s := newTestSrv(t)

	// Set the token.
	s.mu.Lock()
	s.onboardingToken = "test-token"
	s.mu.Unlock()

	// Add an existing user.
	db.AddUser(s.database, "existing", "Existing", "pw", true)

	body := `{"username":"admin","password":"pw"}`
	r := jsonReq(t, http.MethodPost, "/api/onboarding", body, "")
	r.SetBasicAuth("onboarding", "test-token")

	w := httptest.NewRecorder()

	s.handleOnboarding(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusConflict)
	}
}

// ---------------------------------------------------------------------------
// handleListUsers
// ---------------------------------------------------------------------------

func TestHandleListUsers(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "bob", false)

	r := jsonReq(t, http.MethodGet, "/api/users", "", token)
	w := do(s, s.handleListUsers, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d", w.Code)
	}

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	users, ok := resp["users"].([]interface{})
	if !ok || len(users) != 2 {
		t.Errorf("expected 2 users, got %v", resp["users"])
	}
}

// ---------------------------------------------------------------------------
// handleCreateUser
// ---------------------------------------------------------------------------

func TestHandleCreateUser_AdminOnly(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "regular", false)

	body := `{"username":"newuser","password":"pw"}`
	r := jsonReq(t, http.MethodPost, "/api/users", body, token)
	w := do(s, s.handleCreateUser, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleCreateUser_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)

	body := `{"username":"newuser","password":"pw","display_name":"New User"}`
	r := jsonReq(t, http.MethodPost, "/api/users", body, token)
	w := do(s, s.handleCreateUser, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	u, _ := db.FindUser(s.database, "newuser")
	if u == nil {
		t.Error("user should be created")
	}
}

func TestHandleCreateUser_Duplicate(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "existing", false)

	body := `{"username":"existing","password":"pw"}`
	r := jsonReq(t, http.MethodPost, "/api/users", body, token)
	w := do(s, s.handleCreateUser, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestHandleCreateUser_MissingUsername(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)

	body := `{"username":"","password":"pw"}`
	r := jsonReq(t, http.MethodPost, "/api/users", body, token)
	w := do(s, s.handleCreateUser, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// handleDeleteUser
// ---------------------------------------------------------------------------

func TestHandleDeleteUser_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "victim", false)

	r := jsonReq(t, http.MethodDelete, "/api/users/victim", "", token)
	r.SetPathValue("username", "victim")
	w := do(s, s.handleDeleteUser, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleDeleteUser_SelfDeletion(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "other", true) // second admin so not last-admin

	r := jsonReq(t, http.MethodDelete, "/api/users/admin", "", token)
	r.SetPathValue("username", "admin")
	w := do(s, s.handleDeleteUser, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("self-delete: status got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleDeleteUser_LastAdmin(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "regular", false)

	r := jsonReq(t, http.MethodDelete, "/api/users/admin", "", token)
	r.SetPathValue("username", "admin")
	w := do(s, s.handleDeleteUser, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("last-admin delete: status got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleDeleteUser_NonAdmin(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "regular", false)

	r := jsonReq(t, http.MethodDelete, "/api/users/someone", "", token)
	r.SetPathValue("username", "someone")
	w := do(s, s.handleDeleteUser, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusForbidden)
	}
}

// ---------------------------------------------------------------------------
// handleUpdateUser
// ---------------------------------------------------------------------------

func TestHandleUpdateUser_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "target", false)

	body := `{"display_name":"Updated Name","is_admin":false}`
	r := jsonReq(t, http.MethodPut, "/api/users/target", body, token)
	r.SetPathValue("username", "target")
	w := do(s, s.handleUpdateUser, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleUpdateUser_LastAdminDemotion(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)

	body := `{"display_name":"Admin","is_admin":false}`
	r := jsonReq(t, http.MethodPut, "/api/users/admin", body, token)
	r.SetPathValue("username", "admin")
	w := do(s, s.handleUpdateUser, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("last-admin demotion: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// handleCreateProject / handleListProjects
// ---------------------------------------------------------------------------

func TestHandleCreateProject_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)

	body := `{"name":"myproj"}`
	r := jsonReq(t, http.MethodPost, "/api/projects", body, token)
	w := do(s, s.handleCreateProject, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestHandleCreateProject_NonAdmin(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "regular", false)

	body := `{"name":"proj"}`
	r := jsonReq(t, http.MethodPost, "/api/projects", body, token)
	w := do(s, s.handleCreateProject, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleListProjects(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	db.CreateProject(s.database, "alpha")
	db.CreateProject(s.database, "beta")

	r := jsonReq(t, http.MethodGet, "/api/projects", "", token)
	w := do(s, s.handleListProjects, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d", w.Code)
	}

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	projects, _ := resp["projects"].([]interface{})
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

// ---------------------------------------------------------------------------
// handleCreateComponent
// ---------------------------------------------------------------------------

func TestHandleCreateComponent_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	db.CreateProject(s.database, "myproj")

	body := `{"name":"api"}`
	r := jsonReq(t, http.MethodPost, "/api/projects/myproj/components", body, token)
	r.SetPathValue("project", "myproj")
	w := do(s, s.handleCreateComponent, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleDeleteProject
// ---------------------------------------------------------------------------

func TestHandleDeleteProject(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	db.CreateProject(s.database, "doomedproj")

	r := jsonReq(t, http.MethodDelete, "/api/projects/doomedproj", "", token)
	r.SetPathValue("project", "doomedproj")
	w := do(s, s.handleDeleteProject, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleDeleteProject_Referenced(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	db.CreateProject(s.database, "usedproj")
	db.CreateIssue(s.database, "T", "", "admin", "", "Medium", "usedproj", "c")

	r := jsonReq(t, http.MethodDelete, "/api/projects/usedproj", "", token)
	r.SetPathValue("project", "usedproj")
	w := do(s, s.handleDeleteProject, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusConflict)
	}
}

// ---------------------------------------------------------------------------
// Issue handlers
// ---------------------------------------------------------------------------

func TestHandleCreateIssue(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	db.CreateProject(s.database, "p")
	db.AddComponent(s.database, "p", "c")

	body := `{"title":"My Issue","description":"","priority":"High","project":"p","component":"c"}`
	r := jsonReq(t, http.MethodPost, "/api/issues", body, token)
	w := do(s, s.handleCreateIssue, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp map[string]interface{}
	
	json.NewDecoder(w.Body).Decode(&resp)

	issue, _ := resp["issue"].(map[string]interface{})
	if issue == nil {
		t.Fatal("expected 'issue' key in response")
	}

	id, _ := issue["id"].(float64)
	if id == 0 {
		t.Error("expected non-zero issue ID")
	}

	if issue["reporter"] != "alice" {
		t.Errorf("reporter: got %v, want alice", issue["reporter"])
	}
}

func TestHandleGetIssue(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	issue, _ := db.CreateIssue(s.database, "Bug", "", "alice", "", "High", "p", "c")

	r := jsonReq(t, http.MethodGet, "/api/issues/1", "", token)
	r.SetPathValue("id", "1")
	w := do(s, s.handleGetIssue, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	issueObj, _ := resp["issue"].(map[string]interface{})
	if issueObj == nil {
		t.Fatal("expected 'issue' key in response")
	}

	id, _ := issueObj["id"].(float64)
	if int64(id) != issue.ID {
		t.Errorf("issue id: got %v, want %d", issueObj["id"], issue.ID)
	}
}

func TestHandleGetIssue_NotFound(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	r := jsonReq(t, http.MethodGet, "/api/issues/999", "", token)
	r.SetPathValue("id", "999")
	w := do(s, s.handleGetIssue, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateIssue_Forbidden(t *testing.T) {
	s := newTestSrv(t)
	addTestUser(t, s, "alice", false)
	token := addTestUser(t, s, "bob", false)
	db.CreateIssue(s.database, "T", "", "alice", "", "Medium", "p", "c")

	body := `{"title":"Changed","priority":"High","status":"Open","project":"p","component":"c"}`
	r := jsonReq(t, http.MethodPut, "/api/issues/1", body, token)
	r.SetPathValue("id", "1")
	w := do(s, s.handleUpdateIssue, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("third-party update: got %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleUpdateIssue_Reporter(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	issue, _ := db.CreateIssue(s.database, "T", "", "alice", "", "Medium", "p", "c")

	body := `{"title":"Updated","priority":"High","status":"Open","project":"p","component":"c","description":""}`
	r := jsonReq(t, http.MethodPut, "/api/issues/1", body, token)
	r.SetPathValue("id", strings.TrimSpace(string(rune('0'+int(issue.ID)))))
	r.SetPathValue("id", "1")
	w := do(s, s.handleUpdateIssue, r)

	if w.Code != http.StatusOK {
		t.Errorf("reporter update: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleDeleteIssue_Admin(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	addTestUser(t, s, "alice", false)
	db.CreateIssue(s.database, "T", "", "alice", "", "Medium", "p", "c")

	r := jsonReq(t, http.MethodDelete, "/api/issues/1", "", token)
	r.SetPathValue("id", "1")
	w := do(s, s.handleDeleteIssue, r)

	if w.Code != http.StatusOK {
		t.Errorf("admin delete: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleListIssues(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	db.CreateIssue(s.database, "A", "", "alice", "", "High", "p", "c")
	db.CreateIssue(s.database, "B", "", "alice", "", "Low", "p", "c")

	r := jsonReq(t, http.MethodGet, "/api/issues", "", token)
	w := do(s, s.handleListIssues, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d", w.Code)
	}

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	issues, _ := resp["issues"].([]interface{})
	if len(issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(issues))
	}
}

func TestHandleListIssues_SearchTooLong(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	long := strings.Repeat("x", maxSearchLen+1)
	r := jsonReq(t, http.MethodGet, "/api/issues?search="+long, "", token)
	w := do(s, s.handleListIssues, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// handleListChanges
// ---------------------------------------------------------------------------

func TestHandleListChanges(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	db.CreateIssue(s.database, "T", "", "alice", "", "Medium", "p", "c")

	r := jsonReq(t, http.MethodGet, "/api/issues/changes?since=2000-01-01T00:00:00Z", "", token)
	w := do(s, s.handleListChanges, r)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d", w.Code)
	}

	var resp map[string]interface{}

	json.NewDecoder(w.Body).Decode(&resp)

	issues, _ := resp["issues"].([]interface{})
	if len(issues) != 1 {
		t.Errorf("expected 1 change, got %d", len(issues))
	}
}

// ---------------------------------------------------------------------------
// Comment handlers
// ---------------------------------------------------------------------------

func TestHandleCreateComment_Success(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)
	db.CreateIssue(s.database, "T", "", "alice", "", "Medium", "p", "c")

	body := `{"body":"this is a comment"}`
	r := jsonReq(t, http.MethodPost, "/api/issues/1/comments", body, token)
	r.SetPathValue("id", "1")
	w := do(s, s.handleCreateComment, r)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d, want %d: %s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestHandleCreateComment_NotFound(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "alice", false)

	body := `{"body":"orphan comment"}`
	r := jsonReq(t, http.MethodPost, "/api/issues/999/comments", body, token)
	r.SetPathValue("id", "999")
	w := do(s, s.handleCreateComment, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteComment_Admin(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "admin", true)
	issue, _ := db.CreateIssue(s.database, "T", "", "admin", "", "Medium", "p", "c")
	comment, _ := db.CreateComment(s.database, issue.ID, "admin", "text")

	r := jsonReq(t, http.MethodDelete, "/api/issues/1/comments/1", "", token)
	r.SetPathValue("id", "1")
	r.SetPathValue("cid", "1")
	w := do(s, s.handleDeleteComment, r)

	// Ensure we reference the right comment ID.
	_ = comment.ID

	if w.Code != http.StatusOK {
		t.Errorf("admin delete comment: got %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleDeleteComment_NonAdmin(t *testing.T) {
	s := newTestSrv(t)
	token := addTestUser(t, s, "regular", false)

	r := jsonReq(t, http.MethodDelete, "/api/issues/1/comments/1", "", token)
	r.SetPathValue("id", "1")
	r.SetPathValue("cid", "1")
	w := do(s, s.handleDeleteComment, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin delete comment: got %d, want %d", w.Code, http.StatusForbidden)
	}
}
