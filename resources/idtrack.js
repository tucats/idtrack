'use strict';

// =====================================================================
// CONSTANTS
// =====================================================================
// These values never change after the page loads. Declaring them with
// 'const' (rather than 'let') communicates that intent and lets the
// browser warn us if we accidentally try to reassign them.
//
// All three storage keys are named constants so every piece of code
// that reads or writes browser storage uses the same string. A typo
// in any one place would cause a silent bug that's hard to diagnose.

// sessionStorage key — holds { user } for the life of the browser tab.
// Cleared automatically when the tab is closed.
const SESSION_KEY = 'idtrack_session';

// localStorage key — holds user preferences (dark mode, keep-me-logged-in,
// desktop mode). Persists indefinitely across browser sessions.
const PREFS_KEY   = 'idtrack_prefs';

// localStorage key — holds { user } (display object only, no password)
// when the user has "Keep me logged in" enabled. Used to restore the
// display state on the next browser session; the actual auth credential
// is the server-issued session cookie.
const PERSIST_KEY = 'idtrack_persist';

const APP_VERSION = '2.0';

// =====================================================================
// STATE
// =====================================================================
// Because this is a single-page app with no framework, all shared state
// lives here as module-level variables. Functions read and write these
// directly, then call a render function to update the visible page.
//
// The leading underscore (_) is a naming convention that signals
// "this is module-private — don't reach in from outside this file".

// The currently logged-in user. null means no one is logged in.
// Shape: { username, display_name, is_admin }
let _currentUser       = null;

// A lookup table mapping username → display_name, built from GET /api/users
// after login. Used by displayName() to show "Alice Smith" instead of "smith".
let _userMap           = {};

// The full list of user objects from GET /api/users. Needed by the
// Edit User form so it can pre-populate fields when a user is selected.
let _userList          = [];

// All projects and their component lists, fetched from GET /api/projects.
// Shape: [{ name: "Backend", components: ["API", "DB"] }, ...]
let _projectData       = [];

// The project currently open in the Edit Projects detail screen.
// null means we are in "new project" mode.
let _epProject          = null;

// Components staged for a new project before the project itself is saved.
// When the user clicks "Create Project" these are POSTed one by one.
let _epPendingComponents = [];

// Paginated issue window — replaces the old _allIssues flat array.
//
// Rather than loading every issue at once, we fetch one page at a time and
// accumulate results in _issueWindow as the user scrolls down.
//
// _issueWindow  — the issues fetched so far for the current filter/sort.
//                 Grows page-by-page; never trimmed (append-only scroll model).
// _totalIssues  — the server-reported count of ALL matching rows (not just the
//                 loaded ones). Used to know when there are no more pages to fetch.
// _lastSeenAt   — the maximum updated_at timestamp seen in _issueWindow. Sent to
//                 the server as a cursor when polling: "give me anything newer than this."
// _fetchGen     — "generation counter" to prevent stale responses from landing.
//                 Each call to loadIssueWindow() increments this before the fetch.
//                 When the response arrives we check if the counter still matches;
//                 if the user changed a filter while the request was in flight the
//                 counter will have advanced and we simply discard the old result.
// _fetchLock    — prevents loadNextPage() from launching two page-fetches at once.
//                 The IntersectionObserver can fire multiple times before the first
//                 response arrives; the lock ensures only one is in flight.
// _searchTimer  — setTimeout handle used to debounce the search field: we wait
//                 300 ms after the last keystroke before sending a server request.
// _scrollObserver — the IntersectionObserver that watches the invisible sentinel
//                 element at the bottom of the list and triggers loadNextPage().
// _pollTimer    — handle for the 30-second setInterval background polling loop.
// _refreshHintOn — true while the "new issues available" toast is visible.
let _issueWindow    = [];
let _totalIssues    = 0;
let _pageSize       = 50;      // from prefs; changed via Settings
let _lastSeenAt     = '';
let _fetchGen       = 0;
let _fetchLock      = false;
let _searchTimer    = null;
let _scrollObserver = null;
let _pollTimer      = null;
let _refreshHintOn  = false;

// The id of the issue currently open in the detail panel. null = none open.
let _currentId         = null;

// Current sort state for the issue table.
let _sortCol     = 'id';    // field name that matches issue object keys
let _sortAsc     = false;   // true = A→Z / low→high, false = Z→A / high→low

// Current filter state. 'all' means no filter applied for that dimension.
let _statusFilter   = 'open';
let _priorityFilter = 'all';
let _projectFilter  = 'all';

// true when any field in the detail panel has been changed but not yet saved.
// Controls whether the "Save Changes" button is visible and whether the user
// is warned before discarding changes.
let _detailDirty    = false;

// The status value of the current issue as it was when last loaded or saved.
// Comparing the current select value to this lets us detect Open→Resolved
// and Resolved→Open transitions, which require a confirmation dialog.
let _originalStatus = 'Open';

// Holds all the form field values captured just before a status-change dialog
// opens. Cleared on dialog confirm or cancel. null = no dialog is in progress.
let _pendingStatusData = null;

// User preference flags. Loaded from localStorage at startup by loadPrefs().
let _darkMode       = false;  // body.dark CSS class active
let _keepLoggedIn   = false;  // request 30-day session cookie on next login
let _desktopMode    = false;  // html.desktop-mode class active (disables RWD CSS)

// Idle-logout state. The timeout value comes from GET /api/status.
// 0 means the feature is disabled.
let _idleTimeoutSecs = 0;
let _idleTimer       = null;  // handle returned by setTimeout(), kept so we can cancel it

// Application branding. These defaults are overridden by GET /api/status
// if the operator has set custom values via 'idtrack default --app-name'.
let _appName         = 'idtrack';
let _appDesc         = 'Issue Tracker';

// =====================================================================
// BRANDING
// =====================================================================

// applyBranding() updates every place in the UI that shows the application
// name or description. It is called once during init() after GET /api/status
// returns, and again never — the values don't change while the page is open.
//
// The inner setText helper silently skips elements that don't exist yet,
// which makes it safe to call before the app shell is visible.
function applyBranding() {
    document.title = _appName + ' — ' + _appDesc;
    const setText = (id, text) => { const el = document.getElementById(id); if (el) el.textContent = text; };
    setText('header-app-name', _appName);
    setText('login-app-name', _appName);
    setText('login-app-desc', _appDesc);
    setText('ob-app-name', 'Welcome to ' + _appName);
    setText('about-app-name', _appName);
    setText('about-app-desc', _appDesc);
}

// =====================================================================
// UTILITY
// =====================================================================

// esc() turns a value into a safe HTML string by escaping the five
// characters that have special meaning in HTML: & < > " '
//
// Call this on EVERY piece of user-supplied data before inserting it
// into innerHTML. Skipping this step would let a user whose display
// name is "<script>..." inject arbitrary code into every page that
// shows their name — a classic cross-site scripting (XSS) attack.
function esc(s) {
    return String(s == null ? '' : s)
        .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
        .replace(/"/g,'&quot;');
}

// fmtDate turns an ISO 8601 date string (e.g. "2025-05-15T14:32:00Z")
// into a short, locale-appropriate date like "May 15, 2025".
// Passing 'undefined' as the locale uses the user's browser locale, so
// the format automatically matches their regional convention.
function fmtDate(iso) {
    if (!iso) return '';
    try {
        const d = new Date(iso);
        return d.toLocaleDateString(undefined, { year:'numeric', month:'short', day:'numeric' });
    } catch { return iso; }
}

// fmtDateTime is like fmtDate but also includes the time of day ("14:32").
// Used for Created/Updated timestamps in the detail panel and for comment
// timestamps, where knowing the exact time matters.
function fmtDateTime(iso) {
    if (!iso) return '';
    try {
        const d = new Date(iso);
        return d.toLocaleDateString(undefined, {year:'numeric',month:'short',day:'numeric'})
             + ' ' + d.toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit'});
    } catch { return iso; }
}

// priorityBadge returns an HTML string for a colored pill that shows
// the priority level (High / Medium / Low). The colors come from CSS
// classes in idtrack.css. This string must be inserted via innerHTML,
// not textContent, because it contains HTML tags.
function priorityBadge(p) {
    const cls = {High:'badge-high', Medium:'badge-medium', Low:'badge-low'}[p] || 'badge-low';
    return `<span class="badge ${cls}">${esc(p)}</span>`;
}

// statusBadge works exactly like priorityBadge but for issue status
// (Open / Resolved).
function statusBadge(s) {
    const cls = s === 'Open' ? 'badge-open' : 'badge-resolved';
    return `<span class="badge ${cls}">${esc(s)}</span>`;
}

// displayName looks up the human-friendly display name for a username.
// Issue records store the short login name (e.g. "smith") rather than
// the display name ("Alice Smith") to keep foreign-key relationships
// simple. The _userMap, built by populateAssigneeDropdowns() after login,
// provides the reverse mapping. Falls back to the raw username if the
// user has been deleted or the map hasn't been populated yet.
function displayName(username) {
    return _userMap[username] || username;
}

// canModifyIssue returns true when the currently logged-in user is
// permitted to edit or delete the given issue. The rule is: admins,
// the original reporter, and the currently assigned user may modify an
// issue; everyone else is read-only (but can still add comments).
//
// This check mirrors the server-side rule in server/issues.go. Checking
// client-side lets us hide the Save/Delete buttons from users who would
// receive a 403 anyway, keeping the UI uncluttered.
function canModifyIssue(issue) {
    if (!_currentUser || !issue) return false;
    return _currentUser.is_admin
        || _currentUser.username === issue.reporter
        || _currentUser.username === issue.assignee;
}

// =====================================================================
// API LAYER
// =====================================================================
// These five functions are the only places in this file that call
// fetch(). All other code goes through one of these wrappers.
//
// Why wrappers? Two reasons:
//
// 1. Central 401 handling — if any request returns "Unauthorized" it
//    means the session has expired. We immediately clear client state
//    and redirect to the login screen so every caller gets this
//    behavior for free without writing it themselves.
//
// 2. Consistent error surfacing — non-OK responses are turned into a
//    thrown Error whose message is the server's error string (if the
//    response body contains one). Callers can display it in the UI
//    with a single try/catch block.
//
// The 'async' keyword means the function always returns a Promise.
// Inside an async function, 'await' pauses execution until the Promise
// resolves, making asynchronous code read like normal sequential code.

// apiFetch is the lowest-level wrapper. It calls the browser's built-in
// fetch() with whatever options the caller provides, intercepts 401s,
// and returns the raw Response object for higher-level wrappers to parse.
async function apiFetch(url, options = {}) {
    const res = await fetch(url, options);

    if (res.status === 401) {
        // Session expired or never started — wipe all client-side auth
        // state and redirect to login before this function returns.
        _currentUser = null;
        sessionStorage.removeItem(SESSION_KEY);
        localStorage.removeItem(PERSIST_KEY);
        showLogin('Session expired. Please sign in again.');
        // Throwing stops execution in the caller's try block and jumps
        // to its catch block, preventing it from trying to use a response
        // that won't have the expected data shape.
        throw new Error('Unauthorized');
    }
    return res;
}

// apiGet performs a GET request and parses the JSON response body.
// Throws an Error with the server's message if the status is not 2xx.
async function apiGet(url) {
    const res = await apiFetch(url);
    if (!res.ok) {
        let msg = `Error ${res.status}`;
        try { const d = await res.json(); msg = d.error || msg; } catch {}
        throw new Error(msg);
    }
    return res.json();
}

// apiPost sends a POST request with a JSON body and returns the parsed
// response. The Content-Type header tells the server to expect JSON
// (the server's requireJSON middleware enforces this).
async function apiPost(url, body) {
    const res = await apiFetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) {
        let msg = `Error ${res.status}`;
        try { const d = await res.json(); msg = d.error || msg; } catch {}
        throw new Error(msg);
    }
    return res.json();
}

// apiPut is identical to apiPost but uses the PUT method. By convention,
// POST creates a new resource and PUT replaces an existing one.
async function apiPut(url, body) {
    const res = await apiFetch(url, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) {
        let msg = `Error ${res.status}`;
        try { const d = await res.json(); msg = d.error || msg; } catch {}
        throw new Error(msg);
    }
    return res.json();
}

// apiDelete sends a DELETE request. No body is needed; the resource is
// identified by the URL path alone.
async function apiDelete(url) {
    const res = await apiFetch(url, { method: 'DELETE' });
    if (!res.ok) {
        let msg = `Error ${res.status}`;
        try { const d = await res.json(); msg = d.error || msg; } catch {}
        throw new Error(msg);
    }
    return res.json();
}

// =====================================================================
// DOMAIN CALLS
// =====================================================================
// These functions sit one level above the raw API layer. Each one maps
// to a specific server endpoint and knows what data shape to expect.
// The rest of the UI code calls these by name rather than thinking
// about URLs and response shapes directly.

// GET /api/users
// Returns all user accounts. Used to build _userMap / _userList and to
// populate assignee dropdowns.
// Response shape: { users: [{ username, display_name, is_admin, last_login_at }, ...] }
async function fetchUsers() {
    const data = await apiGet('/api/users');
    return data.users || [];
}

// GET /api/issues
// fetchIssuePage fetches one page of issues from the server using the current
// filter/sort state. Returns { issues, total } where total is the count of all
// matching rows (not just this page). limit defaults to _pageSize.
//
// Response shape: { issues: [...], total: N, offset: N, limit: N }
async function fetchIssuePage(offset, limit) {
    limit = limit || _pageSize;
    const params = new URLSearchParams();
    if (_statusFilter !== 'all')   params.set('status',   _statusFilter);
    if (_priorityFilter !== 'all') params.set('priority', _priorityFilter);
    if (_projectFilter !== 'all')  params.set('project',  _projectFilter);
    const q = (document.getElementById('search-input') || {}).value || '';
    if (q) params.set('search', q);
    params.set('sort',   _sortCol);
    params.set('order',  _sortAsc ? 'asc' : 'desc');
    params.set('limit',  String(limit));
    params.set('offset', String(offset || 0));
    const data = await apiGet('/api/issues?' + params);
    return { issues: data.issues || [], total: data.total || 0 };
}

// GET /api/issues/{id}
// Returns the full detail for one issue plus all its comments.
// Response shape: { issue: { ...all fields... },
//                   comments: [{ id, author, body, created_at }, ...] }
async function fetchIssue(id) {
    return apiGet(`/api/issues/${id}`);
}

// GET /api/projects
// Returns all defined projects together with their component lists.
// Response shape: { projects: [{ name, components: ["Comp A", ...] }, ...] }
async function fetchProjects() {
    const data = await apiGet('/api/projects');
    return data.projects || [];
}

// POST /api/issues
// Creates a new issue. Description and assignee are optional; all other
// fields are required (validated server-side).
// Response shape: { issue: { ...newly created issue... } }
async function createIssue(title, description, priority, assignee, project, component) {
    return apiPost('/api/issues', { title, description, priority, assignee, project, component });
}

// PUT /api/issues/{id}
// Replaces all mutable fields of an existing issue. Every field must
// be provided even if unchanged — this is a full replacement, not a
// partial update (PATCH). Only the reporter, current assignee, and
// admins may call this; others receive 403 Forbidden.
// Response shape: { issue: { ...updated issue... } }
async function updateIssue(id, title, description, priority, status, assignee, project, component) {
    return apiPut(`/api/issues/${id}`, { title, description, priority, status, assignee, project, component });
}

// DELETE /api/issues/{id}
// Permanently deletes an issue and all its comments. Admin-only.
async function deleteIssue(id) {
    return apiDelete(`/api/issues/${id}`);
}

// POST /api/issues/{id}/comments
// Adds a comment to an issue. Any authenticated user may comment on
// any issue. Returns { comment: { ...new comment... } }.
async function addComment(issueId, body) {
    return apiPost(`/api/issues/${issueId}/comments`, { body });
}

// =====================================================================
// UI — LOGIN
// =====================================================================

// showLogin hides the main app shell and displays the login overlay.
// If a message is provided (e.g. "Session expired") it is shown above
// the Sign In button. The username and password fields are always
// cleared so previously typed values don't linger.
function showLogin(msg) {
    document.getElementById('app').style.display = 'none';
    document.getElementById('login-error').textContent = msg || '';
    document.getElementById('login-user').value = '';
    document.getElementById('login-pass').value = '';
    document.getElementById('login-overlay').style.display = 'flex';
    document.getElementById('login-user').focus();
}

// submitLogin is called when the user clicks "Sign In" or presses Enter
// in the password field. It reads the form, calls POST /api/login, and
// on success stores the session and launches the app.
//
// POST /api/login
//   Request body: { username, password, keep_logged_in }
//   Response:     { username, display_name, is_admin }
//   Side effect:  server sets an HttpOnly session cookie (idtrack_session)
//                 that the browser will attach to all subsequent requests
//                 automatically — JS cannot read HttpOnly cookies.
async function submitLogin() {
    const username = document.getElementById('login-user').value.trim().toLowerCase();
    const password = document.getElementById('login-pass').value;
    const err      = document.getElementById('login-error');
    const btn      = document.getElementById('login-submit-btn');

    err.textContent = '';
    if (!username || !password) { err.textContent = 'Username and password are required.'; return; }

    // Disable the button and update its label while the request is in
    // flight so the user knows something is happening.
    btn.disabled = true;
    btn.textContent = 'Signing in…';

    try {
        // We call fetch() directly here instead of apiPost() because:
        //  - Login is unauthenticated (no session cookie yet), so a 401
        //    would be misleading.
        //  - Wrong credentials give 401 but the error message should say
        //    "Invalid password", not "Session expired".
        const res = await fetch('/api/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, password, keep_logged_in: _keepLoggedIn }),
        });

        if (!res.ok) {
            const d = await res.json().catch(() => ({}));
            err.textContent = d.error === 'too many failed login attempts — try again later'
                ? 'Too many failed attempts. Please wait a minute before trying again.'
                : 'Invalid username or password.';
            return;
        }

        const user = await res.json();
        // Store only the non-sensitive display object — no password.
        // The actual authentication token is the HttpOnly cookie the
        // server just set in the Set-Cookie response header.
        _currentUser = { username: user.username, display_name: user.display_name, is_admin: !!user.is_admin };
        sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user: _currentUser }));
        if (_keepLoggedIn) localStorage.setItem(PERSIST_KEY, JSON.stringify({ user: _currentUser }));

        document.getElementById('login-overlay').style.display = 'none';
        await launchApp();

    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Login failed.';
    } finally {
        // 'finally' runs whether the try block succeeded or threw an error,
        // so the button is always re-enabled even if login failed.
        btn.disabled = false;
        btn.textContent = 'Sign In';
    }
}

// =====================================================================
// UI — MAIN APP
// =====================================================================

// launchApp is called once after every successful login — from
// submitLogin(), submitOnboarding(), and init() when restoring a saved
// session. It wires up the app shell for the logged-in state and then
// fetches the initial data needed to render the UI.
async function launchApp() {
    // Show the user's display name in the header badge.
    document.getElementById('user-badge').textContent = _currentUser.display_name || _currentUser.username;
    // Admin-only menu items: '' (visible) for admins, 'none' (hidden) for others.
    const adminDisplay = _currentUser.is_admin ? '' : 'none';
    document.getElementById('menu-manage-users').style.display   = adminDisplay;
    document.getElementById('menu-edit-projects').style.display  = adminDisplay;
    document.getElementById('app').style.display = '';
    // Reset the idle timer so it starts fresh from this login event.
    stopIdleTracking();
    startIdleTracking();
    await populateAssigneeDropdowns();
    await populateProjectDropdowns();
    await loadIssueWindow();
    startPolling();
}

// doLogout is triggered by "Sign out" in the menu and by the idle-
// timeout handler. It tells the server to invalidate the session, then
// wipes all client-side auth state and shows the login screen.
//
// POST /api/logout
//   No request body. The server reads the session token from the cookie,
//   removes it from the in-memory session store, and clears the cookie
//   in the Set-Cookie response header.
async function doLogout() {
    stopIdleTracking();
    stopPolling();
    dismissRefreshHint();
    // Fire-and-forget: if the network call fails we still clear local
    // state so the user is at least logged out on this device.
    try { await fetch('/api/logout', { method: 'POST' }); } catch {}
    _currentUser = null;
    _issueWindow = [];
    _totalIssues = 0;
    sessionStorage.removeItem(SESSION_KEY);
    localStorage.removeItem(PERSIST_KEY);
    _keepLoggedIn = false;
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.keepLoggedIn = false;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
    document.getElementById('app').style.display = 'none';
    _detailDirty = false;
    closeDetail();
    showLogin();
}

// =====================================================================
// UI — FILTERS & SORT
// =====================================================================
// Filters and sort are sent to the server on every change; the server
// handles all filtering, sorting, and pagination. Each change below
// resets the issue window and re-fetches from offset 0.

function setStatusFilter(val) {
    _statusFilter = val;
    loadIssueWindow();
}

function setPriorityFilter(val) {
    _priorityFilter = val;
    loadIssueWindow();
}

function setProjectFilter(val) {
    _projectFilter = val;
    loadIssueWindow();
}

// applyFilters is the oninput handler for the search box. It debounces
// the server fetch by 300 ms so rapid typing doesn't fire a request on
// every keystroke. A "Searching…" indicator is shown during the wait.
function applyFilters() {
    const ss = document.getElementById('search-status');
    const q  = (document.getElementById('search-input') || {}).value || '';
    if (ss) ss.textContent = q ? 'Searching…' : '';
    if (_searchTimer) clearTimeout(_searchTimer);
    _searchTimer = setTimeout(async () => {
        if (ss) ss.textContent = '';
        await loadIssueWindow();
    }, 300);
}

// clearSearch immediately clears the search box and reloads (no debounce).
// Bound to the Escape key on the search input.
function clearSearch() {
    const input = document.getElementById('search-input');
    if (input) input.value = '';
    if (_searchTimer) { clearTimeout(_searchTimer); _searchTimer = null; }
    const ss = document.getElementById('search-status');
    if (ss) ss.textContent = '';
    loadIssueWindow();
}

// toggleSort is called when the user clicks a column header. Clicking the
// active column reverses direction; a new column sorts ascending (except
// 'id', which defaults to descending so the newest issues appear first).
function toggleSort(col) {
    if (_sortCol === col) {
        _sortAsc = !_sortAsc;
    } else {
        _sortCol = col;
        _sortAsc = (col !== 'id');
    }
    updateSortUI();
    loadIssueWindow();
}

// updateSortUI decorates the active column header with a sort arrow
// (▲ ascending, ▼ descending). It first removes all indicators from
// every header, then adds the correct one to the active column.
// colMap translates the internal sort-column names (which match the
// field names on issue objects) to the CSS class names on the <th>
// elements in the HTML.
function updateSortUI() {
    document.querySelectorAll('.issues-table th').forEach(th => {
        th.classList.remove('sort-active');
        const arr = th.querySelector('.sort-arrow');
        if (arr) arr.remove();
    });
    const colMap = { id:'col-id', title:'col-title', project:'col-project', component:'col-component', priority:'col-priority', status:'col-status', assignee:'col-assignee', created_at:'col-date' };
    const cls = colMap[_sortCol];
    if (cls) {
        const th = document.querySelector(`.issues-table th.${cls}`);
        if (th) {
            th.classList.add('sort-active');
            const arrow = document.createElement('span');
            arrow.className = 'sort-arrow';
            arrow.textContent = _sortAsc ? ' ▲' : ' ▼';
            th.appendChild(arrow);
        }
    }
}

// =====================================================================
// UI — ISSUES LIST
// =====================================================================

// loadIssueWindow resets the issue window and fetches the first page from the
// server using the current filter/sort/search state. It is called whenever
// those inputs change (filter dropdowns, search box, sort headers) and at
// login time.
//
// The generation counter trick: we capture the current _fetchGen value in a
// local variable `gen` before the async fetch begins. When the response
// arrives we compare `gen` to the (possibly incremented) _fetchGen. If they
// differ it means the user changed a filter while this request was in flight —
// we silently discard the result so it never overwrites a newer page.
async function loadIssueWindow() {
    const gen = ++_fetchGen;
    _issueWindow  = [];
    _totalIssues  = 0;
    _lastSeenAt   = '';
    _fetchLock    = false;
    renderIssueWindow();     // immediately shows the empty state / clears old rows
    updateIssueCounter();
    try {
        const { issues, total } = await fetchIssuePage(0);
        if (gen !== _fetchGen) return; // a newer filter change superseded this fetch
        _issueWindow = issues;
        _totalIssues = total;
        _updateLastSeenAt(issues);
        renderIssueWindow();
        updateIssueCounter();
        setupScrollObserver();
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('loadIssueWindow:', e);
    }
}

// loadNextPage appends the next page to _issueWindow. Called by the
// IntersectionObserver when the bottom sentinel div scrolls into view.
//
// _fetchLock is set to true for the duration of the network request. Without
// it the observer could fire again before the first response arrives (e.g.
// the user scrolls quickly) and we would launch duplicate overlapping fetches
// that each try to append the same page of rows.
async function loadNextPage() {
    if (_fetchLock) return;                            // already fetching a page
    if (_issueWindow.length >= _totalIssues) return;  // all pages already loaded
    _fetchLock = true;
    const gen    = _fetchGen;
    const offset = _issueWindow.length;
    try {
        const { issues, total } = await fetchIssuePage(offset);
        if (gen !== _fetchGen) { _fetchLock = false; return; }
        _issueWindow  = _issueWindow.concat(issues);
        _totalIssues  = total;
        _updateLastSeenAt(issues);
        _appendIssueRows(issues);
        updateIssueCounter();
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('loadNextPage:', e);
    }
    _fetchLock = false;
}

// _updateLastSeenAt advances the polling cursor to the maximum updated_at
// timestamp seen across all issues in the batch. Timestamps are RFC3339
// strings (e.g. "2026-05-17T14:23:00Z") so lexicographic comparison is
// equivalent to chronological comparison — no Date parsing needed.
function _updateLastSeenAt(issues) {
    for (const iss of issues) {
        if (!_lastSeenAt || iss.updated_at > _lastSeenAt) _lastSeenAt = iss.updated_at;
    }
}

// issueRow returns the HTML string for a single table row. The data-id
// attribute embeds the issue id directly on the DOM element so we can later
// find and update a specific row with:
//   document.querySelector('#issues-tbody tr[data-id="42"]')
// without iterating over every row or keeping a separate id→element map.
// esc() is called on every user-supplied string to prevent XSS — it converts
// characters like < > & " into safe HTML entities before they reach innerHTML.
function issueRow(issue) {
    const sel = issue.id === _currentId ? ' selected' : '';
    return `<tr class="issue-row${sel}" data-id="${issue.id}" onclick="selectIssue(${issue.id})">
        <td class="col-id">#${esc(String(issue.id))}</td>
        <td class="col-title issue-title-cell">${esc(issue.title)}</td>
        <td class="col-project">${esc(issue.project || '—')}</td>
        <td class="col-component">${esc(issue.component || '—')}</td>
        <td class="col-priority">${priorityBadge(issue.priority)}</td>
        <td class="col-status">${statusBadge(issue.status)}</td>
        <td class="col-assignee">${esc(issue.assignee ? displayName(issue.assignee) : '—')}</td>
        <td class="col-date">${fmtDate(issue.created_at)}</td>
    </tr>`;
}

// renderIssueWindow rebuilds the entire tbody from _issueWindow. Used when the
// window is first loaded (or reloaded after a filter change). For appending
// additional pages use _appendIssueRows, which is faster because it only
// touches the new rows rather than replacing everything.
function renderIssueWindow() {
    const tbody = document.getElementById('issues-tbody');
    const empty = document.getElementById('issues-empty');
    const table = document.getElementById('issues-table');
    if (!tbody) return;

    if (_issueWindow.length === 0) {
        table.style.display = 'none';
        empty.style.display = '';
        return;
    }
    table.style.display = '';
    empty.style.display = 'none';
    tbody.innerHTML = _issueWindow.map(issueRow).join('');
    updateSortUI();
}

// _appendIssueRows appends a new batch of rows to the tbody without
// rebuilding rows that are already on screen. insertAdjacentHTML('beforeend')
// is equivalent to tbody.innerHTML += html but avoids the browser re-parsing
// the existing content, making it faster and preserving any event state on
// already-rendered rows.
function _appendIssueRows(issues) {
    const tbody = document.getElementById('issues-tbody');
    const table = document.getElementById('issues-table');
    const empty = document.getElementById('issues-empty');
    if (!tbody) return;
    if (issues.length === 0) return;
    table.style.display = '';
    empty.style.display = 'none';
    tbody.insertAdjacentHTML('beforeend', issues.map(issueRow).join(''));
}

// updateIssueCounter updates the sticky "N of M issues" badge above the list.
// When all pages are loaded it shows the total count; while more pages remain
// it shows "Showing X of Y issues" so users know scrolling will reveal more.
// The `_totalIssues === 1 ? '' : 's'` ternary is a simple pluralisation guard
// so the label reads "1 issue" rather than "1 issues".
function updateIssueCounter() {
    const el = document.getElementById('issue-counter');
    if (!el) return;
    if (_totalIssues === 0) { el.style.display = 'none'; return; }
    el.style.display = '';
    const loaded = _issueWindow.length;
    el.textContent = loaded < _totalIssues
        ? `Showing ${loaded} of ${_totalIssues} issues`
        : `${_totalIssues} issue${_totalIssues === 1 ? '' : 's'}`;
}

// setupScrollObserver wires an IntersectionObserver to the invisible sentinel
// <div> that sits just below the last table row. IntersectionObserver is a
// browser API that fires a callback whenever a watched element enters or leaves
// the viewport — far more efficient than attaching a scroll event listener and
// checking element positions on every scroll event.
//
// rootMargin: '0px 0px 300px 0px' expands the "visible" zone 300px below the
// actual viewport bottom, so the next page starts loading before the user
// reaches the very last row rather than only after they hit the bottom.
//
// We disconnect and recreate the observer on each call so stale observers from
// a previous filter/load cycle don't accumulate in the background.
function setupScrollObserver() {
    if (_scrollObserver) { _scrollObserver.disconnect(); _scrollObserver = null; }
    const sentinel = document.getElementById('issue-bottom-sentinel');
    if (!sentinel) return;
    _scrollObserver = new IntersectionObserver(entries => {
        if (entries[0].isIntersecting) loadNextPage();
    }, { rootMargin: '0px 0px 300px 0px' });
    _scrollObserver.observe(sentinel);
}

// =====================================================================
// UI — ISSUE DETAIL
// =====================================================================

// selectIssue opens the detail panel for the given issue id. It first
// checks for unsaved changes, then fetches the full issue record
// (including comments) from the server, populates all fields, and
// makes the panel visible.
//
// GET /api/issues/{id}
//   Response: { issue: { ...all fields... },
//               comments: [{ id, author, body, created_at }, ...] }
//
// Adding 'has-detail' to #main-layout signals to the responsive CSS
// that the detail panel is open; the CSS then hides the list panel on
// tablet/phone screens so the detail panel gets the full height.
async function selectIssue(id) {
    if (_detailDirty) {
        if (!confirm('You have unsaved changes. Discard them?')) return;
    }
    _currentId   = id;
    _detailDirty = false;

    // Update selected-row highlight without a full re-render.
    document.querySelectorAll('#issues-tbody .issue-row').forEach(tr => {
        tr.classList.toggle('selected', Number(tr.dataset.id) === id);
    });

    try {
        const { issue, comments } = await fetchIssue(id);
        if (!issue) return;

        // Populate all detail panel fields from the fetched issue object.
        document.getElementById('detail-issue-id').textContent = `Issue #${issue.id}`;
        document.getElementById('detail-title').value    = issue.title       || '';
        document.getElementById('detail-status').value   = issue.status      || 'Open';
        // Snapshot the status now so we can detect transitions later.
        _originalStatus = issue.status || 'Open';
        document.getElementById('detail-priority').value = issue.priority    || 'Medium';
        document.getElementById('detail-desc').value     = issue.description || '';
        document.getElementById('detail-reporter').textContent = issue.reporter ? displayName(issue.reporter) : '';
        document.getElementById('detail-created').textContent  = fmtDateTime(issue.created_at);
        document.getElementById('detail-updated').textContent  = fmtDateTime(issue.updated_at);
        document.getElementById('detail-error').textContent    = '';

        const asnSel = document.getElementById('detail-assignee');
        asnSel.value = issue.assignee || '';

        document.getElementById('detail-project').value = issue.project || '';
        // The component dropdown depends on the selected project, so we
        // rebuild it with the appropriate options for that project.
        populateComponentDropdown('detail-component', issue.project || '', issue.component || '');

        const canEdit = canModifyIssue(issue);

        // Save button is hidden by default; markDetailDirty() reveals it
        // the first time any field changes.
        document.getElementById('detail-save-btn').style.display = 'none';
        // Delete button is shown immediately for authorized users.
        document.getElementById('detail-delete-btn').style.display = canEdit ? '' : 'none';

        // Disable all editable fields for read-only viewers (not the
        // reporter, assignee, or an admin). A disabled input never fires
        // change events, so markDetailDirty() is never triggered and the
        // Save button never appears. The comment textarea is intentionally
        // left enabled — any authenticated user may add a comment.
        ['detail-title', 'detail-status', 'detail-priority',
         'detail-assignee', 'detail-project', 'detail-component', 'detail-desc']
            .forEach(id => { const el = document.getElementById(id); if (el) el.disabled = !canEdit; });

        _detailDirty = false;

        renderComments(comments);
        document.getElementById('comment-input').value = '';
        document.getElementById('detail-panel').style.display = '';
        // Signal the responsive CSS that the detail panel is now open.
        const layout = document.getElementById('main-layout');
        if (layout) layout.classList.add('has-detail');

    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('selectIssue:', e);
    }
}

// closeDetail hides the detail panel and clears the current selection.
// Checks for unsaved changes first, just like selectIssue() does.
// Removing 'has-detail' from #main-layout tells the responsive CSS to
// restore the list panel at tablet/phone sizes.
function closeDetail() {
    if (_detailDirty) {
        if (!confirm('You have unsaved changes. Discard them?')) return;
    }
    _currentId   = null;
    _detailDirty = false;
    document.getElementById('detail-panel').style.display = 'none';
    const layout = document.getElementById('main-layout');
    if (layout) layout.classList.remove('has-detail');
    document.querySelectorAll('#issues-tbody .issue-row').forEach(tr => tr.classList.remove('selected'));
}

// markDetailDirty is called by the oninput / onchange handlers on all
// editable fields in the detail panel. The first call makes the Save
// Changes button visible; subsequent calls while the panel is already
// dirty are no-ops.
function markDetailDirty() {
    if (!_detailDirty) {
        _detailDirty = true;
        document.getElementById('detail-save-btn').style.display = '';
    }
}

// saveIssueChanges reads all editable fields from the detail panel,
// validates them, and then either:
//   (a) calls doSaveIssue directly for simple field updates, or
//   (b) opens a status-change confirmation dialog when the status has
//       changed (Open→Resolved or Resolved→Open), capturing the current
//       field values in _pendingStatusData for use after confirmation.
async function saveIssueChanges() {
    if (!_currentId) return;
    const title     = document.getElementById('detail-title').value.trim();
    const status    = document.getElementById('detail-status').value;
    const priority  = document.getElementById('detail-priority').value;
    const assignee  = document.getElementById('detail-assignee').value;
    const desc      = document.getElementById('detail-desc').value.trim();
    const project   = document.getElementById('detail-project').value;
    const component = document.getElementById('detail-component').value;
    const err       = document.getElementById('detail-error');

    err.textContent = '';
    if (!title)                          { err.textContent = 'Title is required.'; return; }
    if (!project)                        { err.textContent = 'Project is required.'; return; }
    if (!component)                      { err.textContent = 'Component is required.'; return; }
    // An assignee is required before an issue can be marked Resolved.
    if (status === 'Resolved' && !assignee) { err.textContent = 'An assignee is required before marking an issue Resolved.'; return; }

    // Status transitions require a dialog rather than an immediate save.
    if (_originalStatus === 'Open' && status === 'Resolved') {
        _pendingStatusData = { title, desc, priority, status, assignee, project, component };
        showResolveDialog();
        return;
    }
    if (_originalStatus === 'Resolved' && status === 'Open') {
        _pendingStatusData = { title, desc, priority, status, assignee, project, component };
        showReopenDialog();
        return;
    }

    // No status transition: save directly, no comment needed.
    await doSaveIssue(title, desc, priority, status, assignee, project, component, null);
}

// doSaveIssue performs the actual PUT to update the issue, then
// optionally POSTs a comment (for status-change notes). Called by
// saveIssueChanges() (commentBody = null) and confirmStatusChange()
// (commentBody = non-null string).
//
// PUT /api/issues/{id}
//   Request body: { title, description, priority, status, assignee, project, component }
//   Response:     { issue: { ...updated issue... } }
//
// POST /api/issues/{id}/comments  (only when commentBody is non-null)
//   Request body: { body: "comment text" }
//   Response:     { comment: { ...new comment... } }
async function doSaveIssue(title, desc, priority, status, assignee, project, component, commentBody) {
    const err = document.getElementById('detail-error');
    const btn = document.getElementById('detail-save-btn');
    err.textContent = '';
    btn.disabled = true;
    btn.textContent = 'Saving…';
    try {
        const { issue } = await updateIssue(_currentId, title, desc, priority, status, assignee, project, component);
        if (commentBody) await addComment(_currentId, commentBody);
        _originalStatus = status;
        _detailDirty = false;
        btn.style.display = 'none';
        document.getElementById('detail-updated').textContent = fmtDateTime(issue.updated_at);
        // Update the window entry and the DOM row in-place so the list reflects
        // the new status/priority/assignee without reloading the entire page.
        // _issueWindow[idx] = issue replaces the JS object in our local array.
        // tr.outerHTML = issueRow(issue) replaces the entire <tr> element in the
        // DOM with a freshly rendered string — the browser parses and inserts the
        // new HTML in place of the old element, so the surrounding rows are
        // untouched. We also advance _lastSeenAt so the polling loop won't report
        // this save as an "external change" on the next 30-second tick.
        const idx = _issueWindow.findIndex(i => i.id === issue.id);
        if (idx !== -1) {
            _issueWindow[idx] = issue;
            const tr = document.querySelector(`#issues-tbody tr[data-id="${issue.id}"]`);
            if (tr) tr.outerHTML = issueRow(issue);
        }
        if (issue.updated_at > (_lastSeenAt || '')) _lastSeenAt = issue.updated_at;
        if (commentBody) {
            const { comments } = await fetchIssue(_currentId);
            renderComments(comments);
        }
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Save failed.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Save Changes';
    }
}

// showResolveDialog configures and opens the status-change overlay for
// the Open → Resolved transition. The "Fixed Version" field is shown
// and the comment is optional.
function showResolveDialog() {
    document.getElementById('sc-title').textContent          = 'Resolve Issue';
    document.getElementById('sc-intro').textContent          = 'Optionally document the resolution before marking this issue Resolved.';
    document.getElementById('sc-version-group').style.display = '';
    document.getElementById('sc-comment-label').textContent  = 'Comment (optional)';
    document.getElementById('sc-version').value              = '';
    document.getElementById('sc-comment').value              = '';
    document.getElementById('sc-error').textContent          = '';
    document.getElementById('status-change-overlay').style.display = 'flex';
    document.getElementById('sc-version').focus();
}

// showReopenDialog configures the same overlay for the Resolved → Open
// transition. The "Fixed Version" field is hidden and the comment
// becomes required (confirmStatusChange enforces this).
function showReopenDialog() {
    document.getElementById('sc-title').textContent          = 'Reopen Issue';
    document.getElementById('sc-intro').textContent          = 'A reason is required to reopen a resolved issue.';
    document.getElementById('sc-version-group').style.display = 'none';
    document.getElementById('sc-comment-label').textContent  = 'Reason (required)';
    document.getElementById('sc-version').value              = '';
    document.getElementById('sc-comment').value              = '';
    document.getElementById('sc-error').textContent          = '';
    document.getElementById('status-change-overlay').style.display = 'flex';
    document.getElementById('sc-comment').focus();
}

// confirmStatusChange is called when the user clicks "Confirm" in the
// status-change dialog. It validates the inputs, assembles the comment
// body (if any), hides the dialog, and delegates to doSaveIssue with
// the values captured earlier in _pendingStatusData.
async function confirmStatusChange() {
    if (!_pendingStatusData) return;
    const version = document.getElementById('sc-version').value.trim();
    const comment = document.getElementById('sc-comment').value.trim();
    const err     = document.getElementById('sc-error');
    err.textContent = '';

    // Reopening always requires a non-empty reason.
    const isReopen = _pendingStatusData.status === 'Open';
    if (isReopen && !comment) {
        err.textContent = 'A reason is required to reopen an issue.';
        return;
    }

    // Build the comment body for a resolve transition by combining the
    // optional "Fixed in <version>" header with the optional comment text.
    // join('\n\n') puts a blank line between the two parts.
    let commentBody = null;
    if (_pendingStatusData.status === 'Resolved') {
        let parts = [];
        if (version) parts.push(`Fixed in ${version}`);
        if (comment) parts.push(comment);
        if (parts.length > 0) commentBody = parts.join('\n\n');
    } else {
        commentBody = comment || null;
    }

    document.getElementById('status-change-overlay').style.display = 'none';
    const { title, desc, priority, status, assignee, project, component } = _pendingStatusData;
    _pendingStatusData = null;
    await doSaveIssue(title, desc, priority, status, assignee, project, component, commentBody);
}

// cancelStatusChange dismisses the dialog without saving. It restores
// the status dropdown to the issue's actual saved value so the UI
// reflects reality after the user cancels.
function cancelStatusChange() {
    if (_pendingStatusData) {
        document.getElementById('detail-status').value = _originalStatus;
        _pendingStatusData = null;
    }
    document.getElementById('status-change-overlay').style.display = 'none';
}

// =====================================================================
// UI — COMMENTS
// =====================================================================

// renderComments rebuilds the comment list in the detail panel from
// the provided array. Admin users get a trash-can button on each
// comment; regular users do not see any delete controls.
function renderComments(comments) {
    const el = document.getElementById('comments-list');
    if (!comments || comments.length === 0) {
        el.innerHTML = '<p class="comments-empty">No comments yet.</p>';
        return;
    }
    // This arrow function returns either the button HTML or an empty
    // string depending on whether the current user is an admin.
    const trashBtn = (id) => (_currentUser && _currentUser.is_admin)
        ? `<button class="btn-trash" onclick="confirmDeleteComment(${id}, event)" title="Delete comment">&#x1F5D1;</button>`
        : '';
    el.innerHTML = comments.map(c => `
        <div class="comment-item">
            <div class="comment-header">
                <span class="comment-author">${esc(displayName(c.author))}</span>
                <span class="comment-date">${fmtDateTime(c.created_at)}</span>
                ${trashBtn(c.id)}
            </div>
            <div class="comment-body">${esc(c.body)}</div>
        </div>
    `).join('');
}

// submitComment reads the comment textarea, posts it, and re-renders
// the list. Ctrl+Enter / Cmd+Enter also triggers this via the
// textarea's onkeydown handler in the HTML.
//
// POST /api/issues/{id}/comments
//   Request body: { body: "comment text" }
async function submitComment() {
    if (!_currentId) return;
    const input = document.getElementById('comment-input');
    const body  = input.value.trim();
    if (!body) return;

    try {
        await addComment(_currentId, body);
        input.value = '';
        // Re-fetch rather than appending locally so we get the
        // server-assigned id and creation timestamp.
        const { comments } = await fetchIssue(_currentId);
        renderComments(comments);
        // Scroll the newly posted comment into view.
        const el = document.getElementById('comments-list');
        if (el) el.scrollIntoView({ behavior: 'smooth', block: 'end' });
    } catch (e) {
        if (e.message !== 'Unauthorized') alert('Failed to add comment: ' + e.message);
    }
}

// confirmDeleteIssue is triggered by the Delete button in the detail
// panel header (shown only to admins). After confirmation it deletes the
// issue server-side, removes it from _issueWindow and the DOM, and closes
// the panel. We decrement _totalIssues so the counter stays accurate.
//
// DELETE /api/issues/{id}
async function confirmDeleteIssue() {
    if (!_currentId) return;
    if (!confirm(`Delete Issue #${_currentId}? This cannot be undone.`)) return;
    try {
        const deletedId = _currentId;
        await deleteIssue(deletedId);
        _issueWindow = _issueWindow.filter(i => i.id !== deletedId);
        _totalIssues = Math.max(0, _totalIssues - 1);
        const tr = document.querySelector(`#issues-tbody tr[data-id="${deletedId}"]`);
        if (tr) tr.remove();
        updateIssueCounter();
        closeDetail();
    } catch (e) {
        if (e.message !== 'Unauthorized') alert('Failed to delete issue: ' + (e.message || 'unknown error'));
    }
}

// confirmDeleteComment is triggered by the trash-can button on each
// comment (visible to admins only). event.stopPropagation() prevents
// the click from bubbling up and triggering the parent row's handler.
//
// DELETE /api/issues/{id}/comments/{commentId}
async function confirmDeleteComment(commentId, event) {
    event.stopPropagation();
    if (!confirm('Delete this comment? This cannot be undone.')) return;
    try {
        await apiDelete(`/api/issues/${_currentId}/comments/${commentId}`);
        const { comments } = await fetchIssue(_currentId);
        renderComments(comments);
    } catch (e) {
        if (e.message !== 'Unauthorized') alert('Failed to delete comment: ' + (e.message || 'unknown error'));
    }
}

// =====================================================================
// UI — NEW ISSUE
// =====================================================================

// showNewIssue resets all fields and opens the New Issue overlay.
// The project and assignee dropdowns are refreshed from the server
// each time the form opens so any recently added users or projects
// are reflected immediately.
async function showNewIssue() {
    document.getElementById('ni-title').value = '';
    document.getElementById('ni-priority').value = 'Medium';
    document.getElementById('ni-desc').value = '';
    document.getElementById('ni-error').textContent = '';
    document.getElementById('ni-project').value = '';
    const niComp = document.getElementById('ni-component');
    niComp.innerHTML = '<option value="">Choose component…</option>';
    // Component is disabled until a project is selected (see onNiProjectChange).
    niComp.disabled = true;
    await populateAssigneeDropdowns();
    await populateProjectDropdowns();
    document.getElementById('new-issue-overlay').style.display = 'flex';
    document.getElementById('ni-title').focus();
}

function hideNewIssue() {
    document.getElementById('new-issue-overlay').style.display = 'none';
}

// submitNewIssue validates the form, creates the issue on the server,
// refreshes the list, and then automatically opens the new issue's
// detail panel so the user can see it immediately.
//
// POST /api/issues
//   Request body: { title, description, priority, assignee, project, component }
//   Response:     { issue: { ...newly created issue... } }
async function submitNewIssue() {
    const title     = document.getElementById('ni-title').value.trim();
    const priority  = document.getElementById('ni-priority').value;
    const assignee  = document.getElementById('ni-assignee').value;
    const desc      = document.getElementById('ni-desc').value.trim();
    const project   = document.getElementById('ni-project').value;
    const component = document.getElementById('ni-component').value;
    const err       = document.getElementById('ni-error');
    const btn       = document.getElementById('ni-submit-btn');

    err.textContent = '';
    if (!title)     { err.textContent = 'Title is required.'; return; }
    if (!project)   { err.textContent = 'Project is required.'; return; }
    if (!component) { err.textContent = 'Component is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Creating…';

    try {
        const { issue: newIssue } = await createIssue(title, desc, priority, assignee, project, component);
        hideNewIssue();
        // Reload the full window (server determines sort order).
        await loadIssueWindow();
        if (newIssue) selectIssue(newIssue.id);
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to create issue.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Create Issue';
    }
}

// =====================================================================
// ASSIGNEE DROPDOWNS
// =====================================================================

// populateAssigneeDropdowns fetches the user list from the server and
// rebuilds the Assignee dropdowns in both the New Issue form and the
// detail panel. It also rebuilds _userMap and _userList so displayName()
// works correctly everywhere.
//
// GET /api/users → { users: [...] }
//
// The previous selection is preserved after rebuilding so that calling
// this function (e.g. after adding a user) does not silently clear an
// in-progress assignment.
async function populateAssigneeDropdowns() {
    let users = [];
    try { users = await fetchUsers(); } catch {}

    _userList = users;
    _userMap = {};
    users.forEach(u => { _userMap[u.username] = u.display_name || u.username; });

    const options = ['<option value="">(unassigned)</option>']
        .concat(users.map(u => `<option value="${esc(u.username)}">${esc(u.display_name || u.username)}</option>`))
        .join('');

    ['ni-assignee', 'detail-assignee'].forEach(id => {
        const sel = document.getElementById(id);
        if (!sel) return;
        const prev = sel.value;  // Remember the current selection.
        sel.innerHTML = options;
        sel.value = prev;        // Restore it after rebuilding.
    });
}

// =====================================================================
// UI — USER MANAGEMENT (admin)
// =====================================================================
// The Manage Users overlay is the "parent" in a two-level overlay
// stack. Opening Add User or Edit User hides the parent; closing either
// child always calls openManageUsers() to refresh and re-show the
// parent list. This means every exit path — success, cancel, or
// backdrop click — leaves the user list up to date.

// openManageUsers shows the overlay and fetches the user list.
// It shows "Loading…" while the request is in flight.
//
// GET /api/users → { users: [...] }
async function openManageUsers() {
    _closeMenuOnOutside();
    document.getElementById('manage-users-list').innerHTML = '<p class="mu-loading">Loading…</p>';
    document.getElementById('manage-users-overlay').style.display = 'flex';
    let users = [];
    try { users = await fetchUsers(); _userList = users; } catch {}
    renderManageUsersList(users);
}

function hideManageUsers() {
    document.getElementById('manage-users-overlay').style.display = 'none';
}

// renderManageUsersList builds the user table inside the Manage Users
// overlay. Each row is clickable and calls openEditUserFromManage().
function renderManageUsersList(users) {
    const div = document.getElementById('manage-users-list');
    if (users.length === 0) {
        div.innerHTML = '<p class="mu-empty">No users yet.</p>';
        return;
    }
    div.innerHTML = `<table class="mu-table">
        <thead><tr>
            <th>Username</th><th>Display Name</th><th>Admin</th><th>Last Login</th>
        </tr></thead>
        <tbody>${users.map(u => `
            <tr class="mu-row" onclick="openEditUserFromManage('${esc(u.username)}')">
                <td class="mu-username">${esc(u.username)}</td>
                <td>${esc(u.display_name || u.username)}</td>
                <td>${u.is_admin ? '<span class="badge badge-open">Admin</span>' : ''}</td>
                <td class="mu-login">${esc(u.last_login_at ? fmtDateTime(u.last_login_at) : '(never)')}</td>
            </tr>`).join('')}
        </tbody></table>`;
}

// openAddUserFromManage and openEditUserFromManage implement the overlay
// navigation pattern: hide the parent, open the child. Each child's
// hide function calls openManageUsers() to return to a refreshed list.
function openAddUserFromManage() {
    hideManageUsers();
    openAddUser();
}

function openEditUserFromManage(username) {
    hideManageUsers();
    openEditUser(username);
}

// openAddUser clears and opens the Add User overlay.
function openAddUser() {
    document.getElementById('au-username').value      = '';
    document.getElementById('au-display-name').value  = '';
    document.getElementById('au-password').value      = '';
    document.getElementById('au-confirm').value        = '';
    document.getElementById('au-admin').checked        = false;
    document.getElementById('au-error').textContent   = '';
    document.getElementById('add-user-overlay').style.display = 'flex';
    document.getElementById('au-username').focus();
}

// hideAddUser closes Add User and returns to the refreshed Manage Users list.
function hideAddUser() {
    document.getElementById('add-user-overlay').style.display = 'none';
    openManageUsers();
}

// submitAddUser creates a new user account on the server.
//
// POST /api/users
//   Request body: { username, display_name, password, is_admin }
//   Response:     { user: { username, display_name, is_admin } }
//   Admin-only: the server returns 403 Forbidden for non-admins.
async function submitAddUser() {
    const username     = document.getElementById('au-username').value.trim().toLowerCase();
    const displayName  = document.getElementById('au-display-name').value.trim();
    const password     = document.getElementById('au-password').value;
    const confirm      = document.getElementById('au-confirm').value;
    const isAdmin      = document.getElementById('au-admin').checked;
    const err          = document.getElementById('au-error');
    const btn          = document.getElementById('au-submit-btn');

    err.textContent = '';
    if (!username)  { err.textContent = 'Username is required.'; return; }
    if (!password)  { err.textContent = 'Password is required.'; return; }
    if (password !== confirm) { err.textContent = 'Passwords do not match.'; return; }

    btn.disabled = true;
    btn.textContent = 'Adding…';
    try {
        await apiPost('/api/users', { username, display_name: displayName, password, is_admin: isAdmin });
        // Refresh assignee dropdowns so the new user appears in them.
        await populateAssigneeDropdowns();
        hideAddUser();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to add user.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Add User';
    }
}

// openEditUser pre-populates the Edit User form. The user select
// dropdown is rebuilt from _userList. The optional 'preselect' argument
// (passed from openEditUserFromManage) selects that user automatically
// so the form is ready to edit without an extra click.
function openEditUser(preselect) {
    const sel = document.getElementById('eu-username');
    sel.innerHTML = ['<option value="">Choose user…</option>']
        .concat(_userList.map(u => `<option value="${esc(u.username)}">${esc(u.display_name || u.username)} (${esc(u.username)})</option>`))
        .join('');
    sel.value = preselect || '';
    document.getElementById('eu-password').value       = '';
    document.getElementById('eu-confirm').value         = '';
    document.getElementById('eu-error').textContent    = '';
    onEditUserSelect();
    document.getElementById('edit-user-overlay').style.display = 'flex';
}

// hideEditUser closes Edit User and returns to the refreshed Manage Users list.
function hideEditUser() {
    document.getElementById('edit-user-overlay').style.display = 'none';
    openManageUsers();
}

// onEditUserSelect is the onchange handler for the user dropdown in
// the Edit User form. It finds the selected user in _userList and
// fills in their current display name and admin status. The Delete
// button is hidden for the currently logged-in user's own account
// (self-deletion is disallowed).
function onEditUserSelect() {
    const username = document.getElementById('eu-username').value;
    const user = _userList.find(u => u.username === username);
    document.getElementById('eu-error').textContent = '';
    if (!user) {
        document.getElementById('eu-display-name').value  = '';
        document.getElementById('eu-password').value       = '';
        document.getElementById('eu-confirm').value         = '';
        document.getElementById('eu-admin').checked         = false;
        document.getElementById('eu-delete-btn').style.display = 'none';
        return;
    }
    document.getElementById('eu-display-name').value = user.display_name || '';
    document.getElementById('eu-password').value      = '';
    document.getElementById('eu-confirm').value        = '';
    document.getElementById('eu-admin').checked        = !!user.is_admin;
    document.getElementById('eu-delete-btn').style.display =
        (user.username === _currentUser.username) ? 'none' : '';
}

// submitEditUser saves changes to an existing user.
// Leaving the password fields blank means "keep the current password"
// — the server skips the bcrypt hash update when the password field
// is an empty string.
//
// PUT /api/users/{username}
//   Request body: { display_name, password, is_admin }
//   Admin-only. The server enforces that the last admin cannot be demoted.
async function submitEditUser() {
    const username    = document.getElementById('eu-username').value;
    const displayName = document.getElementById('eu-display-name').value.trim();
    const password    = document.getElementById('eu-password').value;
    const confirm     = document.getElementById('eu-confirm').value;
    const isAdmin     = document.getElementById('eu-admin').checked;
    const err         = document.getElementById('eu-error');
    const btn         = document.getElementById('eu-save-btn');

    err.textContent = '';
    if (!username)    { err.textContent = 'Select a user.'; return; }
    if (password !== confirm) { err.textContent = 'Passwords do not match.'; return; }

    btn.disabled = true;
    btn.textContent = 'Saving…';
    try {
        await apiPut(`/api/users/${encodeURIComponent(username)}`, {
            display_name: displayName, password, is_admin: isAdmin,
        });
        await populateAssigneeDropdowns();
        hideEditUser();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Save failed.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Save Changes';
    }
}

// confirmDeleteUser permanently deletes a user account after a
// confirmation prompt.
//
// DELETE /api/users/{username}
//   Admin-only. The server blocks deletion of the last admin account.
//   Issues and comments that reference the username retain the username
//   string; they are not deleted or reassigned.
async function confirmDeleteUser() {
    const username = document.getElementById('eu-username').value;
    if (!username) return;
    if (!confirm(`Delete user "${username}"? This cannot be undone.`)) return;
    const err = document.getElementById('eu-error');
    const btn = document.getElementById('eu-delete-btn');
    btn.disabled = true;
    try {
        await apiDelete(`/api/users/${encodeURIComponent(username)}`);
        await populateAssigneeDropdowns();
        hideEditUser();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Delete failed.';
    } finally {
        btn.disabled = false;
    }
}

// =====================================================================
// PROJECT / COMPONENT DROPDOWNS
// =====================================================================

// populateProjectDropdowns fetches the full project list from the
// server and rebuilds three places in the UI:
//   - The Project dropdown in the New Issue form  (ni-project)
//   - The Project dropdown in the detail panel    (detail-project)
//   - The Project filter in the header filter bar (project-filter)
//
// GET /api/projects → { projects: [{ name, components: [...] }, ...] }
//
// Previous selections are preserved on each dropdown after rebuilding.
async function populateProjectDropdowns() {
    try { _projectData = await fetchProjects(); } catch { _projectData = []; }

    const options = ['<option value="">Choose project…</option>']
        .concat(_projectData.map(p => `<option value="${esc(p.name)}">${esc(p.name)}</option>`))
        .join('');

    ['ni-project', 'detail-project'].forEach(id => {
        const sel = document.getElementById(id);
        if (!sel) return;
        const prev = sel.value;
        sel.innerHTML = options;
        sel.value = prev;
    });

    populateProjectFilter();
}

// populateProjectFilter rebuilds just the project filter dropdown in
// the header bar. Called by populateProjectDropdowns() and after any
// project is created or deleted.
function populateProjectFilter() {
    const sel = document.getElementById('project-filter');
    if (!sel) return;
    const prev = sel.value;
    sel.innerHTML = ['<option value="all">All…</option>']
        .concat(_projectData.map(p => `<option value="${esc(p.name)}">${esc(p.name)}</option>`))
        .join('');
    sel.value = prev;
    if (!sel.value) sel.value = 'all';
    _projectFilter = sel.value;
}

// populateComponentDropdown rebuilds the Component dropdown for the
// given select element, scoped to the named project. If the project
// name is empty or not found in _projectData the dropdown is cleared
// and disabled — components can't be chosen without a project.
// 'selectedComponent' is the value to pre-select after rebuilding.
function populateComponentDropdown(selectId, projectName, selectedComponent) {
    const sel = document.getElementById(selectId);
    if (!sel) return;
    const project = _projectData.find(p => p.name === projectName);
    if (!project || !projectName) {
        sel.innerHTML = '<option value="">Choose component…</option>';
        sel.disabled = true;
        return;
    }
    sel.innerHTML = ['<option value="">Choose component…</option>']
        .concat(project.components.map(c => `<option value="${esc(c)}">${esc(c)}</option>`))
        .join('');
    sel.disabled = false;
    sel.value = selectedComponent || '';
}

// onNiProjectChange is the onchange handler for the New Issue form's
// Project dropdown. Selecting a project enables and populates the
// cascading Component dropdown.
function onNiProjectChange() {
    populateComponentDropdown('ni-component', document.getElementById('ni-project').value, '');
}

// onDetailProjectChange is the onchange handler for the detail panel's
// Project dropdown. It cascades the Component dropdown and marks the
// detail dirty so the Save button appears.
function onDetailProjectChange() {
    populateComponentDropdown('detail-component', document.getElementById('detail-project').value, '');
    markDetailDirty();
}

// =====================================================================
// UI — EDIT PROJECTS (admin)
// =====================================================================
// The Edit Projects UI is a two-screen overlay stack:
//   ep-list-overlay   — list of all projects (parent screen)
//   ep-detail-overlay — components for one project, or the new-project form
//
// Only one overlay is visible at a time. Navigation between them follows
// the same parent/child pattern as Manage Users.

// openEditProjects shows the project list overlay and renders the
// current _projectData. No server call is needed here — _projectData
// was populated by the most recent call to populateProjectDropdowns().
function openEditProjects() {
    _closeMenuOnOutside();
    document.getElementById('ep-list-error').textContent = '';
    epRenderProjectList();
    document.getElementById('ep-list-overlay').style.display = 'flex';
}

function hideEditProjects() {
    document.getElementById('ep-list-overlay').style.display = 'none';
}

// epRenderProjectList rebuilds the clickable project rows inside the
// list overlay from the in-memory _projectData array.
function epRenderProjectList() {
    const body = document.getElementById('ep-list-body');
    if (!_projectData || _projectData.length === 0) {
        body.innerHTML = '<p class="ep-empty">No projects yet. Click <strong>+ New Project</strong> to add one.</p>';
        return;
    }
    body.innerHTML = _projectData.map(p => `
        <div class="ep-project-row" onclick="openProjectDetail('${esc(p.name)}')">
            <span class="ep-project-name">${esc(p.name)}</span>
            <span class="ep-project-count">${p.components.length} component${p.components.length !== 1 ? 's' : ''}</span>
            <span class="ep-project-arrow">&#8250;</span>
        </div>`).join('');
}

// openProjectDetail switches from the project list to the detail screen.
// Pass null for 'name' to open in "new project" mode.
//
// New project mode:  project name field is editable; components are staged
//                    in _epPendingComponents; "Create Project" button visible.
// Existing project:  name shown as heading; components listed with delete
//                    buttons; "Delete Project" button visible.
function openProjectDetail(name) {
    _epProject = name;
    _epPendingComponents = [];

    const isNew = name === null;
    document.getElementById('ep-detail-title').textContent          = isNew ? 'New Project' : name;
    document.getElementById('ep-name-group').style.display          = isNew ? '' : 'none';
    document.getElementById('ep-project-name').value                = '';
    document.getElementById('ep-delete-project-btn').style.display  = isNew ? 'none' : '';
    document.getElementById('ep-comp-name').value                   = '';
    document.getElementById('ep-detail-error').textContent          = '';
    document.getElementById('ep-create-btn').style.display          = isNew ? '' : 'none';

    epRenderComponents();
    epRenderPending();

    // Swap the two overlays.
    document.getElementById('ep-list-overlay').style.display   = 'none';
    document.getElementById('ep-detail-overlay').style.display = 'flex';
    document.getElementById(isNew ? 'ep-project-name' : 'ep-comp-name').focus();
}

// hideProjectDetail returns to the project list overlay, refreshing
// it from the current _projectData.
function hideProjectDetail() {
    document.getElementById('ep-detail-overlay').style.display = 'none';
    epRenderProjectList();
    document.getElementById('ep-list-overlay').style.display = 'flex';
}

// epRenderComponents lists the existing components for _epProject.
// Each component row includes a trash-can button for immediate deletion.
function epRenderComponents() {
    const list = document.getElementById('ep-comp-list');
    if (_epProject === null) { list.innerHTML = ''; return; }
    const project = _projectData.find(p => p.name === _epProject);
    if (!project || project.components.length === 0) {
        list.innerHTML = '<p class="ep-empty-comps">No components yet.</p>';
        return;
    }
    list.innerHTML = project.components.map(c => `
        <div class="ep-comp-item">
            <span class="ep-comp-name">${esc(c)}</span>
            <button class="btn-trash" data-component="${esc(c)}" onclick="epDeleteComponent(this.dataset.component, event)" title="Delete component">&#x1F5D1;</button>
        </div>`).join('');
}

// epRenderPending shows the list of components staged for a new project.
// These are held in _epPendingComponents and only sent to the server
// once the project itself is created.
function epRenderPending() {
    const listDiv  = document.getElementById('ep-pending-list');
    const itemsDiv = document.getElementById('ep-pending-items');
    if (_epPendingComponents.length === 0) { listDiv.style.display = 'none'; return; }
    listDiv.style.display = '';
    itemsDiv.innerHTML = _epPendingComponents.map((name, i) => `
        <div class="ac-pending-item">
            <span class="ac-pending-name">${esc(name)}</span>
            <button class="btn-trash" onclick="epRemovePending(${i})" title="Remove">&#x1F5D1;</button>
        </div>`).join('');
}

// epRemovePending removes a component from the staging list by its
// array index and re-renders.
function epRemovePending(index) {
    _epPendingComponents.splice(index, 1);
    epRenderPending();
}

// epAddComponent handles the "Add" button in the component input row.
// Behavior differs between new-project mode and existing-project mode:
//
//   New project:      validate project name is set, then stage the
//                     component in _epPendingComponents (no server call).
//   Existing project: case-insensitive duplicate check, then POST immediately.
//
// POST /api/projects/{project}/components  (existing project only)
//   Request body: { name }
//   Admin-only.
async function epAddComponent() {
    const name = document.getElementById('ep-comp-name').value.trim();
    const err  = document.getElementById('ep-detail-error');
    err.textContent = '';
    if (!name) { err.textContent = 'Enter a component name.'; return; }

    const nameLower = name.toLowerCase();

    if (_epProject === null) {
        // New-project mode: stage the component, don't hit the server yet.
        const projectName = document.getElementById('ep-project-name').value.trim();
        if (!projectName) { err.textContent = 'Enter a project name first.'; return; }
        if (_epPendingComponents.some(c => c.toLowerCase() === nameLower)) {
            err.textContent = `"${name}" is already in the list.`;
            return;
        }
        _epPendingComponents.push(name);
        document.getElementById('ep-comp-name').value = '';
        document.getElementById('ep-comp-name').focus();
        epRenderPending();
        return;
    }

    // Existing project: duplicate check, then POST to the server.
    const project = _projectData.find(p => p.name === _epProject);
    if (project && project.components.some(c => c.toLowerCase() === nameLower)) {
        err.textContent = `"${name}" already exists in this project.`;
        return;
    }
    try {
        await apiPost(`/api/projects/${encodeURIComponent(_epProject)}/components`, { name });
        // Refresh _projectData so the new component appears everywhere.
        await populateProjectDropdowns();
        document.getElementById('ep-comp-name').value = '';
        document.getElementById('ep-comp-name').focus();
        epRenderComponents();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to add component.';
    }
}

// epDeleteComponent removes a component from an existing project.
// event.stopPropagation() prevents the click from bubbling to the
// parent component row's onclick handler.
//
// DELETE /api/projects/{project}/components/{component}
//   Admin-only. The server refuses if any issues reference this component.
async function epDeleteComponent(componentName, event) {
    event.stopPropagation();
    if (!confirm(`Delete component "${componentName}" from project "${_epProject}"? This cannot be undone.`)) return;
    const err = document.getElementById('ep-detail-error');
    err.textContent = '';
    try {
        await apiDelete(`/api/projects/${encodeURIComponent(_epProject)}/components/${encodeURIComponent(componentName)}`);
        await populateProjectDropdowns();
        epRenderComponents();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Delete failed.';
    }
}

// epDeleteProject removes the currently open project and all its
// components. The server refuses if any issues reference the project.
//
// DELETE /api/projects/{project}
//   Admin-only. The server returns an error listing blocking issue IDs.
async function epDeleteProject() {
    if (!_epProject) return;
    if (!confirm(`Delete project "${_epProject}" and all its components? This cannot be undone.`)) return;
    const err = document.getElementById('ep-detail-error');
    err.textContent = '';
    try {
        await apiDelete(`/api/projects/${encodeURIComponent(_epProject)}`);
        await populateProjectDropdowns();
        hideProjectDetail();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Delete failed.';
    }
}

// epSaveNewProject creates the new project and then POSTs each staged
// component one by one. On success the view transitions to the
// existing-project detail for the newly created project so the user
// can keep adding or editing components without going back to the list.
//
// POST /api/projects
//   Request body: { name }
//   Admin-only.
//
// POST /api/projects/{project}/components  (once per staged component)
//   Request body: { name }
async function epSaveNewProject() {
    const name = document.getElementById('ep-project-name').value.trim();
    const err  = document.getElementById('ep-detail-error');
    const btn  = document.getElementById('ep-create-btn');
    err.textContent = '';

    if (!name) { err.textContent = 'Project name is required.'; return; }
    if (_projectData.some(p => p.name.toLowerCase() === name.toLowerCase())) {
        err.textContent = `Project "${name}" already exists.`;
        return;
    }

    btn.disabled = true;
    btn.textContent = 'Creating…';
    try {
        await apiPost('/api/projects', { name });
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to create project.';
        btn.disabled = false;
        btn.textContent = 'Create Project';
        return;
    }

    // Project created. Now POST each staged component. We collect failures
    // rather than aborting on the first error so the user can see which
    // components didn't make it and retry them.
    const failures = [];
    for (const comp of _epPendingComponents) {
        try {
            await apiPost(`/api/projects/${encodeURIComponent(name)}/components`, { name: comp });
        } catch (e) {
            if (e.message === 'Unauthorized') { btn.disabled = false; btn.textContent = 'Create Project'; return; }
            failures.push(comp);
        }
    }

    await populateProjectDropdowns();

    // Transition to the existing-project detail view so the user can
    // continue editing without navigating back.
    _epProject = name;
    _epPendingComponents = failures;
    document.getElementById('ep-detail-title').textContent         = name;
    document.getElementById('ep-name-group').style.display         = 'none';
    document.getElementById('ep-delete-project-btn').style.display = '';
    document.getElementById('ep-create-btn').style.display         = 'none';
    epRenderComponents();
    epRenderPending();
    if (failures.length > 0) {
        err.textContent = `Project created, but ${failures.length} component(s) could not be added.`;
    }
    btn.disabled = false;
    btn.textContent = 'Create Project';
}

// =====================================================================
// BACKDROP / MENU / SETTINGS
// =====================================================================

// backdropClick is attached to the overlay divs' onclick handlers. If
// the user clicked the dark backdrop (not the white sheet inside it),
// event.target will be the overlay div itself and we close it. Clicking
// inside the sheet does not match because event.target will be a child
// element, not the overlay div.
function backdropClick(event, overlayId, hideFn) {
    if (event.target.id === overlayId) hideFn();
}

// toggleMenu opens or closes the hamburger (☰) dropdown. To close
// the menu when the user clicks anywhere else on the page we register
// a one-time click listener on document. The { once: true } option
// causes the listener to remove itself automatically after it fires
// once — no manual cleanup required.
function toggleMenu(event) {
    event.stopPropagation();
    const menu = document.getElementById('app-menu');
    const opening = menu.style.display === 'none';
    menu.style.display = opening ? '' : 'none';
    if (opening) {
        document.addEventListener('click', _closeMenuOnOutside, { once: true });
    }
}

// _closeMenuOnOutside hides the menu. Called by the one-time document
// click listener from toggleMenu() and also called directly by functions
// that open an overlay so the menu doesn't remain visible behind it.
function _closeMenuOnOutside() {
    const menu = document.getElementById('app-menu');
    if (menu) menu.style.display = 'none';
}

// openAbout shows the About overlay and fetches the current version info.
//
// GET /api/version
//   Response: { version: "1.0-8", build_time: "20250515143200" }
//   build_time is a 14-character compact UTC timestamp: YYYYMMDDHHmmSS.
async function openAbout() {
    _closeMenuOnOutside();
    document.getElementById('about-overlay').style.display = 'flex';
    try {
        const data = await fetch('/api/version').then(r => r.json());
        document.getElementById('about-version').textContent = 'version ' + (data.version || '—');
        const bt = data.build_time || '';
        if (bt.length === 14) {
            // Manually parse the compact timestamp into a readable UTC string.
            // slice(start, end) extracts a substring: "20250515" → "2025","05","15"
            const ts = `${bt.slice(0,4)}-${bt.slice(4,6)}-${bt.slice(6,8)} ${bt.slice(8,10)}:${bt.slice(10,12)}:${bt.slice(12,14)} UTC`;
            document.getElementById('about-build').textContent = 'built ' + ts;
        } else {
            document.getElementById('about-build').textContent = bt ? 'built ' + bt : '';
        }
    } catch {
        document.getElementById('about-version').textContent = 'version —';
        document.getElementById('about-build').textContent = '';
    }
}

function hideAbout() {
    document.getElementById('about-overlay').style.display = 'none';
}

// openSettings syncs all three toggle controls to the current state
// variables before showing the overlay, so the checkboxes always
// reflect the true current preferences.
function openSettings() {
    _closeMenuOnOutside();
    document.getElementById('dark-mode-toggle').checked = _darkMode;
    document.getElementById('keep-logged-in-toggle').checked = _keepLoggedIn;
    document.getElementById('desktop-mode-toggle').checked = _desktopMode;
    const psSel = document.getElementById('page-size-select');
    if (psSel) psSel.value = String(_pageSize);
    document.getElementById('settings-overlay').style.display = 'flex';
}

function hideSettings() {
    document.getElementById('settings-overlay').style.display = 'none';
}

// toggleDesktopMode enables or disables the "Always show desktop version"
// setting. It adds or removes the 'desktop-mode' class from the <html>
// element (document.documentElement). Every responsive CSS rule in
// idtrack.css is scoped to 'html:not(.desktop-mode)', so adding this
// class makes all mobile/tablet layout overrides inert — the page
// renders identically to a desktop browser regardless of screen width.
//
// A separate minified inline <script> in the HTML <head> reads this
// preference and applies the class before the browser renders the first
// frame, preventing any flash of mobile layout on page reload.
function toggleDesktopMode(on) {
    _desktopMode = on;
    document.documentElement.classList.toggle('desktop-mode', on);
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.desktopMode = on;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
}

// toggleDarkMode adds or removes the 'dark' class from <body>. All dark
// mode color overrides in idtrack.css use the 'body.dark' selector, so
// adding this class is all that's needed to switch themes.
function toggleDarkMode(on) {
    _darkMode = on;
    document.body.classList.toggle('dark', on);
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.darkMode = on;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
}

// toggleKeepLoggedIn controls whether the next login will request a
// 30-day session cookie and persist the user display object to
// localStorage for automatic session restoration. This does not affect
// the current session; it only changes the behavior of the next login.
//
// If turned on while already logged in, the user object is written to
// localStorage immediately so a future browser session can restore the
// display state without re-entering credentials (the auth is carried by
// the long-lived session cookie the server set at login time).
function toggleKeepLoggedIn(on) {
    _keepLoggedIn = on;
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.keepLoggedIn = on;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
    if (on && _currentUser) {
        // Store the non-sensitive user object so init() can restore the display
        // state if the persistent session cookie is still valid on next visit.
        localStorage.setItem(PERSIST_KEY, JSON.stringify({ user: _currentUser }));
    } else if (!on) {
        localStorage.removeItem(PERSIST_KEY);
    }
}

// ── Idle timeout ──────────────────────────────────────────────────────────────
// The server communicates the configured idle-logout duration via
// GET /api/status ('idle_timeout' in seconds; 0 = disabled). Enforcement
// is entirely client-side: a setTimeout fires after the timeout, and any
// detected user activity resets the timer.

// idleLogout is called when the inactivity timer fires. It immediately
// hides the app and shows the login screen before the async /api/logout
// network call completes, so the user never sees the issue list after
// timing out even on a slow connection.
async function idleLogout() {
    stopIdleTracking();
    stopPolling();
    dismissRefreshHint();
    _currentUser = null;
    _issueWindow = [];
    _totalIssues = 0;
    _detailDirty = false;
    closeDetail();
    document.getElementById('app').style.display = 'none';
    showLogin('You have been signed out due to inactivity.');
    sessionStorage.removeItem(SESSION_KEY);
    localStorage.removeItem(PERSIST_KEY);
    _keepLoggedIn = false;
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.keepLoggedIn = false;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
    // Tell the server to invalidate the session after the UI is already clean.
    try { await fetch('/api/logout', { method: 'POST' }); } catch {}
}

// _resetIdleTimer cancels the current timer and starts a fresh one.
// This function is registered as a passive event listener for several
// user-activity events so every interaction pushes the logout deadline
// further into the future.
function _resetIdleTimer() {
    if (!_idleTimeoutSecs) return;
    if (_idleTimer) clearTimeout(_idleTimer);
    _idleTimer = setTimeout(() => idleLogout(), _idleTimeoutSecs * 1000);
}

// startIdleTracking attaches _resetIdleTimer as a listener to the
// listed events and starts the initial countdown. 'passive: true' tells
// the browser that these listeners will never call preventDefault(),
// allowing the browser to optimize scrolling and touch handling.
function startIdleTracking() {
    if (!_idleTimeoutSecs) return;
    const events = ['mousemove', 'mousedown', 'keydown', 'touchstart', 'scroll', 'click'];
    events.forEach(ev => document.addEventListener(ev, _resetIdleTimer, { passive: true }));
    _resetIdleTimer();
}

// stopIdleTracking cancels the pending timer and removes all activity
// listeners. Called on any logout (manual or idle) and at the start of
// launchApp() before re-registering, to avoid accumulating duplicate
// listeners across multiple logins.
function stopIdleTracking() {
    if (_idleTimer) { clearTimeout(_idleTimer); _idleTimer = null; }
    const events = ['mousemove', 'mousedown', 'keydown', 'touchstart', 'scroll', 'click'];
    events.forEach(ev => document.removeEventListener(ev, _resetIdleTimer));
}

// =====================================================================
// BACKGROUND POLLING
// =====================================================================

// startPolling begins 30-second background polling for changes made by
// other users. Changes found in the window are applied in-place; new or
// externally modified issues trigger the refresh hint toast.
function startPolling() {
    stopPolling();
    _pollTimer = setInterval(pollForChanges, 30000);
}

// stopPolling cancels the background polling interval.
function stopPolling() {
    if (_pollTimer) { clearInterval(_pollTimer); _pollTimer = null; }
}

// pollForChanges is called every 30 seconds by the setInterval timer. It
// asks the server for any issues updated after _lastSeenAt and handles them:
//
//   • Issues already in _issueWindow are updated in-place (same technique as
//     doSaveIssue: replace the JS object in the array and swap the DOM row).
//     We skip the currently-open issue so we don't clobber a user's in-progress
//     edits with a change another user just made.
//
//   • Issues not in the window (new issues, or issues on pages not yet loaded)
//     are counted. If any are found we show the refresh-hint toast so the user
//     can choose to reload the list without losing their scroll position.
//
//   • _lastSeenAt is advanced after each batch so the next poll only requests
//     changes that occurred since this poll ran.
//
// Errors are silently swallowed — a network blip shouldn't pop an alert; the
// next poll will pick up any missed changes. 401 Unauthorized (expired session)
// is handled centrally by apiGet().
async function pollForChanges() {
    if (!_lastSeenAt) return;
    try {
        const data = await apiGet('/api/issues/changes?since=' + encodeURIComponent(_lastSeenAt));
        const changed = data.issues || [];
        if (changed.length === 0) return;
        let externalChanges = 0;
        for (const iss of changed) {
            const idx = _issueWindow.findIndex(i => i.id === iss.id);
            if (idx !== -1) {
                // Already in the window: update in-place unless the user is
                // currently editing it (don't stomp unsaved changes).
                if (iss.id !== _currentId) {
                    _issueWindow[idx] = iss;
                    const tr = document.querySelector(`#issues-tbody tr[data-id="${iss.id}"]`);
                    if (tr) tr.outerHTML = issueRow(iss);
                }
            } else {
                externalChanges++;
            }
            if (iss.updated_at > (_lastSeenAt || '')) _lastSeenAt = iss.updated_at;
        }
        if (externalChanges > 0) {
            showRefreshHint(externalChanges + ' new or updated issue' + (externalChanges === 1 ? '' : 's') + ' available.');
        }
    } catch (e) {
        // Silently ignore: network blips, session expiry handled elsewhere.
    }
}

// showRefreshHint displays the fixed-position toast at the bottom of the
// screen informing the user that new issues are available outside the current
// window. The toast has two buttons: "Refresh" (calls applyRefreshHint) and
// "✕" (calls dismissRefreshHint).
function showRefreshHint(msg) {
    _refreshHintOn = true;
    const el = document.getElementById('refresh-hint');
    const txt = document.getElementById('refresh-hint-text');
    if (txt) txt.textContent = msg;
    if (el) el.style.display = 'flex';
}

// dismissRefreshHint hides the toast without reloading. The user has
// acknowledged that there may be unseen changes but chosen not to reload now.
function dismissRefreshHint() {
    _refreshHintOn = false;
    const el = document.getElementById('refresh-hint');
    if (el) el.style.display = 'none';
}

// applyRefreshHint is called when the user clicks "Refresh" in the toast.
// It hides the toast and reloads the full issue window from the server.
async function applyRefreshHint() {
    dismissRefreshHint();
    await loadIssueWindow();
}

// setPageSize updates the page size, persists it, and reloads the window.
function setPageSize(val) {
    if (![10,25,50,100,200].includes(val)) return;
    _pageSize = val;
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.pageSize = val;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
    loadIssueWindow();
}

// loadPrefs reads the saved user preferences from localStorage and
// applies them immediately. Called at the very start of init() before
// any network requests, so dark mode and desktop mode are active before
// the first visible render.
//
// Note: the 'desktop-mode' class on <html> is also set by a minified
// inline <script> in the HTML <head> that runs even earlier. That
// script prevents the flash of mobile layout that would otherwise occur
// between page load and this function running. loadPrefs() re-applies
// the class here to keep the JS state variables (_darkMode, _desktopMode)
// in sync with what is already visible.
function loadPrefs() {
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY));
        if (p) {
            if (p.darkMode) {
                _darkMode = true;
                document.body.classList.add('dark');
            }
            if (p.keepLoggedIn) {
                _keepLoggedIn = true;
            }
            if (p.desktopMode) {
                _desktopMode = true;
                // Class already set by the <head> inline script; no-op if already present.
                document.documentElement.classList.add('desktop-mode');
            }
            if (p.pageSize && [10,25,50,100,200].includes(p.pageSize)) {
                _pageSize = p.pageSize;
            }
        }
    } catch {}
}

// mainLayoutClick is the onclick handler for the main layout container
// (the area that holds both the issue list and the detail panel). If
// the user clicks on the empty space around the list — not on an issue
// row and not inside the detail panel — we close the detail panel.
// This gives a natural "click away to close" behavior on desktop.
function mainLayoutClick(event) {
    const detail = document.getElementById('detail-panel');
    if (!detail || detail.style.display === 'none') return;
    if (detail.contains(event.target)) return;
    if (event.target.closest('.issue-row')) return;
    closeDetail();
}

// =====================================================================
// UI — ONBOARDING
// =====================================================================
// Onboarding runs exactly once: when the database has no users. The
// server detects this in GET /api/status and returns { onboarding: true,
// token: "<uuid>" }. The client uses that one-time token as a Basic auth
// credential when creating the first admin account. Once the account is
// created the token is cleared from server memory.

let _onboardingToken = null;

// showOnboarding stores the token and opens the first-run account
// creation form.
function showOnboarding(token) {
    _onboardingToken = token;
    document.getElementById('ob-username').value      = '';
    document.getElementById('ob-display-name').value  = '';
    document.getElementById('ob-pass').value           = '';
    document.getElementById('ob-confirm').value        = '';
    document.getElementById('onboarding-error').textContent = '';
    document.getElementById('onboarding-overlay').style.display = 'flex';
    document.getElementById('ob-username').focus();
}

// submitOnboarding creates the first admin account. It uses HTTP Basic
// authentication with the one-time token rather than a session cookie,
// because no user account or session exists yet.
//
// POST /api/onboarding
//   Authorization: Basic base64("onboarding:<uuid-token>")
//   Content-Type:  application/json
//   Request body:  { username, display_name, password }
//   Response:      { username, display_name, is_admin: true }
//   Side effect:   server sets a session cookie, just like /api/login.
//   Returns 409 Conflict if any users already exist.
async function submitOnboarding() {
    const username    = document.getElementById('ob-username').value.trim().toLowerCase();
    const displayName = document.getElementById('ob-display-name').value.trim();
    const password    = document.getElementById('ob-pass').value;
    const confirm     = document.getElementById('ob-confirm').value;
    const err         = document.getElementById('onboarding-error');
    const btn         = document.getElementById('ob-submit-btn');

    err.textContent = '';
    if (!username)              { err.textContent = 'Username is required.'; return; }
    if (!password)              { err.textContent = 'Password is required.'; return; }
    if (password !== confirm)   { err.textContent = 'Passwords do not match.'; return; }

    btn.disabled = true;
    btn.textContent = 'Creating…';

    try {
        // btoa() encodes a string to Base64, which is the format that
        // the HTTP Basic Authentication scheme requires.
        const tokenCreds = 'Basic ' + btoa('onboarding:' + _onboardingToken);

        const res = await fetch('/api/onboarding', {
            method: 'POST',
            headers: {
                'Authorization': tokenCreds,
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ username, display_name: displayName, password }),
        });

        if (!res.ok) {
            let msg = 'Failed to create account.';
            try { const d = await res.json(); msg = d.error || msg; } catch {}
            err.textContent = msg;
            return;
        }

        const user = await res.json();
        _onboardingToken = null;
        document.getElementById('onboarding-overlay').style.display = 'none';

        // The server set a session cookie in the response. Store the
        // user display object so the app shell can render without an
        // extra round-trip.
        _currentUser = { username: user.username, display_name: user.display_name || username, is_admin: true };
        sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user: _currentUser }));

        await launchApp();

    } catch (e) {
        err.textContent = e.message || 'Failed to create account.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Create Account';
    }
}

// =====================================================================
// INITIALIZATION
// =====================================================================

// init() is the application entry point. It runs once when the page
// finishes loading (see the DOMContentLoaded listener at the bottom).
// The startup sequence is:
//
//  1. Load preferences (dark mode, keep-me-logged-in, desktop mode).
//  2. GET /api/status — get idle timeout, custom branding, and whether
//     first-run onboarding is needed.
//  3. Check sessionStorage for a live in-tab session (survives refresh,
//     cleared when the tab closes).
//  4. Check localStorage for a persisted "keep me logged in" session
//     (survives closing the browser entirely; auth via 30-day cookie).
//  5. If no session exists but onboarding is needed, show the first-run
//     account creation form.
//  6. Otherwise show the standard login screen.
async function init() {
    loadPrefs();

    // GET /api/status
    //   Always called without authentication — it is the very first
    //   request on every page load.
    //   Response: { idle_timeout: N, onboarding: bool, token: "uuid",
    //               app_name: "...", app_description: "..." }
    //   'onboarding' and 'token' are only present when no users exist.
    let statusData = null;
    try {
        const res = await fetch('/api/status');
        if (res.ok) statusData = await res.json();
    } catch {}
    if (statusData) {
        if (statusData.idle_timeout)    _idleTimeoutSecs = statusData.idle_timeout;
        if (statusData.app_name)        _appName = statusData.app_name;
        if (statusData.app_description) _appDesc = statusData.app_description;
    }
    applyBranding();

    // sessionStorage holds the user object for the life of the current
    // browser tab. The session cookie is HttpOnly so JS can't read it,
    // but the browser sends it automatically — launchApp() will surface
    // a 401 on the first API call if the cookie has since expired.
    const saved = sessionStorage.getItem(SESSION_KEY);
    if (saved) {
        try {
            const { user } = JSON.parse(saved);
            if (user && user.username) {
                _currentUser = user;
                await launchApp();
                return;
            }
        } catch {}
    }

    // localStorage holds the user object across browser sessions when
    // "Keep me logged in" is enabled. The actual credential is the
    // 30-day session cookie the server issued at login.
    const persist = localStorage.getItem(PERSIST_KEY);
    if (persist) {
        try {
            const { user } = JSON.parse(persist);
            if (user && user.username) {
                _currentUser = user;
                sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user }));
                // launchApp() will surface a 401 and call showLogin() if
                // the 30-day cookie has expired.
                await launchApp();
                return;
            }
        } catch {}
    }

    // No active session — decide whether to show onboarding or login.
    if (statusData && statusData.onboarding) {
        showOnboarding(statusData.token);
        return;
    }

    showLogin();
}

// DOMContentLoaded fires when the HTML has been fully parsed and all
// elements exist in the DOM — the correct moment to start the app.
// Using this event (rather than putting <script> at the very end of
// <body>) makes the startup trigger explicit and is the standard pattern
// for JavaScript-driven single-page applications.
document.addEventListener('DOMContentLoaded', init);
