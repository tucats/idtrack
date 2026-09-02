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

// BASE_PATH is a sentinel the server rewrites at request time (serveJS in
// server/static.go) when a --base-path is configured, e.g. '/idtrack'. Left
// as '' here, it is a no-op prefix, which is why every API call below is
// written as BASE_PATH + '/api/...' rather than a bare '/api/...' literal.
const BASE_PATH = '';

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
// _lastSeenAt   — the polling cursor sent to the server as "give me anything
//                 newer than this." Set to the current time when the window
//                 loads (see loadIssueWindow()), NOT the max updated_at within
//                 _issueWindow — that window is only the current filter/sort's
//                 first page, so its max timestamp is often much older than
//                 "now" (e.g. sorted by a column other than Updated, or
//                 filtered to exclude a recently-touched issue), while
//                 GET /api/issues/changes ignores the active filter entirely.
//                 Using the window's max as the cursor previously caused
//                 pollForChanges() to report unrelated, already-existing
//                 issues as "new changes" on every poll. It is still
//                 ratcheted forward (never backward) by _updateLastSeenAt()
//                 as pages load, as a safety net against client/server clock
//                 skew.
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

// Column visibility — which optional columns are shown in the issue table.
// Keys correspond to the CSS class suffix (col-X) and the hide-col-X classes
// toggled on <html> by applyColVisibility(). Defaults preserve the historic
// column set; the two new columns (resolved, comments) start hidden.
const _colVisibilityDefaults = {
    project:   true,
    component: true,
    status:    true,
    priority:  true,
    reporter:  false,
    assignee:  true,
    date:      true,   // "Created" column — CSS class is col-date
    resolved:  false,
    comments:  false,
};
// `{ ..._colVisibilityDefaults }` uses the object "spread" syntax to copy
// every key/value pair out of _colVisibilityDefaults into a brand-new
// object. Without the spread, `let _colVisibility = _colVisibilityDefaults`
// would make both names point at the *same* object, so later code that
// mutates _colVisibility (toggleColumn) would also silently corrupt the
// "defaults" object. The spread gives _colVisibility its own independent
// copy to mutate, loaded over with any saved preference in loadPrefs().
let _colVisibility = { ..._colVisibilityDefaults };

// The id of the issue currently open in the detail panel. null = none open.
let _currentId         = null;

// The dependent_issues field of the currently-open issue, kept in sync with
// the detail panel form. For Duplicate status this holds exactly one ID; for
// Blocked status it holds one or more. Empty for all other statuses.
let _dependentIssues = [];

// Every image attachment on the currently-open issue — both on its
// description (comment_id absent) and on any of its comments (comment_id
// present). Refetched from the server on every selectIssue() and after
// every upload/delete rather than mutated in place, so it never drifts from
// the database. renderAllAttachments() splits this one list out into the
// description's thumbnail row and each comment's own thumbnail row.
let _detailAttachments = [];

// The id of the attachment currently shown in the full-size viewer overlay,
// or null when the overlay is closed. Needed by confirmDeleteAttachment()
// since the overlay only has a bare "Delete" button, not the id itself.
let _avAttachmentId = null;

// Staging list of blocking issue IDs assembled inside the Blocked dialog
// before the user confirms. Separate from _dependentIssues so cancelling
// the dialog leaves the form in its original state.
let _pendingBlockedIds = [];

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
let _darkModePref   = 'off'; // 'off' | 'on' | 'auto' — the saved setting
let _darkMode       = false;  // body.dark CSS class active (resolved from _darkModePref)
let _keepLoggedIn   = false;  // request 30-day session cookie on next login
let _desktopMode    = false;  // html.desktop-mode class active (disables RWD CSS)
let _usePasskeys    = true;   // client-side "Use passkeys" preference (default on); see toggleUsePasskeys()

// Whether THIS server instance has passkey login turned on at all — from
// GET /api/status's webauthn_enabled field (set in init()). Server-off
// always wins over the _usePasskeys preference above: every passkey UI
// element checks both.
let _webauthnEnabled = false;

// Idle-logout state. The timeout value comes from GET /api/status.
// 0 means the feature is disabled.
let _idleTimeoutSecs = 0;
let _idleTimer       = null;  // handle returned by setTimeout(), kept so we can cancel it

// Application branding. These defaults are overridden by GET /api/status
// if the operator has set custom values via 'idtrack default --app-name'.
let _appName         = 'idtrack';
let _appDesc         = 'Issue Tracker';

// Canonical team list from GET /api/teams. Shape: [{ name, description }, ...]
let _teamData        = [];

// Currently-edited team name in the manage-teams detail screen (null = new).
let _mtTeam          = null;

// Per-context team chip arrays for the four chip pickers.
let _auTeams         = [];   // add-user overlay
let _euTeams         = [];   // edit-user overlay
let _epTeams         = [];   // edit-project overlay
let _detailTeams     = [];   // issue detail panel

// =====================================================================
// BRANDING
// =====================================================================

// applyBranding() updates every place in the UI that shows the application
// name or description. It is called once during init() after GET /api/status
// returns, and again never — the values don't change while the page is open.
//
// The inner setText helper silently skips elements that don't exist yet,
// which makes it safe to call before the app shell is visible.
//
// setText is an "arrow function" — a shorter syntax for writing small
// functions: `(params) => expression-or-block`. It behaves like a normal
// function here (this file doesn't rely on arrow functions' other
// difference, that they don't have their own `this`); think of it as a
// compact, unnamed function assigned to a local variable, handy for
// tiny one-off helpers like this that are only used inside applyBranding.
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
// TEAMS — loading, datalist, chip picker helpers
// =====================================================================

// loadTeamData fetches the canonical team list from the server and
// updates the shared datalist so all chip-picker inputs get autocomplete.
async function loadTeamData() {
    try {
        const data = await fetch(BASE_PATH + '/api/teams').then(r => r.json());
        _teamData = data.teams || [];
    } catch {
        _teamData = [];
    }
    updateTeamDatalist();
}

// teamNames returns just the name strings from _teamData.
//
// .map() is an Array.prototype method: it walks the array, calls the
// given function once per element, and builds a brand-new array from
// the return values — it does NOT change _teamData itself. This
// "functional" style (map/filter/join/some/find, used throughout this
// file) is different from writing a manual for-loop that pushes into
// an array or mutates one in place; get used to reading `.map(x => ...)`
// as "produce a new array, transforming each x".
function teamNames() {
    return _teamData.map(t => t.name);
}

// updateTeamDatalist rebuilds the shared <datalist id="team-names-dl">
// so every team chip picker input gets the same autocomplete options.
//
// The string inside backticks below is a "template literal" — text
// wrapped in ` ` (backticks) instead of quotes, which lets you embed
// live JavaScript expressions using ${...}. It's the primary way this
// file builds HTML: assemble a string (often via .map(...).join('')
// to turn an array into one big concatenated string), then assign it
// to an element's .innerHTML. That REPLACES all of the element's
// existing markup with the new string — there's no framework doing
// incremental "diffing" here, so any child DOM nodes / event listeners
// inside it are thrown away and recreated from scratch. Because
// .innerHTML parses its string as real HTML, any value that came from
// user input (a team name, a title, etc.) MUST be passed through esc()
// first — otherwise a team named `<script>...</script>` would execute
// as code for every user who sees this datalist (XSS).
function updateTeamDatalist() {
    const dl = document.getElementById('team-names-dl');
    if (dl) dl.innerHTML = _teamData.map(t => `<option value="${esc(t.name)}">`).join('');
}

// renderTeamChips rebuilds a chip container (e.g. #au-teams-chips) from an
// array of team names — the visual "chips" (small pill buttons) seen next
// to add/edit-user, edit-project, and issue-detail team pickers.
//   containerId — id of the element whose innerHTML gets replaced
//   teams       — array of team-name strings to render, in order
//   prefix      — which picker this is ('au', 'eu', 'ep', 'detail'); used
//                 to build the onclick attribute string so each chip's ×
//                 button calls the right per-picker remove function
//                 (auRemoveTeam, euRemoveTeam, etc. — see below)
//   editable    — when true, each chip gets a × remove button; when false
//                 (read-only contexts) the chip is just a label
// The .map((t, i) => ...) callback takes both the array element (t) and
// its index (i) — the index is what lets the × button know which chip to
// splice out later. Every dynamic value (t, i, prefix) is either escaped
// with esc() or is a value this code itself controls (never inserted
// straight from user input unescaped).
function renderTeamChips(containerId, teams, prefix, editable) {
    const container = document.getElementById(containerId);
    if (!container) return;
    container.innerHTML = teams.map((t, i) => {
        const cls = t === 'admin' ? ' chip-admin' : t === 'any' ? ' chip-any' : '';
        const removeBtn = editable
            ? `<button class="chip-remove" onclick="${prefix}RemoveTeam(${i})" title="Remove">&#215;</button>`
            : '';
        return `<span class="team-chip${cls}">${esc(t)}${removeBtn}</span>`;
    }).join('');
}

// addTeamChip reads the input for 'prefix', validates, adds to the correct
// team array, and re-renders the chips. Called when the user types a team
// name (autocompleted from #team-names-dl, populated by updateTeamDatalist)
// and presses Enter or clicks "Add" next to one of the four chip pickers.
// Names are lower-cased and de-duplicated before being added. If this is
// the issue-detail picker ('detail'), markDetailDirty() flags the form so
// the Save button becomes active.
function addTeamChip(prefix) {
    const input = document.getElementById(`${prefix}-teams-input`);
    if (!input) return;
    const name = input.value.trim().toLowerCase();
    if (!name) return;
    const arr = _teamArrayFor(prefix);
    if (!arr.includes(name)) {
        arr.push(name);
        renderTeamChips(`${prefix}-teams-chips`, arr, prefix, true);
        if (prefix === 'detail') markDetailDirty();
    }
    input.value = '';
    input.focus();
}

// _teamArrayFor returns a reference to the correct per-context array.
function _teamArrayFor(prefix) {
    switch (prefix) {
        case 'au':     return _auTeams;
        case 'eu':     return _euTeams;
        case 'ep':     return _epTeams;
        case 'detail': return _detailTeams;
    }
    return [];
}

// Per-prefix remove functions called from chip × buttons.
function auRemoveTeam(i)     { _auTeams.splice(i,1);     renderTeamChips('au-teams-chips',     _auTeams,     'au',     true); }
function euRemoveTeam(i)     { _euTeams.splice(i,1);     renderTeamChips('eu-teams-chips',     _euTeams,     'eu',     true); }
function epRemoveTeam(i)     { _epTeams.splice(i,1);     renderTeamChips('ep-teams-chips',     _epTeams,     'ep',     true); }
function detailRemoveTeam(i) { _detailTeams.splice(i,1); renderTeamChips('detail-teams-chips', _detailTeams, 'detail', true); markDetailDirty(); }

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

// statusBadge works exactly like priorityBadge but for issue status.
// Each status maps to a distinct colour so issues are scannable at a glance.
function statusBadge(s) {
    const cls = { Open: 'badge-open', Resolved: 'badge-resolved',
                  Blocked: 'badge-blocked', Duplicate: 'badge-duplicate' }[s] || 'badge-resolved';
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
//
// fetch() is the modern browser API for making HTTP requests; it's a
// built-in global, not something this codebase defines. It returns a
// Promise that resolves once the response headers arrive — you then
// call .json() (also Promise-based) to read and parse the body. This
// replaces the older, callback-based XMLHttpRequest API you may see in
// legacy JS code; fetch + async/await is what lets the code below read
// top-to-bottom like synchronous code instead of nesting callbacks.

// apiFetch is the lowest-level wrapper. It calls the browser's built-in
// fetch() with whatever options the caller provides, intercepts 401s,
// and returns the raw Response object for higher-level wrappers to parse.
//
// `options = {}` is a "default parameter": if the caller omits the
// second argument entirely, options is set to a fresh empty object
// instead of being undefined. This lets callers write apiFetch(url)
// when they don't need to customize the request (method, headers, body).
async function apiFetch(url, options = {}) {
    const res = await fetch(BASE_PATH + url, options);

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

// apiUpload sends a POST request with a FormData (multipart) body — used
// for attachment uploads. Unlike apiPost, no Content-Type header is set
// here: the browser fills it in itself (including the multipart boundary
// string), which fetch can't replicate by hand.
async function apiUpload(url, formData) {
    const res = await apiFetch(url, { method: 'POST', body: formData });
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

// currentFilterParams returns a URLSearchParams populated with the active
// status/priority/project/search filters (not sort or pagination), for
// fetchIssuePage(). NOT used by pollForChanges() — the changes endpoint is
// deliberately unfiltered by these fields (see its doc comment and
// matchesCurrentFilters()) so the client can detect issues that just left
// the active filter, not only ones that still match it.
function currentFilterParams() {
    const params = new URLSearchParams();
    if (_statusFilter !== 'all')   params.set('status',   _statusFilter);
    if (_priorityFilter !== 'all') params.set('priority', _priorityFilter);
    if (_projectFilter !== 'all')  params.set('project',  _projectFilter);
    const q = (document.getElementById('search-input') || {}).value || '';
    if (q) params.set('search', q);
    return params;
}

// GET /api/issues
// fetchIssuePage fetches one page of issues from the server using the current
// filter/sort state. Returns { issues, total } where total is the count of all
// matching rows (not just this page). limit defaults to _pageSize.
//
// Response shape: { issues: [...], total: N, offset: N, limit: N }
async function fetchIssuePage(offset, limit) {
    limit = limit || _pageSize;
    const params = currentFilterParams();
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
async function createIssue(title, description, priority, assignee, project, component, format) {
    return apiPost('/api/issues', { title, description, priority, assignee, project, component, format });
}

// PUT /api/issues/{id}
// Replaces all mutable fields of an existing issue. Every field must
// be provided even if unchanged — this is a full replacement, not a
// partial update (PATCH). Only the reporter, current assignee, and
// admins may call this; others receive 403 Forbidden.
// Response shape: { issue: { ...updated issue... } }
async function updateIssue(id, title, description, priority, status, assignee, project, component, format, dependentIssues, comment, teams) {
    return apiPut(`/api/issues/${id}`, {
        title, description, priority, status, assignee, project, component, format,
        dependent_issues: dependentIssues || [],
        comment: comment || '',
        teams: teams || [],
    });
}

// POST /api/render
// Server-renders arbitrary text per format (using the same goldmark
// renderer as the saved issue view), for live-previewing unsaved
// description edits without duplicating markdown parsing in JavaScript.
// Response shape: { html: "..." }
async function renderPreview(format, text) {
    const data = await apiPost('/api/render', { format, text });
    return data.html || '';
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
    updatePasskeyLoginVisibility();
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
        const res = await fetch(BASE_PATH + '/api/login', {
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
// UI — PASSKEYS (WebAuthn: Touch ID / Face ID / Windows Hello / security keys)
// =====================================================================
// Two ceremonies mirror the two on the server (see server/webauthn.go):
// login (this section) establishes a brand new session the same way
// submitLogin() does; registration (further down, alongside the Settings
// toggles) adds a passkey to an already-logged-in account. Both ceremonies
// follow the same two-step shape: POST .../begin returns a challenge object
// the browser's WebAuthn API needs, the browser prompts the user for their
// fingerprint/face/PIN/security key and produces a signed response, and that
// response is POSTed to .../finish.
//
// Every field the WebAuthn API deals in — challenges, credential IDs, public
// keys, signatures — is a raw ArrayBuffer in the browser but base64url text
// over the wire (JSON has no binary type). bufferDecode/bufferEncode convert
// between the two; preformatGetOptions/preformatCreateOptions apply
// bufferDecode to the specific fields of a server response that need to
// become ArrayBuffers before being handed to navigator.credentials;
// credentialGetToJSON/credentialCreateToJSON apply bufferEncode to the
// specific fields of a navigator.credentials result that need to become
// strings before being JSON.stringify'd back to the server. This hand-rolled
// conversion (rather than relying on the newer PublicKeyCredential.toJSON()
// browser method, which not every browser idtrack might run in supports yet)
// only encodes/decodes the fields the go-webauthn library's parser actually
// reads — see server/webauthn.go's SessionData-handoff comment for exactly
// which fields those are and why a couple of Level-3-only convenience fields
// (authenticatorData/publicKey/publicKeyAlgorithm on the registration
// response) are intentionally omitted rather than reconstructed here.

// browserSupportsWebAuthn reports whether the current browser exposes the
// WebAuthn APIs at all. Passkey UI (the login button, the Settings toggle
// and management list) is only ever shown when this AND _webauthnEnabled
// (the server-side switch) are both true.
function browserSupportsWebAuthn() {
    return typeof window.PublicKeyCredential !== 'undefined' && !!navigator.credentials;
}

// bufferDecode converts a base64url string (no padding, '-'/'_' instead of
// '+'/'/') into an ArrayBuffer, as required by every byte-valued field the
// WebAuthn API expects (challenge, credential IDs, ...).
function bufferDecode(value) {
    let b64 = value.replace(/-/g, '+').replace(/_/g, '/');
    while (b64.length % 4) b64 += '=';
    const raw = atob(b64);
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    return bytes.buffer;
}

// bufferEncode is bufferDecode's inverse: an ArrayBuffer (or a typed-array
// view of one, as navigator.credentials results provide) to a base64url
// string with no padding.
function bufferEncode(buffer) {
    const bytes = new Uint8Array(buffer);
    let str = '';
    for (let i = 0; i < bytes.length; i++) str += String.fromCharCode(bytes[i]);
    return btoa(str).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// preformatCreateOptions mutates the PublicKeyCredentialCreationOptions JSON
// returned by POST .../register/begin in place, decoding every base64url
// byte field into the ArrayBuffer navigator.credentials.create() requires.
function preformatCreateOptions(pubKey) {
    pubKey.challenge = bufferDecode(pubKey.challenge);
    pubKey.user.id = bufferDecode(pubKey.user.id);
    if (pubKey.excludeCredentials) {
        pubKey.excludeCredentials = pubKey.excludeCredentials.map(c => ({ ...c, id: bufferDecode(c.id) }));
    }
    return pubKey;
}

// preformatGetOptions is preformatCreateOptions's counterpart for a login
// ceremony's PublicKeyCredentialRequestOptions JSON, used before
// navigator.credentials.get().
function preformatGetOptions(pubKey) {
    pubKey.challenge = bufferDecode(pubKey.challenge);
    if (pubKey.allowCredentials) {
        pubKey.allowCredentials = pubKey.allowCredentials.map(c => ({ ...c, id: bufferDecode(c.id) }));
    }
    return pubKey;
}

// credentialCreateToJSON serializes a navigator.credentials.create() result
// into the JSON shape POST .../register/finish expects (see
// server/webauthn.go — only clientDataJSON and attestationObject are
// actually read server-side; the rest is present because the standard
// WebAuthn response shape includes it).
function credentialCreateToJSON(cred) {
    return {
        id: cred.id,
        rawId: bufferEncode(cred.rawId),
        type: cred.type,
        clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
        response: {
            clientDataJSON: bufferEncode(cred.response.clientDataJSON),
            attestationObject: bufferEncode(cred.response.attestationObject),
        },
    };
}

// credentialGetToJSON is credentialCreateToJSON's counterpart for a
// navigator.credentials.get() (login) result.
function credentialGetToJSON(cred) {
    return {
        id: cred.id,
        rawId: bufferEncode(cred.rawId),
        type: cred.type,
        clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
        response: {
            clientDataJSON: bufferEncode(cred.response.clientDataJSON),
            authenticatorData: bufferEncode(cred.response.authenticatorData),
            signature: bufferEncode(cred.response.signature),
            userHandle: cred.response.userHandle ? bufferEncode(cred.response.userHandle) : undefined,
        },
    };
}

// updatePasskeyLoginVisibility shows or hides the "Sign in with a passkey"
// button on the login screen. Called from showLogin() (so it is re-checked
// every time the login screen is shown, e.g. after a logout) and from
// init() once the server status probe has set _webauthnEnabled.
function updatePasskeyLoginVisibility() {
    const row = document.getElementById('login-passkey-row');
    if (!row) return;
    row.style.display = (_webauthnEnabled && _usePasskeys && browserSupportsWebAuthn()) ? 'flex' : 'none';
}

// loginWithPasskey is the click handler for the login screen's "Sign in
// with a passkey" button. It runs a full discoverable-credential ceremony
// (see server/webauthn.go's handleWebAuthnLoginBegin/Finish) — the user
// picks which passkey to use from their platform's own prompt, with no
// username typed first — and, on success, follows the same success path
// submitLogin() uses (populate _currentUser, sessionStorage/localStorage,
// launchApp()).
async function loginWithPasskey() {
    const err = document.getElementById('login-error');
    err.textContent = '';

    try {
        // Public endpoint: raw fetch(), not apiPost(), for the same reason
        // submitLogin() above uses fetch() directly — there is no session
        // yet, so apiFetch's 401 handling would be misleading here.
        const beginRes = await fetch(BASE_PATH + '/api/webauthn/login/begin', { method: 'POST' });
        if (!beginRes.ok) { err.textContent = 'Passkey login is unavailable right now.'; return; }

        const data = await beginRes.json();
        const publicKey = preformatGetOptions(data.publicKey);

        const assertion = await navigator.credentials.get({ publicKey });
        if (!assertion) { err.textContent = 'Passkey login was cancelled.'; return; }

        const qs = new URLSearchParams({ ceremony: data.ceremony_id, keep: _keepLoggedIn ? 'true' : 'false' });
        const finishRes = await fetch(BASE_PATH + '/api/webauthn/login/finish?' + qs.toString(), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(credentialGetToJSON(assertion)),
        });

        if (!finishRes.ok) { err.textContent = 'Passkey login failed.'; return; }

        const user = await finishRes.json();
        _currentUser = { username: user.username, display_name: user.display_name, is_admin: !!user.is_admin };
        sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user: _currentUser }));
        if (_keepLoggedIn) localStorage.setItem(PERSIST_KEY, JSON.stringify({ user: _currentUser }));

        document.getElementById('login-overlay').style.display = 'none';
        await launchApp();

    } catch (e) {
        // NotAllowedError covers both the user dismissing the platform
        // prompt and the ceremony timing out. It's not really an "error" in
        // the sense the other branch's message implies, but staying
        // completely silent here was a mistake — from the user's side it
        // looked exactly like a hang, with nothing in the console or the
        // server log to explain it (the request to login/finish was never
        // even sent, since the ceremony failed client-side before that).
        // A gentler, distinct message at least confirms something happened
        // and there's nothing to debug.
        if (e && e.name === 'NotAllowedError') {
            err.textContent = 'Passkey sign-in was cancelled or timed out.';
        } else {
            err.textContent = 'Passkey login failed.';
        }
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
    document.getElementById('menu-edit-teams').style.display     = adminDisplay;
    document.getElementById('menu-edit-projects').style.display  = adminDisplay;
    document.getElementById('app').style.display = '';
    // Reset the idle timer so it starts fresh from this login event.
    stopIdleTracking();
    startIdleTracking();
    await loadTeamData();
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
    try { await fetch(BASE_PATH + '/api/logout', { method: 'POST' }); } catch {}
    _currentUser = null;
    _issueWindow = [];
    _totalIssues = 0;
    _teamData = [];
    _detailTeams = [];
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
// COLUMN VISIBILITY
// =====================================================================

// applyColVisibility synchronises the html.hide-col-X CSS classes with the
// current _colVisibility state. Adding 'hide-col-project' to <html> causes
// the rule 'html.hide-col-project .col-project { display: none }' to hide
// every .col-project element (both <th> headers and <td> data cells) without
// touching the DOM structure at all — no re-render of the issue rows needed.
function applyColVisibility() {
    for (const [col, visible] of Object.entries(_colVisibility)) {
        document.documentElement.classList.toggle('hide-col-' + col, !visible);
    }
}

// toggleColumn is called by each checkbox in the column picker panel. It
// updates _colVisibility, applies the CSS change immediately, and persists
// the new state to localStorage so the preference survives page reloads.
function toggleColumn(col, checked) {
    _colVisibility[col] = checked;
    applyColVisibility();
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.colVisibility = _colVisibility;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
}

// toggleColPicker opens or closes the column picker dropdown. Uses the same
// { once: true } document click pattern as the hamburger menu so the panel
// closes automatically when the user clicks anywhere else on the page.
function toggleColPicker(event) {
    event.stopPropagation();
    _closeMenuOnOutside();   // close hamburger menu if it is open
    const panel = document.getElementById('col-picker-panel');
    if (!panel) return;
    const opening = panel.style.display === 'none';
    panel.style.display = opening ? '' : 'none';
    if (opening) {
        // Sync checkbox states to the current _colVisibility values so the
        // panel always reflects the truth even if prefs changed since it was
        // last opened.
        for (const col of Object.keys(_colVisibility)) {
            const chk = document.getElementById('colchk-' + col);
            if (chk) chk.checked = _colVisibility[col];
        }
        document.addEventListener('click', _closeColPickerOnOutside, { once: true });
    }
}

// _closeColPickerOnOutside hides the column picker panel. Called by the
// one-time document click listener from toggleColPicker().
function _closeColPickerOnOutside() {
    const panel = document.getElementById('col-picker-panel');
    if (panel) panel.style.display = 'none';
}

// =====================================================================
// UI — FILTERS & SORT
// =====================================================================
// Filters and sort are sent to the server on every change; the server
// handles all filtering, sorting, and pagination. Each change below
// resets the issue window and re-fetches from offset 0.

// setStatusFilter is the onchange handler for the status <select> in the
// filter bar. val is one of 'all' | 'open' | 'resolved' | 'blocked' |
// 'duplicate'. Updates the shared _statusFilter state and re-fetches the
// issue list from the server (offset 0) so the new filter takes effect —
// see currentFilterParams(), which reads _statusFilter on every fetch.
function setStatusFilter(val) {
    _statusFilter = val;
    loadIssueWindow();
}

// setPriorityFilter is the onchange handler for the priority <select>.
// val is one of 'all' | 'High' | 'Medium' | 'Low'. Same pattern as
// setStatusFilter: update state, then reload the issue window.
function setPriorityFilter(val) {
    _priorityFilter = val;
    loadIssueWindow();
}

// setProjectFilter is the onchange handler for the project <select>.
// val is 'all' or a project name from _projectData. Same pattern as
// setStatusFilter: update state, then reload the issue window.
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

// matchesCurrentFilters returns true if the given issue satisfies all of the
// currently active filters and the search query. Used after a save to decide
// whether an issue should stay in the list or be removed — for example, if
// the filter is "Open only" and the user just resolved the issue, the issue
// should disappear from the list immediately rather than remaining as a stale
// row. The logic mirrors what the server applies in db.buildWhereClause so
// the client and server always agree on which issues are visible.
function matchesCurrentFilters(issue) {
    if (_statusFilter !== 'all') {
        // Map the filter token (lowercase, from the <select>) to the server's
        // Title-cased status string stored in issue objects.
        const want = { open: 'Open', resolved: 'Resolved', blocked: 'Blocked', duplicate: 'Duplicate' }[_statusFilter];
        if (want && issue.status !== want) return false;
    }
    if (_priorityFilter !== 'all' && issue.priority !== _priorityFilter) return false;
    if (_projectFilter  !== 'all' && issue.project  !== _projectFilter)  return false;
    const q = ((document.getElementById('search-input') || {}).value || '').toLowerCase();
    if (q) {
        const hay = [issue.title, issue.description, issue.reporter,
                     issue.assignee, issue.project, issue.component].join(' ').toLowerCase();
        if (!hay.includes(q)) return false;
    }
    return true;
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
        // Seed the polling cursor to "now", not the loaded page's max
        // updated_at — see the _lastSeenAt comment above for why that
        // previously caused false-positive "new changes" toasts.
        _lastSeenAt = _nowRFC3339();
        _updateLastSeenAt(issues); // ratchet forward if server clock is ahead
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

// _nowRFC3339 returns the current time in the same textual format the server
// uses for updated_at (Go's time.RFC3339, no fractional seconds), so
// lexicographic comparisons against it stay valid. Date.toISOString() always
// includes milliseconds (e.g. "...45.123Z"); comparing that directly against
// a same-second server timestamp with no fractional part (e.g. "...45Z")
// would be WRONG — "." (0x2E) sorts before "Z" (0x5A), so the millisecond
// string would lexicographically compare as "less than" the whole-second one
// even though it represents a later instant. Stripping the milliseconds
// avoids that trap.
function _nowRFC3339() {
    return new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
}

// issueRow returns the HTML string for a single table row. It is built with
// a template literal — a string delimited by backticks (`) instead of quotes,
// which allows both multi-line text and ${expression} interpolation directly
// inside the string. Every ${...} slot below gets converted to text and
// spliced into the result. The data-id attribute embeds the issue id
// directly on the DOM element so we can later find and update a specific
// row with:
//   document.querySelector('#issues-tbody tr[data-id="42"]')
// without iterating over every row or keeping a separate id→element map.
// esc() (defined earlier in this file) is called on every user-supplied
// string before it goes into the template — it converts characters like
// < > & " into safe HTML entities. This matters because the resulting
// string is later assigned to .innerHTML, which the browser parses as real
// HTML: an unescaped issue title of `<script>evil()</script>` would
// actually execute in the page. Escaping first turns it into inert text.
function issueRow(issue) {
    const sel = issue.id === _currentId ? ' selected' : '';
    return `<tr class="issue-row${sel}" data-id="${issue.id}" onclick="selectIssue(${issue.id})">
        <td class="col-id">#${esc(String(issue.id))}</td>
        <td class="col-title issue-title-cell">${esc(issue.title)}</td>
        <td class="col-project">${esc(issue.project || '—')}</td>
        <td class="col-component">${esc(issue.component || '—')}</td>
        <td class="col-priority">${priorityBadge(issue.priority)}</td>
        <td class="col-status">${statusBadge(issue.status)}</td>
        <td class="col-reporter">${esc(issue.reporter ? displayName(issue.reporter) : '—')}</td>
        <td class="col-assignee">${esc(issue.assignee ? displayName(issue.assignee) : '—')}</td>
        <td class="col-date">${fmtDate(issue.created_at)}</td>
        <td class="col-resolved">${issue.resolved_at ? fmtDate(issue.resolved_at) : '—'}</td>
        <td class="col-comments">${issue.comment_count || 0}</td>
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
    // _issueWindow.map(issueRow) runs issueRow() over every issue object and
    // collects the returned HTML strings into a new array (one <tr>...</tr>
    // string per issue); .join('') concatenates that array into one long
    // string with nothing between the pieces. That single string is then
    // assigned to innerHTML, which asks the browser to parse it as HTML and
    // replace the tbody's entire contents in one shot. This "build a string,
    // then set innerHTML" approach is how this whole app renders — there is
    // no virtual DOM or diffing, just re-parsing fresh HTML from scratch.
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

        // The Format picker currently only offers "text" and "markdown" — if
        // an issue was created with "html" (e.g. via the API before that
        // option was hidden from the UI), inject a hidden option so its
        // value round-trips on save instead of silently reverting to "text".
        setDetailFormatValue(issue.format || 'text');
        initDescPreview(issue.format, issue.description_html);

        // Snapshot the dependent_issues and teams fields.
        _dependentIssues = issue.dependent_issues || [];
        // [...issue.teams] is the "spread" syntax: it copies every element of
        // issue.teams into a brand-new array. This matters because arrays in
        // JS are reference types — writing `_detailTeams = issue.teams`
        // (no spread) would make _detailTeams point at the very same array
        // object, so later edits via addTeamChip()/removeTeamChip() would
        // silently mutate the issue object too. Spreading breaks that link.
        _detailTeams = Array.isArray(issue.teams) ? [...issue.teams] : ['any'];

        const canEdit = canModifyIssue(issue);
        const isAdmin = _currentUser && _currentUser.is_admin;

        // Render teams chips; admins see the add/remove controls.
        renderTeamChips('detail-teams-chips', _detailTeams, 'detail', isAdmin);
        const teamsEditRow = document.getElementById('detail-teams-edit-row');
        if (teamsEditRow) teamsEditRow.style.display = isAdmin ? '' : 'none';

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
         'detail-assignee', 'detail-project', 'detail-component', 'detail-format', 'detail-desc']
            .forEach(id => { const el = document.getElementById(id); if (el) el.disabled = !canEdit; });
        // The Edit button lets a read-only viewer attempt to start editing
        // the description; hide it entirely rather than relying solely on
        // the disabled textarea to block entry. Preview stays available so
        // read-only viewers can still toggle back to the formatted view.
        document.getElementById('detail-desc-edit-btn').style.display = canEdit ? '' : 'none';

        renderDependentIssues(issue.status, canEdit);

        _detailDirty = false;

        renderComments(comments);
        document.getElementById('comment-input').value = '';
        initCommentToggle(issue.format);

        // The Add Image control follows the same open-to-any-authenticated-
        // user rule as commenting (not canEdit) — see handleCreateIssueAttachment's
        // doc comment in server/attachments.go.
        document.getElementById('detail-addimg-btn').style.display = _currentUser ? '' : 'none';
        document.getElementById('detail-image-dropzone').style.display = 'none';
        document.getElementById('detail-image-error').textContent = '';
        loadDetailAttachments(id);

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
    _detailAttachments = [];
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

// setDetailFormatValue selects the given format in #detail-format. If the
// value isn't one of the picker's options (currently only "text" and
// "markdown" are offered — "html" is supported end-to-end but hidden from
// the UI for now), a hidden option is injected so the value round-trips on
// save instead of silently reverting to "text".
function setDetailFormatValue(format) {
    const sel = document.getElementById('detail-format');
    // sel.options is not a real array (it's an HTMLOptionsCollection), so
    // [...sel.options] spreads it into a real array first. .some() then
    // returns true as soon as any element satisfies the callback (here:
    // any <option> whose value matches `format`) — it short-circuits rather
    // than checking every element once it finds a match, and returns a
    // plain boolean rather than the matching element itself.
    if (![...sel.options].some(o => o.value === format)) {
        const opt = document.createElement('option');
        opt.value = format;
        opt.textContent = format;
        opt.hidden = true;
        sel.appendChild(opt);
    }
    sel.value = format;
}

// The description field toggles between two mutually exclusive views for
// markdown/html-formatted issues: a raw-text textarea (edit mode) and a
// rendered HTML div (preview mode). Plain-text issues never show the
// toggle — the textarea is the only view, exactly as before this feature.
//
// initDescPreview sets up the field for a freshly loaded or saved issue:
// the Edit/Preview buttons appear only for non-text formats, and the field
// defaults to preview mode using the server-rendered HTML that already
// came back with the issue (no extra round-trip needed on open).
function initDescPreview(format, descriptionHtml) {
    const isFormatted = !!format && format !== 'text';
    document.getElementById('detail-desc-toggle').style.display = isFormatted ? '' : 'none';
    if (isFormatted) {
        showDescPreview(descriptionHtml || '');
    } else {
        showDescEdit();
    }
}

// showDescEdit displays the raw-text textarea and hides the rendered preview.
function showDescEdit() {
    document.getElementById('detail-desc').style.display = '';
    document.getElementById('detail-desc-preview').style.display = 'none';
}

// showDescPreview displays the given rendered HTML and hides the textarea.
function showDescPreview(html) {
    const preview = document.getElementById('detail-desc-preview');
    preview.innerHTML = html;
    preview.style.display = '';
    document.getElementById('detail-desc').style.display = 'none';
}

// switchDescToEdit is called by the Edit button and by clicking directly on
// the rendered preview (clicking "in the field" starts an edit). No-op for
// plain-text issues (there's no toggle) and for read-only viewers (the
// textarea is disabled, so there's nothing to edit).
function switchDescToEdit() {
    const textarea = document.getElementById('detail-desc');
    if (textarea.disabled) return;
    if (document.getElementById('detail-format').value === 'text') return;
    showDescEdit();
    textarea.focus();
}

// switchDescToPreview asks the server to render the current raw text (via
// POST /api/render, the same goldmark renderer used for the saved view) and
// switches the field to show the result. Called by the Preview button and
// by the textarea losing focus, so tabbing away always reverts to the
// formatted view. No-op for plain-text issues.
async function switchDescToPreview() {
    const format = document.getElementById('detail-format').value;
    if (format === 'text') return;
    const text = document.getElementById('detail-desc').value;
    try {
        showDescPreview(await renderPreview(format, text));
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('switchDescToPreview:', e);
    }
}

// onDescBlur reverts the description field to the formatted preview when the
// textarea loses focus (e.g. tabbing to the next field).
function onDescBlur() {
    switchDescToPreview();
}

// onDetailFormatChange handles the Format dropdown changing before save.
// It marks the panel dirty as usual, then updates the Edit/Preview toggle
// for the newly selected format: hidden (and forced to edit mode) for
// "text", shown for markdown/html. If the field is already showing a
// preview, that preview is refreshed against the new format rather than
// left displaying HTML rendered for the old one.
function onDetailFormatChange() {
    markDetailDirty();
    const format = document.getElementById('detail-format').value;
    const toggle = document.getElementById('detail-desc-toggle');
    if (format === 'text') {
        toggle.style.display = 'none';
        showDescEdit();
        return;
    }
    toggle.style.display = '';
    if (document.getElementById('detail-desc-preview').style.display !== 'none') {
        switchDescToPreview();
    }
    // The new-comment box shares the same format-driven toggle rules.
    updateCommentToggleVisibility(format);
}

// The new-comment textarea gets the same Edit/Preview toggle as the
// description field, for the same reason: composing markdown/HTML blind is
// awkward without a way to check the rendered result before posting. Unlike
// the description, a fresh comment box starts empty, so it always defaults
// to edit mode rather than preview mode — there's nothing to preview yet.
// The comment textarea is never disabled (any authenticated user may
// comment), so unlike switchDescToEdit there's no disabled/canEdit check.

// initCommentToggle shows/hides the toggle for the current issue's format
// and resets the field to (empty) edit mode. Used when opening an issue.
function initCommentToggle(format) {
    updateCommentToggleVisibility(format);
    showCommentEdit();
}

// updateCommentToggleVisibility shows/hides the toggle for the given format
// without otherwise disturbing an in-progress draft — used when the format
// changes out from under an already-open comment box (the Format dropdown,
// or a save that persists a format change). Forces edit mode when the new
// format no longer supports a toggle.
function updateCommentToggleVisibility(format) {
    const isFormatted = !!format && format !== 'text';
    document.getElementById('comment-toggle').style.display = isFormatted ? '' : 'none';
    if (!isFormatted) showCommentEdit();
}

// showCommentEdit displays the raw-text textarea and hides the preview.
function showCommentEdit() {
    document.getElementById('comment-input').style.display = '';
    document.getElementById('comment-preview').style.display = 'none';
}

// showCommentPreview displays the given rendered HTML and hides the textarea.
function showCommentPreview(html) {
    const preview = document.getElementById('comment-preview');
    preview.innerHTML = html;
    preview.style.display = '';
    document.getElementById('comment-input').style.display = 'none';
}

// switchCommentToEdit is called by the Edit button and by clicking directly
// on the rendered preview. No-op for plain-text issues (there's no toggle).
function switchCommentToEdit() {
    if (document.getElementById('detail-format').value === 'text') return;
    showCommentEdit();
    document.getElementById('comment-input').focus();
}

// switchCommentToPreview renders the current draft server-side (via
// POST /api/render) and switches the field to show the result. Called by
// the Preview button and by the textarea losing focus. No-op for
// plain-text issues.
async function switchCommentToPreview() {
    const format = document.getElementById('detail-format').value;
    if (format === 'text') return;
    const text = document.getElementById('comment-input').value;
    try {
        showCommentPreview(await renderPreview(format, text));
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('switchCommentToPreview:', e);
    }
}

// onCommentBlur reverts the comment field to the formatted preview when the
// textarea loses focus (e.g. tabbing to the "Add Comment" button).
function onCommentBlur() {
    switchCommentToPreview();
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
    const format    = document.getElementById('detail-format').value;
    const err       = document.getElementById('detail-error');

    err.textContent = '';
    if (!title)                          { err.textContent = 'Title is required.'; return; }
    if (!project)                        { err.textContent = 'Project is required.'; return; }
    if (!component)                      { err.textContent = 'Component is required.'; return; }
    // An assignee is required before an issue can be marked Resolved.
    if (status === 'Resolved' && !assignee) { err.textContent = 'An assignee is required before marking an issue Resolved.'; return; }

    // Status transitions that need additional input show a dialog and return.
    // _pendingStatusData holds the form values so the dialog's confirm handler
    // can call doSaveIssue without re-reading all the fields.
    if (_originalStatus === 'Open' && status === 'Resolved') {
        _pendingStatusData = { title, desc, priority, status, assignee, project, component, format };
        showResolveDialog();
        return;
    }
    if (_originalStatus === 'Resolved' && status === 'Open') {
        _pendingStatusData = { title, desc, priority, status, assignee, project, component, format };
        showReopenDialog();
        return;
    }
    // Any → Duplicate: must supply exactly one target issue ID.
    if (_originalStatus !== 'Duplicate' && status === 'Duplicate') {
        _pendingStatusData = { title, desc, priority, status, assignee, project, component, format };
        showDuplicateDialog();
        return;
    }
    // Any → Blocked: must supply at least one blocking issue ID.
    if (_originalStatus !== 'Blocked' && status === 'Blocked') {
        _pendingStatusData = { title, desc, priority, status, assignee, project, component, format };
        showBlockedDialog();
        return;
    }

    // No special transition: save directly. This covers field edits on a stable
    // status, inline add/remove on an already-Blocked issue, and Blocked→Open
    // (the server validates that all dep issues are Resolved and returns 409 if not).
    await doSaveIssue(title, desc, priority, status, assignee, project, component, format, null, '');
}

// doSaveIssue performs the actual PUT to update the issue, then
// optionally POSTs a client-side comment (for Resolve/Reopen notes).
// `serverComment` is the extra text appended to the server's auto-generated
// "Blocked by issues #N…" comment — it is sent in the PUT body, not as a
// separate POST.
async function doSaveIssue(title, desc, priority, status, assignee, project, component, format, commentBody, serverComment) {
    const err = document.getElementById('detail-error');
    const btn = document.getElementById('detail-save-btn');
    err.textContent = '';
    btn.disabled = true;
    btn.textContent = 'Saving…';
    try {
        const { issue } = await updateIssue(
            _currentId, title, desc, priority, status, assignee, project, component, format,
            _dependentIssues, serverComment || '', _detailTeams
        );
        if (commentBody) await addComment(_currentId, commentBody);
        _originalStatus     = status;
        _dependentIssues    = issue.dependent_issues || [];
        _detailTeams        = Array.isArray(issue.teams) ? [...issue.teams] : _detailTeams;
        renderTeamChips('detail-teams-chips', _detailTeams, 'detail', _currentUser && _currentUser.is_admin);
        _detailDirty = false;
        btn.style.display = 'none';
        document.getElementById('detail-updated').textContent = fmtDateTime(issue.updated_at);
        setDetailFormatValue(issue.format || 'text');
        initDescPreview(issue.format, issue.description_html);
        updateCommentToggleVisibility(issue.format);
        // Sync the dependent-issues section with what the server committed.
        renderDependentIssues(status, canModifyIssue(issue));
        // After a successful save, decide whether the issue still belongs in the
        // visible list given the current filters. For example, if the user filters
        // by "Open" and just resolved this issue it should vanish from the list
        // rather than staying as a stale row that doesn't match the filter.
        if (issue.updated_at > (_lastSeenAt || '')) _lastSeenAt = issue.updated_at;
        const idx = _issueWindow.findIndex(i => i.id === issue.id);
        if (idx !== -1) {
            if (matchesCurrentFilters(issue)) {
                // Still matches: update the row in-place.
                // _issueWindow[idx] = issue replaces the JS object in our local array.
                // tr.outerHTML = issueRow(issue) swaps the entire <tr> element in the
                // DOM for a freshly rendered string without touching neighbouring rows.
                _issueWindow[idx] = issue;
                const tr = document.querySelector(`#issues-tbody tr[data-id="${issue.id}"]`);
                if (tr) tr.outerHTML = issueRow(issue);
            } else {
                // No longer matches the active filter: remove from the window and DOM,
                // adjust the total, and close the detail panel.
                _issueWindow.splice(idx, 1);
                _totalIssues = Math.max(0, _totalIssues - 1);
                const tr = document.querySelector(`#issues-tbody tr[data-id="${issue.id}"]`);
                if (tr) tr.remove();
                updateIssueCounter();
                closeDetail();
            }
        }
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

// The next several functions implement a "confirmation dialog" pattern that
// repeats (with small variations) for every status transition that needs
// extra input: Resolve, Reopen, Duplicate, and Blocked. The shape is always
// the same:
//   1. saveIssueChanges() detects that the new status needs a dialog, saves
//      the already-validated form field values into a module-level
//      "_pending..." variable (_pendingStatusData, and for Blocked also
//      _pendingBlockedIds), and calls the matching show*Dialog() function.
//   2. show*Dialog() makes the overlay visible (`style.display = 'flex'`)
//      and pre-fills its fields.
//   3. The user either clicks "Confirm" (routes to confirm*Dialog(), which
//      reads the dialog's own input fields, validates them, hides the
//      overlay, and finally calls doSaveIssue() using the stashed pending
//      data) or clicks "Cancel"/closes the overlay (routes to
//      cancel*Dialog(), which resets the status dropdown back to
//      _originalStatus, clears the pending variable, and hides the overlay
//      without saving anything).
// Splitting the flow this way lets the dialog's own inputs (e.g. the
// resolution comment) be combined with the detail panel's fields (title,
// priority, etc.) into one PUT request once the user confirms, while still
// letting them back out cleanly at any point before that.

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
    // Object destructuring: this one line pulls each named property off
    // _pendingStatusData and declares a same-named local variable for it —
    // equivalent to writing `const title = _pendingStatusData.title;` etc.
    // for every field, just far more compact.
    const { title, desc, priority, status, assignee, project, component, format } = _pendingStatusData;
    _pendingStatusData = null;
    await doSaveIssue(title, desc, priority, status, assignee, project, component, format, commentBody, '');
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
// UI — STATUS CHANGE: Duplicate dialog
// =====================================================================

// showDuplicateDialog opens the overlay that captures the single target
// issue ID required when marking an issue Duplicate.
function showDuplicateDialog() {
    document.getElementById('dup-id-input').value = '';
    document.getElementById('dup-error').textContent = '';
    document.getElementById('duplicate-overlay').style.display = 'flex';
    document.getElementById('dup-id-input').focus();
}

// confirmDuplicateDialog validates the entered ID, stores it in
// _dependentIssues, and delegates to doSaveIssue.
async function confirmDuplicateDialog() {
    if (!_pendingStatusData) return;
    const val = parseInt(document.getElementById('dup-id-input').value.trim(), 10);
    const err = document.getElementById('dup-error');
    err.textContent = '';
    if (!val || val < 1) { err.textContent = 'Please enter a valid issue number.'; return; }
    if (val === _currentId) { err.textContent = 'An issue cannot be a duplicate of itself.'; return; }
    _dependentIssues = [val];
    document.getElementById('duplicate-overlay').style.display = 'none';
    const { title, desc, priority, status, assignee, project, component, format } = _pendingStatusData;
    _pendingStatusData = null;
    await doSaveIssue(title, desc, priority, status, assignee, project, component, format, null, '');
}

// cancelDuplicateDialog dismisses the dialog and restores the status picker.
function cancelDuplicateDialog() {
    if (_pendingStatusData) {
        document.getElementById('detail-status').value = _originalStatus;
        _pendingStatusData = null;
    }
    document.getElementById('duplicate-overlay').style.display = 'none';
}

// =====================================================================
// UI — STATUS CHANGE: Blocked dialog
// =====================================================================

// buildBlockedCommentText returns the seeded comment prefix for a Blocked
// transition, e.g. "Blocked by issues #3, #7". Returns '' if ids is empty.
function buildBlockedCommentText(ids) {
    if (!ids || ids.length === 0) return '';
    return 'Blocked by issues ' + ids.map(id => '#' + id).join(', ');
}

// showBlockedDialog opens the overlay for capturing one or more blocking
// issue IDs and a comment that will be posted client-side on confirm.
function showBlockedDialog() {
    _pendingBlockedIds = [..._dependentIssues];
    renderBlockedDialogList();
    document.getElementById('blk-add-input').value = '';
    const seeded = buildBlockedCommentText(_pendingBlockedIds);
    document.getElementById('blk-comment').value = seeded ? seeded + '\n\n' : '';
    document.getElementById('blk-error').textContent = '';
    document.getElementById('blocked-overlay').style.display = 'flex';
    if (_pendingBlockedIds.length > 0) {
        document.getElementById('blk-comment').focus();
    } else {
        document.getElementById('blk-add-input').focus();
    }
}

// addBlockedDialogIssue adds the value in the dialog's number input to the
// pending list and re-renders the chip list.
function addBlockedDialogIssue() {
    const input = document.getElementById('blk-add-input');
    const val   = parseInt(input.value.trim(), 10);
    const err   = document.getElementById('blk-error');
    err.textContent = '';
    if (!val || val < 1) return;
    if (val === _currentId) { err.textContent = 'An issue cannot block itself.'; return; }
    if (_pendingBlockedIds.includes(val)) { input.value = ''; return; }
    _pendingBlockedIds.push(val);
    input.value = '';
    renderBlockedDialogList();
}

// removeBlockedDialogIssue removes one ID from the staging list.
// Array.prototype.filter() returns a new array containing only the elements
// for which the callback returns true — here, every id except the one being
// removed. It never mutates the original array, so the "removal" is really
// "replace _pendingBlockedIds with a shorter copy that excludes `id`".
function removeBlockedDialogIssue(id) {
    _pendingBlockedIds = _pendingBlockedIds.filter(x => x !== id);
    renderBlockedDialogList();
}

// renderBlockedDialogList rebuilds the chip list inside the Blocked dialog
// and refreshes the seeded prefix in the comment textarea.
function renderBlockedDialogList() {
    const list = document.getElementById('blk-issues-list');
    if (_pendingBlockedIds.length === 0) {
        list.innerHTML = '<em class="dep-empty">No blocking issues added yet.</em>';
    } else {
        list.innerHTML = _pendingBlockedIds.map(id =>
            `<span class="dep-issue-chip">#${esc(String(id))}<button class="dep-remove-btn" onclick="removeBlockedDialogIssue(${id})" title="Remove">×</button></span>`
        ).join('');
    }
    // Keep the comment textarea prefix in sync with the current chip list.
    const ta = document.getElementById('blk-comment');
    if (!ta) return;
    const seeded = buildBlockedCommentText(_pendingBlockedIds);
    const current = ta.value;
    // Replace everything up to the first blank line (the seeded prefix line).
    const blankIdx = current.indexOf('\n\n');
    const userText = blankIdx >= 0 ? current.slice(blankIdx + 2) : '';
    ta.value = seeded ? seeded + '\n\n' + userText : userText;
}

// confirmBlockedDialog validates the pending list, transfers it to
// _dependentIssues, and saves. The comment is posted client-side so the
// textarea text (including the seeded prefix) is sent exactly as written.
async function confirmBlockedDialog() {
    if (!_pendingStatusData) return;
    const err = document.getElementById('blk-error');
    err.textContent = '';
    if (_pendingBlockedIds.length === 0) {
        err.textContent = 'At least one blocking issue is required.';
        return;
    }
    const commentBody = document.getElementById('blk-comment').value.trim();
    _dependentIssues = [..._pendingBlockedIds];
    document.getElementById('blocked-overlay').style.display = 'none';
    const { title, desc, priority, status, assignee, project, component, format } = _pendingStatusData;
    _pendingStatusData = null;
    await doSaveIssue(title, desc, priority, status, assignee, project, component, format, commentBody || null, '');
}

// cancelBlockedDialog dismisses the overlay and restores the status picker.
function cancelBlockedDialog() {
    if (_pendingStatusData) {
        document.getElementById('detail-status').value = _originalStatus;
        _pendingStatusData = null;
    }
    document.getElementById('blocked-overlay').style.display = 'none';
}

// =====================================================================
// UI — DEPENDENT ISSUES (inline panel, Blocked/Duplicate)
// =====================================================================

// onDetailStatusChange is called by the onchange handler on #detail-status.
// It calls markDetailDirty() and then updates the dependent-issues section
// visibility. When moving AWAY from Blocked/Duplicate the local list is
// cleared so the section doesn't show stale chips for the new status.
function onDetailStatusChange() {
    markDetailDirty();
    const status = document.getElementById('detail-status').value;
    if (status !== 'Blocked' && status !== 'Duplicate') {
        _dependentIssues = [];
    }
    // Compute canEdit from the currently open issue (may be undefined if not
    // yet loaded, in which case we default to false for safety).
    // Array.prototype.find() scans the array and returns the first element
    // for which the callback returns true (here: the issue whose id matches
    // _currentId), or undefined if none match — unlike filter(), it returns
    // a single element, not a new array.
    const issue   = _issueWindow.find(i => i.id === _currentId);
    const canEdit = issue ? canModifyIssue(issue) : false;
    renderDependentIssues(status, canEdit);
}

// renderDependentIssues updates the dependent-issues section in the detail
// panel to reflect the current status and _dependentIssues array.
//
// For Duplicate status it shows a read-only chip for the single target.
// For Blocked status it shows a chip-list with optional remove buttons
// (admins only) and an add field (any editor).
// For any other status the section is hidden.
function renderDependentIssues(status, canEdit) {
    const section  = document.getElementById('dependent-issues-section');
    const label    = document.getElementById('dep-label');
    const list     = document.getElementById('dep-issues-list');
    const addRow   = document.getElementById('dep-add-row');
    if (!section) return;

    if (status !== 'Blocked' && status !== 'Duplicate') {
        section.style.display = 'none';
        return;
    }

    section.style.display = '';
    const isAdmin = _currentUser && _currentUser.is_admin;

    if (status === 'Duplicate') {
        label.textContent = 'Duplicate Of';
        const depId = _dependentIssues[0];
        list.innerHTML = depId
            ? `<span class="dep-issue-chip">Issue #${esc(String(depId))}</span>`
            : '<em class="dep-empty">Not set — save to open the Duplicate dialog.</em>';
        addRow.style.display = 'none';
    } else {
        // Blocked
        label.textContent = 'Blocked By';
        if (_dependentIssues.length === 0) {
            list.innerHTML = '<em class="dep-empty">No blocking issues.</em>';
        } else {
            list.innerHTML = _dependentIssues.map(id => {
                // Only admins may remove blocking issues; the server enforces
                // the same rule and returns 403 for unauthorised removals.
                const removeBtn = (canEdit && isAdmin)
                    ? `<button class="dep-remove-btn" onclick="removeBlockingIssue(${id})" title="Remove">×</button>`
                    : '';
                return `<span class="dep-issue-chip">#${esc(String(id))}${removeBtn}</span>`;
            }).join('');
        }
        addRow.style.display = canEdit ? '' : 'none';
    }
}

// addBlockingIssue is called by the "Add" button in the inline Blocked
// section. It reads #dep-add-input, validates the value, and appends it
// to _dependentIssues before re-rendering.
function addBlockingIssue() {
    const input = document.getElementById('dep-add-input');
    const val   = parseInt(input.value.trim(), 10);
    if (!val || val < 1) return;
    if (val === _currentId) return;   // self-blocking is rejected by the server too
    if (_dependentIssues.includes(val)) { input.value = ''; return; }
    _dependentIssues.push(val);
    input.value = '';
    markDetailDirty();
    const status  = document.getElementById('detail-status').value;
    const issue   = _issueWindow.find(i => i.id === _currentId);
    const canEdit = issue ? canModifyIssue(issue) : false;
    renderDependentIssues(status, canEdit);
}

// removeBlockingIssue is called by the × button on a chip in the inline
// Blocked section. Admin-only (enforced by the server; the button is hidden
// for non-admins by renderDependentIssues).
function removeBlockingIssue(id) {
    _dependentIssues = _dependentIssues.filter(x => x !== id);
    markDetailDirty();
    const status  = document.getElementById('detail-status').value;
    const issue   = _issueWindow.find(i => i.id === _currentId);
    const canEdit = issue ? canModifyIssue(issue) : false;
    renderDependentIssues(status, canEdit);
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
    // body_html is server-rendered per the issue's format (markdown/html);
    // it's absent (undefined) for plain "text" issues, where the escaped
    // raw body plus CSS white-space:pre-wrap is the correct presentation.
    //
    // Add Image is open to any authenticated user (mirrors commenting
    // itself, and the description-level Add Image button), so — unlike
    // trashBtn — it's unconditional here. The drop-zone/error/thumbnail row
    // sit between the header and the body, the same position the
    // description's occupy relative to its textarea, so opening the
    // drop-zone pushes the comment text down rather than the thumbnails.
    el.innerHTML = comments.map(c => `
        <div class="comment-item">
            <div class="comment-header">
                <span class="comment-author">${esc(displayName(c.author))}</span>
                <span class="comment-date">${fmtDateTime(c.created_at)}</span>
                <div class="comment-actions">
                    <button class="btn-comment-img" onclick="toggleCommentImageDropzone(${c.id})" title="Add image" aria-label="Add image">${CAMERA_ICON_SVG}</button>
                    ${trashBtn(c.id)}
                </div>
            </div>
            <div id="comment-dropzone-${c.id}" class="image-dropzone" style="display:none"
                 onclick="triggerCommentImagePicker(${c.id})"
                 ondragover="onDropzoneDragOver(event)" ondragleave="onDropzoneDragLeave(event)" ondrop="onCommentDropzoneDrop(event, ${c.id})">
                <span class="dropzone-hint">Drop images here, or click to browse — PNG or JPEG, up to 10&nbsp;MB each</span>
                <input type="file" id="comment-image-input-${c.id}" accept="image/png,image/jpeg" multiple
                       style="display:none" onchange="onCommentImageFilesSelected(event, ${c.id})">
            </div>
            <div id="comment-image-error-${c.id}" class="error-text"></div>
            <div class="comment-body${c.body_html ? ' rendered-html' : ''}">${c.body_html || esc(c.body)}</div>
            <div id="comment-attachments-${c.id}" class="attachment-thumbs" style="display:none"></div>
        </div>
    `).join('');
    renderCommentAttachmentThumbs();
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
        // Reset to (empty) edit mode — a stale preview of the just-posted
        // text would otherwise linger for the next comment.
        showCommentEdit();
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
// UI — ATTACHMENTS
// =====================================================================
// Image attachments on the currently-open issue's description. The upload
// flow is: click "Add Image…" to reveal a drop-zone (also click-to-browse
// via a hidden <input type=file>), pick/drop one or more PNG/JPEG files,
// each is POSTed individually to POST /api/issues/{id}/attachments as
// multipart/form-data, then the whole list is refetched from the server
// and re-rendered as a row of thumbnails under the description. Clicking a
// thumbnail opens the full-size image in an overlay, which carries a
// Delete button when the current user is the uploader or an admin — see
// server/attachments.go's handleDeleteAttachment for the matching
// server-side check this mirrors (client-side hiding is a convenience;
// the server enforces the real rule).

const ATTACHMENT_MAX_BYTES = 10 * 1024 * 1024; // headroom under the server's 12 MiB multipart cap
const ATTACHMENT_MIME_TYPES = ['image/png', 'image/jpeg'];

// CAMERA_ICON_SVG is the per-comment "Add Image" button's glyph — an inline
// stroke-based SVG (Feather Icons' "camera", MIT licensed) rather than a
// Unicode emoji. This matters specifically because of what it fixes: a
// color emoji (the previous 🖼 glyph) renders with its own fixed palette
// that ignores the button's `color`/`opacity` CSS entirely, so it can't
// pick up .btn-comment-img's --text-light/--primary hover colors the way
// the SVG below does via fill="currentColor" — that mismatch is what made
// it look muddy/low-contrast in Safari's dark mode. A plain stroke camera
// outline is also the closest web-safe equivalent to the SF Symbols
// "camera" glyph the button is meant to evoke.
const CAMERA_ICON_SVG = `<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M23 19a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4l2-3h6l2 3h4a2 2 0 0 1 2 2z"/><circle cx="12" cy="13" r="4"/></svg>`;

// loadDetailAttachments fetches every attachment on issueId — both on its
// description and on any of its comments — and re-renders both. Called from
// selectIssue() and again after every successful upload/delete so
// _detailAttachments never drifts from the database.
async function loadDetailAttachments(issueId) {
    try {
        const { attachments } = await apiGet(`/api/issues/${issueId}/attachments`);
        // Bail if the user has since navigated to a different issue (or
        // closed the panel) while this request was in flight.
        if (issueId !== _currentId) return;
        _detailAttachments = attachments || [];
        renderAllAttachments();
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('loadDetailAttachments:', e);
    }
}

// renderAllAttachments splits _detailAttachments into the description's
// thumbnail row and each comment's own thumbnail row. Called after every
// fetch/upload/delete, and also from the tail of renderComments() — the
// comment thumbnail containers only exist once the comment list itself has
// been (re)rendered, so any code path that redraws #comments-list (posting
// a new comment, deleting one, an auto-posted status-change comment) needs
// its thumbnails redrawn too, and folding that into renderComments() itself
// means every call site gets it for free instead of needing to remember.
function renderAllAttachments() {
    renderAttachmentThumbs();
    renderCommentAttachmentThumbs();
}

// attachmentThumbsHTML is the shared markup for one thumbnail row, used by
// both renderAttachmentThumbs (description) and renderCommentAttachmentThumbs
// (per comment). Thumbnails are plain <img> tags pointed at the
// per-attachment thumbnail route rather than embedded data — the browser
// caches/lazy-loads each one independently (see GET
// /api/attachments/{id}/thumbnail's doc comment). A flex-wrap row lets as
// many thumbnails fit per line as the available width allows, with no JS
// layout math needed.
function attachmentThumbsHTML(list) {
    return list.map(a => `
        <button type="button" class="attachment-thumb" onclick="openAttachmentViewer('${a.id}')" title="${esc(a.filename || 'image.png')}">
            <img src="${BASE_PATH}/api/attachments/${a.id}/thumbnail" alt="${esc(a.filename || '')}" loading="lazy">
        </button>
    `).join('');
}

// renderAttachmentThumbs rebuilds the description's thumbnail row —
// attachments with no comment_id.
function renderAttachmentThumbs() {
    const el = document.getElementById('detail-attachments');
    if (!el) return;
    const list = _detailAttachments.filter(a => !a.comment_id);
    if (!list.length) {
        el.innerHTML = '';
        el.style.display = 'none';
        return;
    }
    el.style.display = '';
    el.innerHTML = attachmentThumbsHTML(list);
}

// renderCommentAttachmentThumbs rebuilds every comment's own thumbnail row.
// It looks up each row's target comment id from the container's own id
// (comment-attachments-{id}, written by renderComments()) rather than
// iterating _detailAttachments and hoping a container exists for every
// comment_id — that way a container that legitimately has zero attachments
// still gets cleared/hidden instead of silently keeping stale content.
function renderCommentAttachmentThumbs() {
    const byComment = {};
    for (const a of _detailAttachments) {
        if (!a.comment_id) continue;
        (byComment[a.comment_id] = byComment[a.comment_id] || []).push(a);
    }
    document.querySelectorAll('[id^="comment-attachments-"]').forEach(el => {
        const cid = el.id.slice('comment-attachments-'.length);
        const list = byComment[cid] || [];
        if (!list.length) {
            el.innerHTML = '';
            el.style.display = 'none';
            return;
        }
        el.style.display = '';
        el.innerHTML = attachmentThumbsHTML(list);
    });
}

// toggleImageDropzone shows/hides the drag-and-drop + click-to-browse panel
// under the description's "Add Image…" button. Any authenticated user can
// open it — see selectIssue()'s comment on why this isn't gated by canEdit
// the way the title/status/etc. fields are.
function toggleImageDropzone() {
    const dz = document.getElementById('detail-image-dropzone');
    if (!dz) return;
    dz.style.display = dz.style.display === 'none' ? '' : 'none';
    document.getElementById('detail-image-error').textContent = '';
}

// triggerImageFilePicker opens the native OS file picker by forwarding the
// click to the hidden <input type=file> — this is what makes the drop-zone
// clickable as well as drag-droppable. accept="image/png,image/jpeg" on
// that input filters the picker's file listing in every major browser,
// Safari included.
function triggerImageFilePicker() {
    document.getElementById('detail-image-input').click();
}

// onDropzoneDragOver/DragLeave are shared by every drop-zone on the page
// (the description's and every comment's) since they only ever touch
// e.currentTarget — the specific element the listener is bound to — never a
// hardcoded id.
function onDropzoneDragOver(e) {
    e.preventDefault(); // required for the drop event to fire at all
    e.currentTarget.classList.add('drag-over');
}
function onDropzoneDragLeave(e) {
    e.currentTarget.classList.remove('drag-over');
}
function onDropzoneDrop(e) {
    e.preventDefault();
    e.currentTarget.classList.remove('drag-over');
    handleImageFiles(e.dataTransfer.files);
}
function onAttachmentFilesSelected(e) {
    handleImageFiles(e.target.files);
    e.target.value = ''; // reset so selecting the exact same file again still fires 'change'
}

// uploadAttachmentFiles validates a FileList client-side (format + size —
// purely a fast-fail UX nicety; the server re-validates both regardless,
// see processUploadedImage in server/images.go) and uploads each accepted
// file in turn to uploadUrl. Uploads run sequentially rather than in
// parallel: idtrack's SQLite connection is already serialized to a single
// writer (db.SetMaxOpenConns(1)), so parallel requests would just queue up
// behind each other server-side anyway, and sequential keeps the
// error-per-file bookkeeping simple. Shared by the description-level and
// per-comment "Add Image" flows — errEl/dropzoneEl are whichever pair of
// elements belongs to the caller's own drop-zone. Returns {uploaded,
// failed} counts so the caller can decide whether to refresh/collapse.
async function uploadAttachmentFiles(fileList, uploadUrl, errEl, dropzoneEl) {
    errEl.textContent = '';
    if (!fileList || !fileList.length) return { uploaded: 0, failed: 0 };

    const files = Array.from(fileList);
    const rejected = [];
    const accepted = files.filter(f => {
        if (!ATTACHMENT_MIME_TYPES.includes(f.type)) { rejected.push(`${f.name} (unsupported format)`); return false; }
        if (f.size > ATTACHMENT_MAX_BYTES) { rejected.push(`${f.name} (too large)`); return false; }
        return true;
    });

    if (rejected.length) {
        errEl.textContent = `Skipped ${rejected.join(', ')} — only PNG/JPEG images up to 10 MB are supported.`;
    }
    if (!accepted.length) return { uploaded: 0, failed: 0 };

    if (dropzoneEl) dropzoneEl.classList.add('uploading');

    let uploaded = 0, failed = 0;
    try {
        for (const file of accepted) {
            const fd = new FormData();
            fd.append('image', file);
            try {
                await apiUpload(uploadUrl, fd);
                uploaded++;
            } catch (e) {
                if (e.message === 'Unauthorized') throw e;
                failed++;
                errEl.textContent = `Failed to upload ${file.name}: ${e.message || 'unknown error'}`;
            }
        }
    } finally {
        if (dropzoneEl) dropzoneEl.classList.remove('uploading');
    }
    return { uploaded, failed };
}

// handleImageFiles is the description-level entry point wired to the
// dropzone's click-to-browse input and drag/drop handlers.
async function handleImageFiles(fileList) {
    if (!_currentId) return;
    const errEl = document.getElementById('detail-image-error');
    const dz = document.getElementById('detail-image-dropzone');
    const { uploaded, failed } = await uploadAttachmentFiles(fileList, `/api/issues/${_currentId}/attachments`, errEl, dz);
    if (uploaded) await loadDetailAttachments(_currentId);
    if (uploaded && !failed) toggleImageDropzone(); // collapse the drop-zone once every file succeeded
}

// ---- Per-comment attachments ----
// Each rendered comment (renderComments(), below) gets its own "Add
// Image…" icon button, drop-zone, error line, and thumbnail row — mirroring
// the description's, just scoped to that one comment id instead of the
// issue itself. POST target is /api/issues/{id}/comments/{cid}/attachments
// rather than /api/issues/{id}/attachments; everything else is identical.

function toggleCommentImageDropzone(commentId) {
    const dz = document.getElementById(`comment-dropzone-${commentId}`);
    if (!dz) return;
    dz.style.display = dz.style.display === 'none' ? '' : 'none';
    document.getElementById(`comment-image-error-${commentId}`).textContent = '';
}

function triggerCommentImagePicker(commentId) {
    document.getElementById(`comment-image-input-${commentId}`).click();
}

function onCommentDropzoneDrop(e, commentId) {
    e.preventDefault();
    e.currentTarget.classList.remove('drag-over');
    handleCommentImageFiles(e.dataTransfer.files, commentId);
}
function onCommentImageFilesSelected(e, commentId) {
    handleCommentImageFiles(e.target.files, commentId);
    e.target.value = '';
}

async function handleCommentImageFiles(fileList, commentId) {
    if (!_currentId) return;
    const errEl = document.getElementById(`comment-image-error-${commentId}`);
    const dz = document.getElementById(`comment-dropzone-${commentId}`);
    const { uploaded, failed } = await uploadAttachmentFiles(
        fileList, `/api/issues/${_currentId}/comments/${commentId}/attachments`, errEl, dz);
    if (uploaded) await loadDetailAttachments(_currentId);
    if (uploaded && !failed) toggleCommentImageDropzone(commentId);
}

// openAttachmentViewer shows one attachment's full-size image in the
// #attachment-viewer-overlay sheet — used for both description- and
// comment-level attachments, since _detailAttachments holds both. The
// Delete button is shown only when the current user is the uploader or an
// admin, mirroring handleDeleteAttachment's server-side check — see this
// section's header comment.
function openAttachmentViewer(id) {
    const a = _detailAttachments.find(x => x.id === id);
    if (!a) return;

    _avAttachmentId = id;
    document.getElementById('av-filename').textContent = a.filename || 'image.png';

    const img = document.getElementById('av-image');
    img.src = `${BASE_PATH}/api/attachments/${id}`;
    img.alt = a.filename || '';

    document.getElementById('av-meta').textContent =
        `${a.width}×${a.height} · uploaded by ${displayName(a.uploader)} · ${fmtDateTime(a.created_at)}`;

    const canDelete = _currentUser && (_currentUser.is_admin || _currentUser.username === a.uploader);
    document.getElementById('av-delete-btn').style.display = canDelete ? '' : 'none';

    document.getElementById('attachment-viewer-overlay').style.display = '';
}

function closeAttachmentViewer() {
    document.getElementById('attachment-viewer-overlay').style.display = 'none';
    document.getElementById('av-image').src = '';
    _avAttachmentId = null;
}

// confirmDeleteAttachment deletes the attachment currently shown in the
// viewer. DELETE /api/attachments/{id}
async function confirmDeleteAttachment() {
    if (!_avAttachmentId) return;
    if (!confirm('Delete this image? This cannot be undone.')) return;

    const id = _avAttachmentId;

    try {
        await apiDelete(`/api/attachments/${id}`);
        _detailAttachments = _detailAttachments.filter(a => a.id !== id);
        renderAllAttachments();
        closeAttachmentViewer();
    } catch (e) {
        if (e.message !== 'Unauthorized') alert('Failed to delete image: ' + (e.message || 'unknown error'));
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
    document.getElementById('ni-format').value = 'text';
    document.getElementById('ni-desc').value = '';
    document.getElementById('ni-error').textContent = '';
    initNiDescToggle();
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

// hideNewIssue closes the New Issue overlay without saving anything. There
// is no dirty-check here (unlike closeDetail/selectIssue) — an abandoned
// New Issue draft is simply discarded.
function hideNewIssue() {
    document.getElementById('new-issue-overlay').style.display = 'none';
}

// The New Issue form's description field gets the same Edit/Preview toggle
// as the detail panel's description and the new-comment box. Like the
// comment box (and unlike the detail description), it always starts empty,
// so it defaults to edit mode rather than preview mode.

// initNiDescToggle resets the toggle to match the form's default "text"
// format (hidden, edit mode) when the New Issue overlay opens.
function initNiDescToggle() {
    updateNiDescToggleVisibility('text');
}

// updateNiDescToggleVisibility shows/hides the toggle for the given format.
// Forces edit mode when the format no longer supports a toggle.
function updateNiDescToggleVisibility(format) {
    const isFormatted = !!format && format !== 'text';
    document.getElementById('ni-desc-toggle').style.display = isFormatted ? '' : 'none';
    if (!isFormatted) showNiDescEdit();
}

// onNiFormatChange is the onchange handler for the New Issue form's Format
// dropdown: it updates the description toggle for the newly selected format
// without otherwise disturbing whatever the user has already typed.
function onNiFormatChange() {
    updateNiDescToggleVisibility(document.getElementById('ni-format').value);
}

// showNiDescEdit displays the raw-text textarea and hides the preview.
function showNiDescEdit() {
    document.getElementById('ni-desc').style.display = '';
    document.getElementById('ni-desc-preview').style.display = 'none';
}

// showNiDescPreview displays the given rendered HTML and hides the textarea.
function showNiDescPreview(html) {
    const preview = document.getElementById('ni-desc-preview');
    preview.innerHTML = html;
    preview.style.display = '';
    document.getElementById('ni-desc').style.display = 'none';
}

// switchNiDescToEdit is called by the Edit button and by clicking directly
// on the rendered preview. No-op for plain-text issues (there's no toggle).
function switchNiDescToEdit() {
    if (document.getElementById('ni-format').value === 'text') return;
    showNiDescEdit();
    document.getElementById('ni-desc').focus();
}

// switchNiDescToPreview renders the current draft server-side (via
// POST /api/render) and switches the field to show the result. Called by
// the Preview button and by the textarea losing focus. No-op for
// plain-text issues.
async function switchNiDescToPreview() {
    const format = document.getElementById('ni-format').value;
    if (format === 'text') return;
    const text = document.getElementById('ni-desc').value;
    try {
        showNiDescPreview(await renderPreview(format, text));
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('switchNiDescToPreview:', e);
    }
}

// onNiDescBlur reverts the description field to the formatted preview when
// the textarea loses focus (e.g. tabbing to the next field).
function onNiDescBlur() {
    switchNiDescToPreview();
}

// submitNewIssue validates the form, creates the issue on the server,
// refreshes the list, and then automatically opens the new issue's
// detail panel so the user can see it immediately.
//
// POST /api/issues
//   Request body: { title, description, priority, assignee, project, component, format }
//   Response:     { issue: { ...newly created issue... } }
async function submitNewIssue() {
    const title     = document.getElementById('ni-title').value.trim();
    const priority  = document.getElementById('ni-priority').value;
    const assignee  = document.getElementById('ni-assignee').value;
    const desc      = document.getElementById('ni-desc').value.trim();
    const project   = document.getElementById('ni-project').value;
    const component = document.getElementById('ni-component').value;
    const format    = document.getElementById('ni-format').value;
    const err       = document.getElementById('ni-error');
    const btn       = document.getElementById('ni-submit-btn');

    err.textContent = '';
    if (!title)     { err.textContent = 'Title is required.'; return; }
    if (!project)   { err.textContent = 'Project is required.'; return; }
    if (!component) { err.textContent = 'Component is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Creating…';

    try {
        const { issue: newIssue } = await createIssue(title, desc, priority, assignee, project, component, format);
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
//
// Note on the rendering pattern used throughout the rest of this file:
// options are built with `Array.prototype.map()` (transform each user into
// an `<option>` string) followed by `.join('')` (glue the array of strings
// into one big string), using backtick template literals (`` `...${expr}...` ``)
// to interpolate values into the markup. The finished string is assigned to
// an element's `.innerHTML`, which is how this codebase renders dynamic
// content — there is no UI framework doing this for us. Every value that
// came from user input (here, `u.username` and `u.display_name`) is passed
// through `esc()` (defined earlier in this file) before interpolation, so a
// user-chosen display name containing `<script>` can't inject markup — this
// is the standard defense against cross-site scripting (XSS) when building
// HTML by hand.
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
            <th>Username</th><th>Display Name</th><th>Teams</th><th>Last Login</th>
        </tr></thead>
        <tbody>${users.map(u => {
            const teams = Array.isArray(u.teams) ? u.teams : (u.is_admin ? ['admin'] : ['any']);
            const teamBadges = teams.map(t => {
                const cls = t === 'admin' ? 'badge-open' : t === 'any' ? 'badge-low' : '';
                return `<span class="badge ${cls}">${esc(t)}</span>`;
            }).join(' ');
            return `
            <tr class="mu-row" onclick="openEditUserFromManage('${esc(u.username)}')">
                <td class="mu-username">${esc(u.username)}</td>
                <td>${esc(u.display_name || u.username)}</td>
                <td>${teamBadges}</td>
                <td class="mu-login">${esc(u.last_login_at ? fmtDateTime(u.last_login_at) : '(never)')}</td>
            </tr>`;
        }).join('')}
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
    document.getElementById('au-error').textContent   = '';
    _auTeams = ['any'];
    renderTeamChips('au-teams-chips', _auTeams, 'au', true);
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
    const err          = document.getElementById('au-error');
    const btn          = document.getElementById('au-submit-btn');

    err.textContent = '';
    if (!username)  { err.textContent = 'Username is required.'; return; }
    if (!password)  { err.textContent = 'Password is required.'; return; }
    if (password !== confirm) { err.textContent = 'Passwords do not match.'; return; }
    if (_auTeams.length === 0) { err.textContent = 'At least one team is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Adding…';
    try {
        await apiPost('/api/users', {
            username, display_name: displayName, password,
            teams: _auTeams,
            is_admin: _auTeams.includes('admin'),
        });
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
        document.getElementById('eu-delete-btn').style.display = 'none';
        _euTeams = [];
        renderTeamChips('eu-teams-chips', _euTeams, 'eu', true);
        return;
    }
    document.getElementById('eu-display-name').value = user.display_name || '';
    document.getElementById('eu-password').value      = '';
    document.getElementById('eu-confirm').value        = '';
    document.getElementById('eu-delete-btn').style.display =
        (user.username === _currentUser.username) ? 'none' : '';
    _euTeams = Array.isArray(user.teams) ? [...user.teams] : (user.is_admin ? ['admin'] : ['any']);
    renderTeamChips('eu-teams-chips', _euTeams, 'eu', true);
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
    const err         = document.getElementById('eu-error');
    const btn         = document.getElementById('eu-save-btn');

    err.textContent = '';
    if (!username)    { err.textContent = 'Select a user.'; return; }
    if (password !== confirm) { err.textContent = 'Passwords do not match.'; return; }
    if (_euTeams.length === 0) { err.textContent = 'At least one team is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Saving…';
    try {
        await apiPut(`/api/users/${encodeURIComponent(username)}`, {
            display_name: displayName, password,
            teams: _euTeams,
            is_admin: _euTeams.includes('admin'),
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
// UI — MANAGE TEAMS (admin)
// =====================================================================
// Two-screen overlay stack: mt-list-overlay → mt-detail-overlay.
// Follows the same parent/child pattern as Edit Projects.

// openManageTeams shows the team list overlay. Unlike openManageUsers it
// makes no network call — _teamData is expected to already be populated
// (loaded once at login by loadTeamData(), and refreshed by mtSaveTeam()/
// mtDeleteTeam() after any change) so opening the list is instant.
function openManageTeams() {
    _closeMenuOnOutside();
    mtRenderTeamList();
    document.getElementById('mt-list-overlay').style.display = 'flex';
}

function hideManageTeams() {
    document.getElementById('mt-list-overlay').style.display = 'none';
}

// mtRenderTeamList builds the clickable team rows inside the list overlay
// from the in-memory _teamData array. The reserved teams ("admin" and
// "any") are marked with a small lock emoji since they can't be renamed
// or deleted — see openTeamDetail().
function mtRenderTeamList() {
    const body = document.getElementById('mt-list-body');
    if (!_teamData || _teamData.length === 0) {
        body.innerHTML = '<p class="ep-empty">No teams yet.</p>';
        return;
    }
    body.innerHTML = _teamData.map(t => {
        const reserved = t.name === 'admin' || t.name === 'any';
        const lock = reserved ? ' &#x1F512;' : '';
        const desc = t.description ? esc(t.description) : '<em class="mu-empty">No description</em>';
        return `
        <div class="ep-project-row" onclick="openTeamDetail('${esc(t.name)}')">
            <span class="ep-project-name">${esc(t.name)}${lock}</span>
            <span class="ep-project-count">${desc}</span>
            <span class="ep-project-arrow">&#8250;</span>
        </div>`;
    }).join('');
}

// openTeamDetail switches from the team list to the detail screen, the
// "child" overlay in the same parent/child pattern used by Manage Users
// and Edit Projects (see the comments at the top of this section and at
// "UI — EDIT PROJECTS" above). Pass null for 'name' to open in "new team"
// mode; pass an existing team's name to edit it.
//
// The reserved teams "admin" and "any" (see db.TeamAdmin/db.TeamAny in the
// Go backend) have special meaning to the access-control logic and so
// cannot be renamed or deleted here — the name input is disabled and the
// Delete button hidden whenever isReserved is true, though their
// description can still be edited.
function openTeamDetail(name) {
    _mtTeam = name;
    const isNew = name === null;
    const team = name ? _teamData.find(t => t.name === name) : null;
    const isReserved = name === 'admin' || name === 'any';

    document.getElementById('mt-detail-title').textContent = isNew ? 'New Team' : esc(name);
    const nameInput = document.getElementById('mt-name-input');
    nameInput.value    = isNew ? '' : name;
    nameInput.disabled = isReserved;
    document.getElementById('mt-desc-input').value = team ? (team.description || '') : '';
    document.getElementById('mt-delete-btn').style.display  = (isNew || isReserved) ? 'none' : '';
    document.getElementById('mt-detail-error').textContent  = '';

    document.getElementById('mt-list-overlay').style.display   = 'none';
    document.getElementById('mt-detail-overlay').style.display = 'flex';
    document.getElementById(isNew ? 'mt-name-input' : 'mt-desc-input').focus();
}

// hideTeamDetail closes the detail screen and returns to a freshly
// re-rendered team list — the same "every exit path re-shows the parent"
// rule followed by hideEditUser()/hideProjectDetail().
function hideTeamDetail() {
    document.getElementById('mt-detail-overlay').style.display = 'none';
    mtRenderTeamList();
    document.getElementById('mt-list-overlay').style.display = 'flex';
}

// mtSaveTeam creates a new team or saves changes to an existing one,
// depending on whether _mtTeam is null (new-team mode, set by
// openTeamDetail(null)) or holds the name of the team being edited.
//
// POST /api/teams              (new team)
//   Request body: { name, description }
// PUT /api/teams/{name}        (existing team)
//   Request body: { description } plus 'name' only when it changed —
//   renaming a team cascades server-side to every user/project/issue
//   that referenced the old name.
//   Both endpoints are admin-only.
async function mtSaveTeam() {
    const newName = document.getElementById('mt-name-input').value.trim().toLowerCase();
    const desc    = document.getElementById('mt-desc-input').value.trim();
    const err     = document.getElementById('mt-detail-error');
    const btn     = document.getElementById('mt-save-btn');
    err.textContent = '';

    if (_mtTeam === null) {
        if (!newName) { err.textContent = 'Team name is required.'; return; }
        btn.disabled = true; btn.textContent = 'Saving…';
        try {
            await apiPost('/api/teams', { name: newName, description: desc });
            await loadTeamData();
            hideTeamDetail();
        } catch (e) {
            if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed.';
        } finally { btn.disabled = false; btn.textContent = 'Save'; }
    } else {
        btn.disabled = true; btn.textContent = 'Saving…';
        try {
            const body = { description: desc };
            if (newName && newName !== _mtTeam) body.name = newName;
            await apiPut(`/api/teams/${encodeURIComponent(_mtTeam)}`, body);
            await loadTeamData();
            hideTeamDetail();
        } catch (e) {
            if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed.';
        } finally { btn.disabled = false; btn.textContent = 'Save'; }
    }
}

// mtDeleteTeam permanently deletes the team currently open in the detail
// screen, after a confirm() prompt. The Delete button is only shown for
// non-reserved, non-new teams (see openTeamDetail()), so _mtTeam is always
// an existing, deletable team name here.
//
// DELETE /api/teams/{name}
//   Admin-only. The server refuses if the team is still referenced by any
//   user, project, or issue, and returns an error describing how many.
async function mtDeleteTeam() {
    if (!_mtTeam) return;
    if (!confirm(`Delete team "${_mtTeam}"? This cannot be undone.`)) return;
    const err = document.getElementById('mt-detail-error');
    const btn = document.getElementById('mt-delete-btn');
    btn.disabled = true;
    try {
        await apiDelete(`/api/teams/${encodeURIComponent(_mtTeam)}`);
        await loadTeamData();
        hideTeamDetail();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Delete failed.';
    } finally { btn.disabled = false; }
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
    const project = name ? _projectData.find(p => p.name === name) : null;

    document.getElementById('ep-detail-title').textContent          = isNew ? 'New Project' : name;
    document.getElementById('ep-name-group').style.display          = isNew ? '' : 'none';
    document.getElementById('ep-project-name').value                = '';
    document.getElementById('ep-delete-project-btn').style.display  = isNew ? 'none' : '';
    document.getElementById('ep-comp-name').value                   = '';
    document.getElementById('ep-detail-error').textContent          = '';
    document.getElementById('ep-create-btn').style.display          = isNew ? '' : 'none';

    // Populate the teams chip picker.
    _epTeams = (project && Array.isArray(project.teams)) ? [...project.teams] : ['any'];
    renderTeamChips('ep-teams-chips', _epTeams, 'ep', true);
    // "Save Teams" only makes sense when editing an existing project.
    const saveTeamsBtn = document.getElementById('ep-save-teams-btn');
    if (saveTeamsBtn) saveTeamsBtn.style.display = isNew ? 'none' : '';

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
        await apiPost('/api/projects', { name, teams: _epTeams.length ? _epTeams : ['any'] });
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

// epSaveTeams pushes the current _epTeams list to the server for an
// existing project.
//
// PUT /api/projects/{project}/teams
//   Request body: { teams: [...] }
async function epSaveTeams() {
    if (!_epProject) return;
    const err = document.getElementById('ep-detail-error');
    err.textContent = '';
    try {
        await apiPut(`/api/projects/${encodeURIComponent(_epProject)}/teams`, { teams: _epTeams });
        await populateProjectDropdowns();
        err.textContent = '';
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to save teams.';
    }
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
        const data = await fetch(BASE_PATH + '/api/version').then(r => r.json());
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
    document.getElementById('dark-mode-select').value = _darkModePref;
    document.getElementById('keep-logged-in-toggle').checked = _keepLoggedIn;
    document.getElementById('desktop-mode-toggle').checked = _desktopMode;
    const psSel = document.getElementById('page-size-select');
    if (psSel) psSel.value = String(_pageSize);
    document.getElementById('use-passkeys-toggle').checked = _usePasskeys;
    updateSettingsPasskeysVisibility();
    document.getElementById('settings-overlay').style.display = 'flex';
}

// updateSettingsPasskeysVisibility shows/hides the two passkey-related
// pieces of Settings independently: the "Use passkeys" toggle row itself is
// shown whenever this server instance has the feature on at all
// (_webauthnEnabled), regardless of the user's own preference — the toggle
// is how they change that preference, so it can't be hidden by the very
// thing it controls. The credential list + "Add a passkey" section below it
// only appears once the toggle is also on, per the plan: turning the
// preference off "collapses" management down to just the toggle so nothing
// else prompts or expects passkey use until it's turned back on.
function updateSettingsPasskeysVisibility() {
    const toggleRow = document.getElementById('use-passkeys-row');
    const section = document.getElementById('passkeys-section');
    if (!toggleRow || !section) return;

    toggleRow.style.display = _webauthnEnabled ? 'flex' : 'none';

    const showSection = _webauthnEnabled && _usePasskeys;
    section.style.display = showSection ? '' : 'none';
    if (showSection) loadPasskeys();
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

// prefersDarkColorScheme reports the browser/OS default color scheme, used
// to resolve the 'auto' dark mode setting.
function prefersDarkColorScheme() {
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

// applyDarkMode adds or removes the 'dark' class from <body> based on the
// resolved effective state (mode 'on', or 'auto' when the browser prefers
// a dark scheme). All dark mode color overrides in idtrack.css use the
// 'body.dark' selector, so toggling this class is all that's needed to
// switch themes.
function applyDarkMode(mode) {
    _darkMode = mode === 'on' || (mode === 'auto' && prefersDarkColorScheme());
    document.body.classList.toggle('dark', _darkMode);
}

// setDarkMode is called from the Settings 'Dark mode' select (off/on/auto).
// It persists the chosen mode and applies it immediately. When 'auto' is
// selected, a media-query listener (installed once in loadPrefs) keeps the
// theme in sync with the browser's color scheme for the rest of the session.
function setDarkMode(mode) {
    _darkModePref = mode;
    applyDarkMode(mode);
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.darkMode = mode;
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

// toggleUsePasskeys controls the per-browser "Use passkeys" preference (see
// _usePasskeys's declaration near the top of this file). It is independent
// of the server-side _webauthnEnabled switch — this only ever lets a user
// opt out of a feature the server has already turned on for everyone, never
// opt into one the server has turned off. Turning it off immediately hides
// the login screen's passkey button (updatePasskeyLoginVisibility runs on
// every showLogin(), so the next time the login screen appears it reflects
// the new preference) and collapses the Settings passkey list back down to
// just this toggle; turning it on restores both.
function toggleUsePasskeys(on) {
    _usePasskeys = on;
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.usePasskeys = on;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
    updateSettingsPasskeysVisibility();
}

// loadPasskeys fetches the current user's registered passkeys and renders
// them into the Settings "Passkeys" list, each with a "Remove" button.
// Called by updateSettingsPasskeysVisibility() whenever that section becomes
// visible (opening Settings, or turning "Use passkeys" back on) and again
// after addPasskey()/removePasskey() change the list.
async function loadPasskeys() {
    const list = document.getElementById('passkeys-list');
    const err  = document.getElementById('passkeys-error');
    if (!list) return;
    err.textContent = '';
    list.textContent = 'Loading…';
    try {
        const creds = await apiGet('/api/webauthn/credentials');
        if (!creds.length) {
            list.innerHTML = '<div class="settings-row"><span class="settings-label">No passkeys registered yet</span></div>';
            return;
        }
        list.innerHTML = creds.map(c => `
            <div class="settings-row">
              <span class="settings-label">${esc(c.name)} <span style="opacity:.65">— added ${esc(fmtDate(c.created_at))}</span></span>
              <button class="btn btn-secondary btn-sm" onclick="removePasskey('${esc(c.id)}')">Remove</button>
            </div>`).join('');
    } catch (e) {
        list.textContent = '';
        err.textContent = e.message || 'Could not load passkeys.';
    }
}

// addPasskey is the click handler for the Settings "Add a passkey" button.
// It prompts for a short label (so the user can tell their passkeys apart
// later — e.g. "MacBook Touch ID" vs. "YubiKey"), then runs a full
// registration ceremony against POST .../register/begin and .../finish (see
// server/webauthn.go). requireResidentKey is requested server-side, so most
// platform authenticators will prompt for Touch ID/Face ID/Windows Hello
// directly.
async function addPasskey() {
    const err = document.getElementById('passkeys-error');
    err.textContent = '';

    if (!browserSupportsWebAuthn()) {
        err.textContent = 'This browser does not support passkeys.';
        return;
    }

    const name = (window.prompt('Name this passkey (e.g. "MacBook Touch ID"):', '') || '').trim();
    if (!name) return; // cancelled

    try {
        const beginRes = await apiFetch('/api/webauthn/register/begin', { method: 'POST' });
        if (!beginRes.ok) throw new Error('Could not start passkey registration.');
        const data = await beginRes.json();
        const publicKey = preformatCreateOptions(data.publicKey);

        const cred = await navigator.credentials.create({ publicKey });
        if (!cred) { err.textContent = 'Passkey registration was cancelled.'; return; }

        await apiPost('/api/webauthn/register/finish?name=' + encodeURIComponent(name), credentialCreateToJSON(cred));

        await loadPasskeys();

    } catch (e) {
        // See the matching comment in loginWithPasskey() — NotAllowedError
        // (dismissed prompt, timeout) used to be swallowed with no feedback
        // at all, which was indistinguishable from a hang. Now it at least
        // says something, distinct from a real failure.
        if (e && e.name === 'NotAllowedError') {
            err.textContent = 'Passkey registration was cancelled or timed out.';
        } else {
            err.textContent = (e && e.message) || 'Passkey registration failed.';
        }
    }
}

// removePasskey deletes one of the current user's own passkeys. There is no
// confirmation dialog (unlike the admin-only issue/comment deletes
// elsewhere in this file) because a removed passkey is trivially replaced
// by registering a new one, and the account's password always remains a
// working fallback.
async function removePasskey(id) {
    const err = document.getElementById('passkeys-error');
    err.textContent = '';
    try {
        await apiDelete('/api/webauthn/credentials/' + encodeURIComponent(id));
        await loadPasskeys();
    } catch (e) {
        err.textContent = e.message || 'Could not remove passkey.';
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
    try { await fetch(BASE_PATH + '/api/logout', { method: 'POST' }); } catch {}
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
// asks the server for every team-visible issue updated after _lastSeenAt
// (deliberately NOT scoped by status/priority/project/search — see
// handleListChanges' doc comment for why) and decides relevance itself with
// matchesCurrentFilters(), the same check doSaveIssue() uses after the
// user's own edits:
//
//   • Matches the active filter AND already in _issueWindow: updated in-place
//     (same technique as doSaveIssue: replace the JS object in the array and
//     swap the DOM row). Skipped for the currently-open issue so we don't
//     stomp a user's in-progress edit with a change another user just made.
//
//   • Matches the active filter but NOT in the window: a genuinely new match
//     (new issue, or an issue that just started matching the filter). Counted
//     toward the refresh-hint toast rather than inserted directly, so the
//     user's scroll position isn't disturbed — "Refresh" reloads the window.
//
//   • Does NOT match the active filter but IS in the window: the issue just
//     left the current view (e.g. Resolved while filtering Status=Open).
//     Removed from _issueWindow and the DOM immediately — this is a complete,
//     unambiguous update, so there's nothing for "Refresh" to do and no need
//     to wait for the user to click it.
//
//   • Does NOT match the active filter and is NOT in the window: irrelevant
//     to the current view (e.g. a change in a different project). Ignored
//     entirely — this is what keeps the toast from firing on unrelated
//     database activity.
//
// _lastSeenAt is advanced after each batch so the next poll only requests
// changes that occurred since this poll ran.
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
        let removed = 0;
        for (const iss of changed) {
            const idx = _issueWindow.findIndex(i => i.id === iss.id);
            const matches = matchesCurrentFilters(iss);
            if (idx !== -1) {
                if (iss.id === _currentId) {
                    // Currently open in the detail panel: leave the row and
                    // window entry alone regardless of match — don't stomp an
                    // in-progress edit or yank the panel out from under the user.
                } else if (matches) {
                    _issueWindow[idx] = iss;
                    const tr = document.querySelector(`#issues-tbody tr[data-id="${iss.id}"]`);
                    if (tr) tr.outerHTML = issueRow(iss);
                } else {
                    _issueWindow.splice(idx, 1);
                    const tr = document.querySelector(`#issues-tbody tr[data-id="${iss.id}"]`);
                    if (tr) tr.remove();
                    removed++;
                }
            } else if (matches) {
                externalChanges++;
            }
            if (iss.updated_at > (_lastSeenAt || '')) _lastSeenAt = iss.updated_at;
        }
        if (removed > 0) {
            _totalIssues = Math.max(0, _totalIssues - removed);
            updateIssueCounter();
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
            // p.darkMode was historically a boolean (true = dark, unset/false =
            // light). Newer prefs store one of 'off'/'on'/'auto' directly.
            if (typeof p.darkMode === 'string') {
                _darkModePref = p.darkMode;
            } else if (p.darkMode) {
                _darkModePref = 'on';
            }
            if (p.keepLoggedIn) {
                _keepLoggedIn = true;
            }
            // usePasskeys defaults to true (opt-out, not opt-in) — only an
            // explicit "false" previously saved by toggleUsePasskeys turns it
            // off; an absent key (never saved, or an older idtrack version's
            // prefs blob) leaves the true default in place.
            if (p.usePasskeys === false) {
                _usePasskeys = false;
            }
            if (p.desktopMode) {
                _desktopMode = true;
                // Class already set by the <head> inline script; no-op if already present.
                document.documentElement.classList.add('desktop-mode');
            }
            if (p.pageSize && [10,25,50,100,200].includes(p.pageSize)) {
                _pageSize = p.pageSize;
            }
            // Merge saved column visibility over the defaults. Unknown keys in
            // saved data are ignored; missing keys keep the default value.
            if (p.colVisibility) {
                for (const col of Object.keys(_colVisibility)) {
                    if (typeof p.colVisibility[col] === 'boolean') {
                        _colVisibility[col] = p.colVisibility[col];
                    }
                }
            }
        }
    } catch {}
    // Apply column visibility classes now. The <head> inline script already
    // set these classes before first render to prevent a flash; calling
    // applyColVisibility() here keeps _colVisibility in sync with the DOM.
    applyColVisibility();
    applyDarkMode(_darkModePref);
    // Keep 'auto' mode in sync with the browser's color scheme for the rest
    // of the session (e.g. the OS switches to Night Shift mid-session).
    if (window.matchMedia) {
        window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
            if (_darkModePref === 'auto') applyDarkMode('auto');
        });
    }
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

        const res = await fetch(BASE_PATH + '/api/onboarding', {
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
        const res = await fetch(BASE_PATH + '/api/status');
        if (res.ok) statusData = await res.json();
    } catch {}
    if (statusData) {
        if (statusData.idle_timeout)    _idleTimeoutSecs = statusData.idle_timeout;
        if (statusData.app_name)        _appName = statusData.app_name;
        if (statusData.app_description) _appDesc = statusData.app_description;
        _webauthnEnabled = !!statusData.webauthn_enabled;
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
