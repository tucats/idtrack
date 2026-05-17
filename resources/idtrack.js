'use strict';

// =====================================================================
// CONSTANTS
// =====================================================================

const SESSION_KEY = 'idtrack_session';  // sessionStorage: { user }
const PREFS_KEY   = 'idtrack_prefs';   // localStorage:   { darkMode, keepLoggedIn }
const PERSIST_KEY = 'idtrack_persist'; // localStorage:   { user } when keepLoggedIn (no credentials)
const APP_VERSION = '2.0';

// =====================================================================
// STATE
// =====================================================================

let _currentUser       = null;   // { username, display_name, is_admin }
let _userMap           = {};     // username -> display_name
let _userList          = [];     // full user objects from GET /api/users
let _projectData       = [];     // [{name, components: [...]}]
let _epProject          = null;  // currently open project in Edit Projects detail (null = new)
let _epPendingComponents = [];   // staged components while creating a new project
let _allIssues         = [];
let _currentId         = null;
let _sortCol     = 'id';
let _sortAsc     = false;
let _statusFilter   = 'open';
let _priorityFilter = 'all';
let _projectFilter  = 'all';
let _detailDirty    = false;
let _originalStatus = 'Open'; // status when the issue was last loaded/saved
let _pendingStatusData = null; // captured fields while status-change dialog is open
let _darkMode       = false;
let _keepLoggedIn   = false;
let _desktopMode    = false;
let _idleTimeoutSecs = 0;          // 0 = disabled; set from /api/status
let _idleTimer       = null;       // setTimeout handle for idle logout
let _appName         = 'idtrack';  // custom branding name; set from /api/status
let _appDesc         = 'Issue Tracker'; // custom branding tagline; set from /api/status

// =====================================================================
// BRANDING
// =====================================================================

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

function esc(s) {
    return String(s == null ? '' : s)
        .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
        .replace(/"/g,'&quot;');
}

function fmtDate(iso) {
    if (!iso) return '';
    try {
        const d = new Date(iso);
        return d.toLocaleDateString(undefined, { year:'numeric', month:'short', day:'numeric' });
    } catch { return iso; }
}

function fmtDateTime(iso) {
    if (!iso) return '';
    try {
        const d = new Date(iso);
        return d.toLocaleDateString(undefined, {year:'numeric',month:'short',day:'numeric'})
             + ' ' + d.toLocaleTimeString(undefined,{hour:'2-digit',minute:'2-digit'});
    } catch { return iso; }
}

function priorityBadge(p) {
    const cls = {High:'badge-high', Medium:'badge-medium', Low:'badge-low'}[p] || 'badge-low';
    return `<span class="badge ${cls}">${esc(p)}</span>`;
}

function statusBadge(s) {
    const cls = s === 'Open' ? 'badge-open' : 'badge-resolved';
    return `<span class="badge ${cls}">${esc(s)}</span>`;
}

function displayName(username) {
    return _userMap[username] || username;
}

// canModifyIssue returns true when the current user is allowed to edit or
// delete the given issue object. Admins, the original reporter, and the
// current assignee all qualify; everyone else is a read-only third party who
// can still view the issue and add comments.
function canModifyIssue(issue) {
    if (!_currentUser || !issue) return false;
    return _currentUser.is_admin
        || _currentUser.username === issue.reporter
        || _currentUser.username === issue.assignee;
}

// =====================================================================
// API LAYER
// =====================================================================

async function apiFetch(url, options = {}) {
    const res = await fetch(url, options);

    if (res.status === 401) {
        _currentUser = null;
        sessionStorage.removeItem(SESSION_KEY);
        localStorage.removeItem(PERSIST_KEY);
        showLogin('Session expired. Please sign in again.');
        throw new Error('Unauthorized');
    }
    return res;
}

async function apiGet(url) {
    const res = await apiFetch(url);
    if (!res.ok) {
        let msg = `Error ${res.status}`;
        try { const d = await res.json(); msg = d.error || msg; } catch {}
        throw new Error(msg);
    }
    return res.json();
}

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

async function fetchUsers() {
    const data = await apiGet('/api/users');
    return data.users || [];
}

async function fetchIssues() {
    const data = await apiGet('/api/issues');
    return data.issues || [];
}

async function fetchIssue(id) {
    return apiGet(`/api/issues/${id}`);  // returns { issue, comments }
}

async function fetchProjects() {
    const data = await apiGet('/api/projects');
    return data.projects || [];
}

async function createIssue(title, description, priority, assignee, project, component) {
    return apiPost('/api/issues', { title, description, priority, assignee, project, component });
}

async function updateIssue(id, title, description, priority, status, assignee, project, component) {
    return apiPut(`/api/issues/${id}`, { title, description, priority, status, assignee, project, component });
}

async function deleteIssue(id) {
    return apiDelete(`/api/issues/${id}`);
}

async function addComment(issueId, body) {
    return apiPost(`/api/issues/${issueId}/comments`, { body });
}

// =====================================================================
// UI — LOGIN
// =====================================================================

function showLogin(msg) {
    document.getElementById('app').style.display = 'none';
    document.getElementById('login-error').textContent = msg || '';
    document.getElementById('login-user').value = '';
    document.getElementById('login-pass').value = '';
    document.getElementById('login-overlay').style.display = 'flex';
    document.getElementById('login-user').focus();
}

async function submitLogin() {
    const username = document.getElementById('login-user').value.trim().toLowerCase();
    const password = document.getElementById('login-pass').value;
    const err      = document.getElementById('login-error');
    const btn      = document.getElementById('login-submit-btn');

    err.textContent = '';
    if (!username || !password) { err.textContent = 'Username and password are required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Signing in…';

    try {
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
        _currentUser = { username: user.username, display_name: user.display_name, is_admin: !!user.is_admin };
        sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user: _currentUser }));
        if (_keepLoggedIn) localStorage.setItem(PERSIST_KEY, JSON.stringify({ user: _currentUser }));

        document.getElementById('login-overlay').style.display = 'none';
        await launchApp();

    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Login failed.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Sign In';
    }
}

// =====================================================================
// UI — MAIN APP
// =====================================================================

async function launchApp() {
    document.getElementById('user-badge').textContent = _currentUser.display_name || _currentUser.username;
    const adminDisplay = _currentUser.is_admin ? '' : 'none';
    document.getElementById('menu-manage-users').style.display   = adminDisplay;
    document.getElementById('menu-edit-projects').style.display  = adminDisplay;
    document.getElementById('app').style.display = '';
    stopIdleTracking();
    startIdleTracking();
    await refreshIssues();
    await populateAssigneeDropdowns();
    await populateProjectDropdowns();
}

async function doLogout() {
    stopIdleTracking();
    // Ask the server to invalidate the session token and clear the cookie.
    try { await fetch('/api/logout', { method: 'POST' }); } catch {}
    _currentUser = null;
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

function setStatusFilter(val) {
    _statusFilter = val;
    renderIssues(_allIssues);
}

function setPriorityFilter(val) {
    _priorityFilter = val;
    renderIssues(_allIssues);
}

function setProjectFilter(val) {
    _projectFilter = val;
    renderIssues(_allIssues);
}

function applyFilters() {
    renderIssues(_allIssues);
}

function toggleSort(col) {
    if (_sortCol === col) {
        _sortAsc = !_sortAsc;
    } else {
        _sortCol = col;
        _sortAsc = (col === 'id') ? false : true;
    }
    updateSortUI();
    renderIssues(_allIssues);
}

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

async function refreshIssues() {
    try {
        _allIssues = await fetchIssues();
        renderIssues(_allIssues);
    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('refreshIssues:', e);
    }
}

function filteredAndSorted(issues) {
    const search = (document.getElementById('search-input') || {}).value || '';
    const q = search.toLowerCase();

    let result = issues.filter(issue => {
        if (_statusFilter !== 'all') {
            const wantOpen = _statusFilter === 'open';
            if (wantOpen && issue.status !== 'Open') return false;
            if (!wantOpen && issue.status !== 'Resolved') return false;
        }
        if (_priorityFilter !== 'all' && issue.priority !== _priorityFilter) return false;
        if (_projectFilter !== 'all' && issue.project !== _projectFilter) return false;
        if (q) {
            const haystack = [issue.title, issue.description, issue.reporter, issue.assignee, issue.project, issue.component].join(' ').toLowerCase();
            if (!haystack.includes(q)) return false;
        }
        return true;
    });

    const priOrder = { High:0, Medium:1, Low:2 };
    result.sort((a, b) => {
        let va = a[_sortCol], vb = b[_sortCol];
        if (_sortCol === 'priority') { va = priOrder[va] ?? 99; vb = priOrder[vb] ?? 99; }
        if (_sortCol === 'id')       { va = Number(va); vb = Number(vb); }
        if (va < vb) return _sortAsc ? -1 : 1;
        if (va > vb) return _sortAsc ?  1 : -1;
        return 0;
    });

    return result;
}

function renderIssues(issues) {
    const visible = filteredAndSorted(issues);
    const tbody   = document.getElementById('issues-tbody');
    const empty   = document.getElementById('issues-empty');
    const table   = document.getElementById('issues-table');

    if (visible.length === 0) {
        table.style.display = 'none';
        empty.style.display = '';
        return;
    }
    table.style.display = '';
    empty.style.display = 'none';

    tbody.innerHTML = visible.map(issue => {
        const sel = issue.id === _currentId ? ' selected' : '';
        return `<tr class="issue-row${sel}" onclick="selectIssue(${issue.id})">
            <td class="col-id">#${esc(String(issue.id))}</td>
            <td class="col-title issue-title-cell">${esc(issue.title)}</td>
            <td class="col-project">${esc(issue.project || '—')}</td>
            <td class="col-component">${esc(issue.component || '—')}</td>
            <td class="col-priority">${priorityBadge(issue.priority)}</td>
            <td class="col-status">${statusBadge(issue.status)}</td>
            <td class="col-assignee">${esc(issue.assignee ? displayName(issue.assignee) : '—')}</td>
            <td class="col-date">${fmtDate(issue.created_at)}</td>
        </tr>`;
    }).join('');

    updateSortUI();
}

// =====================================================================
// UI — ISSUE DETAIL
// =====================================================================

async function selectIssue(id) {
    if (_detailDirty) {
        if (!confirm('You have unsaved changes. Discard them?')) return;
    }
    _currentId   = id;
    _detailDirty = false;

    renderIssues(_allIssues);

    try {
        const { issue, comments } = await fetchIssue(id);
        if (!issue) return;

        document.getElementById('detail-issue-id').textContent = `Issue #${issue.id}`;
        document.getElementById('detail-title').value    = issue.title       || '';
        document.getElementById('detail-status').value   = issue.status      || 'Open';
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
        populateComponentDropdown('detail-component', issue.project || '', issue.component || '');

        const canEdit = canModifyIssue(issue);

        document.getElementById('detail-save-btn').style.display = 'none';
        document.getElementById('detail-delete-btn').style.display = canEdit ? '' : 'none';

        // Disable all editable fields for third-party viewers. Disabled inputs
        // do not fire change events, so markDetailDirty() is never called and
        // the Save button never appears. The comment textarea is intentionally
        // excluded — any authenticated user may add a comment.
        ['detail-title', 'detail-status', 'detail-priority',
         'detail-assignee', 'detail-project', 'detail-component', 'detail-desc']
            .forEach(id => { const el = document.getElementById(id); if (el) el.disabled = !canEdit; });

        _detailDirty = false;

        renderComments(comments);
        document.getElementById('comment-input').value = '';
        document.getElementById('detail-panel').style.display = '';
        const layout = document.getElementById('main-layout');
        if (layout) layout.classList.add('has-detail');

    } catch (e) {
        if (e.message !== 'Unauthorized') console.error('selectIssue:', e);
    }
}

function closeDetail() {
    if (_detailDirty) {
        if (!confirm('You have unsaved changes. Discard them?')) return;
    }
    _currentId   = null;
    _detailDirty = false;
    document.getElementById('detail-panel').style.display = 'none';
    const layout = document.getElementById('main-layout');
    if (layout) layout.classList.remove('has-detail');
    renderIssues(_allIssues);
}

function markDetailDirty() {
    if (!_detailDirty) {
        _detailDirty = true;
        document.getElementById('detail-save-btn').style.display = '';
    }
}

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
    if (status === 'Resolved' && !assignee) { err.textContent = 'An assignee is required before marking an issue Resolved.'; return; }

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

    await doSaveIssue(title, desc, priority, status, assignee, project, component, null);
}

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
        _allIssues = await fetchIssues();
        renderIssues(_allIssues);
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

async function confirmStatusChange() {
    if (!_pendingStatusData) return;
    const version = document.getElementById('sc-version').value.trim();
    const comment = document.getElementById('sc-comment').value.trim();
    const err     = document.getElementById('sc-error');
    err.textContent = '';

    const isReopen = _pendingStatusData.status === 'Open';
    if (isReopen && !comment) {
        err.textContent = 'A reason is required to reopen an issue.';
        return;
    }

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

function renderComments(comments) {
    const el = document.getElementById('comments-list');
    if (!comments || comments.length === 0) {
        el.innerHTML = '<p class="comments-empty">No comments yet.</p>';
        return;
    }
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

async function submitComment() {
    if (!_currentId) return;
    const input = document.getElementById('comment-input');
    const body  = input.value.trim();
    if (!body) return;

    try {
        await addComment(_currentId, body);
        input.value = '';
        const { comments } = await fetchIssue(_currentId);
        renderComments(comments);
        const el = document.getElementById('comments-list');
        if (el) el.scrollIntoView({ behavior: 'smooth', block: 'end' });
    } catch (e) {
        if (e.message !== 'Unauthorized') alert('Failed to add comment: ' + e.message);
    }
}

async function confirmDeleteIssue() {
    if (!_currentId) return;
    if (!confirm(`Delete Issue #${_currentId}? This cannot be undone.`)) return;
    try {
        await deleteIssue(_currentId);
        _allIssues = _allIssues.filter(i => i.id !== _currentId);
        closeDetail();
        renderIssues(_allIssues);
    } catch (e) {
        if (e.message !== 'Unauthorized') alert('Failed to delete issue: ' + (e.message || 'unknown error'));
    }
}

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

async function showNewIssue() {
    document.getElementById('ni-title').value = '';
    document.getElementById('ni-priority').value = 'Medium';
    document.getElementById('ni-desc').value = '';
    document.getElementById('ni-error').textContent = '';
    document.getElementById('ni-project').value = '';
    const niComp = document.getElementById('ni-component');
    niComp.innerHTML = '<option value="">Choose component…</option>';
    niComp.disabled = true;
    await populateAssigneeDropdowns();
    await populateProjectDropdowns();
    document.getElementById('new-issue-overlay').style.display = 'flex';
    document.getElementById('ni-title').focus();
}

function hideNewIssue() {
    document.getElementById('new-issue-overlay').style.display = 'none';
}

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
        await createIssue(title, desc, priority, assignee, project, component);
        hideNewIssue();
        _allIssues = await fetchIssues();
        renderIssues(_allIssues);

        if (_allIssues.length > 0) {
            const newest = _allIssues.reduce((a, b) => Number(a.id) > Number(b.id) ? a : b);
            selectIssue(newest.id);
        }
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
        const prev = sel.value;
        sel.innerHTML = options;
        sel.value = prev;
    });
}

// =====================================================================
// UI — USER MANAGEMENT (admin)
// =====================================================================

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

function openAddUserFromManage() {
    hideManageUsers();
    openAddUser();
}

function openEditUserFromManage(username) {
    hideManageUsers();
    openEditUser(username);
}

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

function hideAddUser() {
    document.getElementById('add-user-overlay').style.display = 'none';
    openManageUsers();
}

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
        await populateAssigneeDropdowns();
        hideAddUser(); // hides overlay and returns to manage users (refreshed)
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to add user.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Add User';
    }
}

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

function hideEditUser() {
    document.getElementById('edit-user-overlay').style.display = 'none';
    openManageUsers();
}

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
        hideEditUser(); // hides overlay and returns to manage users (refreshed)
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Save failed.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Save Changes';
    }
}

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
        hideEditUser(); // hides overlay and returns to manage users (refreshed)
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Delete failed.';
    } finally {
        btn.disabled = false;
    }
}

// =====================================================================
// PROJECT / COMPONENT DROPDOWNS
// =====================================================================

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

function onNiProjectChange() {
    populateComponentDropdown('ni-component', document.getElementById('ni-project').value, '');
}

function onDetailProjectChange() {
    populateComponentDropdown('detail-component', document.getElementById('detail-project').value, '');
    markDetailDirty();
}

// =====================================================================
// UI — EDIT PROJECTS (admin)
// =====================================================================

function openEditProjects() {
    _closeMenuOnOutside();
    document.getElementById('ep-list-error').textContent = '';
    epRenderProjectList();
    document.getElementById('ep-list-overlay').style.display = 'flex';
}

function hideEditProjects() {
    document.getElementById('ep-list-overlay').style.display = 'none';
}

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

function openProjectDetail(name) {
    _epProject = name; // null = new project
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

    document.getElementById('ep-list-overlay').style.display   = 'none';
    document.getElementById('ep-detail-overlay').style.display = 'flex';
    document.getElementById(isNew ? 'ep-project-name' : 'ep-comp-name').focus();
}

function hideProjectDetail() {
    document.getElementById('ep-detail-overlay').style.display = 'none';
    epRenderProjectList();
    document.getElementById('ep-list-overlay').style.display = 'flex';
}

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
            <button class="btn-trash" onclick="epDeleteComponent('${esc(c).replace(/'/g,"\\'")}', event)" title="Delete component">&#x1F5D1;</button>
        </div>`).join('');
}

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

function epRemovePending(index) {
    _epPendingComponents.splice(index, 1);
    epRenderPending();
}

async function epAddComponent() {
    const name = document.getElementById('ep-comp-name').value.trim();
    const err  = document.getElementById('ep-detail-error');
    err.textContent = '';
    if (!name) { err.textContent = 'Enter a component name.'; return; }

    const nameLower = name.toLowerCase();

    if (_epProject === null) {
        // New-project mode: validate the project name is filled, then stage
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

    // Existing project: duplicate check, then POST immediately
    const project = _projectData.find(p => p.name === _epProject);
    if (project && project.components.some(c => c.toLowerCase() === nameLower)) {
        err.textContent = `"${name}" already exists in this project.`;
        return;
    }
    try {
        await apiPost(`/api/projects/${encodeURIComponent(_epProject)}/components`, { name });
        await populateProjectDropdowns();
        document.getElementById('ep-comp-name').value = '';
        document.getElementById('ep-comp-name').focus();
        epRenderComponents();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to add component.';
    }
}

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

    // Project created — now POST any staged components
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

    // Transition to existing-project detail view
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

function backdropClick(event, overlayId, hideFn) {
    if (event.target.id === overlayId) hideFn();
}

function toggleMenu(event) {
    event.stopPropagation();
    const menu = document.getElementById('app-menu');
    const opening = menu.style.display === 'none';
    menu.style.display = opening ? '' : 'none';
    if (opening) {
        document.addEventListener('click', _closeMenuOnOutside, { once: true });
    }
}

function _closeMenuOnOutside() {
    const menu = document.getElementById('app-menu');
    if (menu) menu.style.display = 'none';
}

async function openAbout() {
    _closeMenuOnOutside();
    document.getElementById('about-overlay').style.display = 'flex';
    try {
        const data = await fetch('/api/version').then(r => r.json());
        document.getElementById('about-version').textContent = 'version ' + (data.version || '—');
        const bt = data.build_time || '';
        if (bt.length === 14) {
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

function openSettings() {
    _closeMenuOnOutside();
    document.getElementById('dark-mode-toggle').checked = _darkMode;
    document.getElementById('keep-logged-in-toggle').checked = _keepLoggedIn;
    document.getElementById('desktop-mode-toggle').checked = _desktopMode;
    document.getElementById('settings-overlay').style.display = 'flex';
}

function hideSettings() {
    document.getElementById('settings-overlay').style.display = 'none';
}

function toggleDesktopMode(on) {
    _desktopMode = on;
    document.documentElement.classList.toggle('desktop-mode', on);
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.desktopMode = on;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
}

function toggleDarkMode(on) {
    _darkMode = on;
    document.body.classList.toggle('dark', on);
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}');
        p.darkMode = on;
        localStorage.setItem(PREFS_KEY, JSON.stringify(p));
    } catch {}
}

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

// idleLogout is called when the inactivity timer fires. It clears the screen
// and shows the login form with an explanatory message BEFORE the async
// /api/logout round-trip, so the user never sees the issue list after timeout.
async function idleLogout() {
    stopIdleTracking();
    _currentUser = null;
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
    try { await fetch('/api/logout', { method: 'POST' }); } catch {}
}

function _resetIdleTimer() {
    if (!_idleTimeoutSecs) return;
    if (_idleTimer) clearTimeout(_idleTimer);
    _idleTimer = setTimeout(() => idleLogout(), _idleTimeoutSecs * 1000);
}

function startIdleTracking() {
    if (!_idleTimeoutSecs) return;
    const events = ['mousemove', 'mousedown', 'keydown', 'touchstart', 'scroll', 'click'];
    events.forEach(ev => document.addEventListener(ev, _resetIdleTimer, { passive: true }));
    _resetIdleTimer();
}

function stopIdleTracking() {
    if (_idleTimer) { clearTimeout(_idleTimer); _idleTimer = null; }
    const events = ['mousemove', 'mousedown', 'keydown', 'touchstart', 'scroll', 'click'];
    events.forEach(ev => document.removeEventListener(ev, _resetIdleTimer));
}

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
        }
    } catch {}
}

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

let _onboardingToken = null;

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

        // The server set a session cookie; store the user object for display.
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

async function init() {
    loadPrefs();

    // Always fetch status first to get idle_timeout and onboarding state.
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

    // Session storage: live in-tab session survives page refresh. The session
    // cookie is HttpOnly so JS cannot read it, but the browser sends it
    // automatically — the first authenticated API call in launchApp() will
    // surface a 401 if it has expired, falling back to showLogin().
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

    // Persistent storage: "keep me logged in" across browser sessions. Only
    // the non-sensitive user object is stored; the session cookie (set with
    // MaxAge=30 days at login) carries the credential.
    const persist = localStorage.getItem(PERSIST_KEY);
    if (persist) {
        try {
            const { user } = JSON.parse(persist);
            if (user && user.username) {
                _currentUser = user;
                sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user }));
                // launchApp() will hit 401 and show login if the cookie expired.
                await launchApp();
                return;
            }
        } catch {}
    }

    if (statusData && statusData.onboarding) {
        showOnboarding(statusData.token);
        return;
    }

    showLogin();
}

document.addEventListener('DOMContentLoaded', init);
