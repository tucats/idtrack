'use strict';

// =====================================================================
// CONSTANTS
// =====================================================================

const SESSION_KEY = 'idtrack_session';  // sessionStorage: { user, creds }
const PREFS_KEY   = 'idtrack_prefs';   // localStorage:   { darkMode: bool }
const APP_VERSION = '2.0';

// =====================================================================
// STATE
// =====================================================================

let _credentials       = null;   // 'Basic base64(username:sha256hash)'
let _currentUser       = null;   // { username, display_name }
let _userMap           = {};     // username -> display_name
let _userList          = [];     // full user objects from GET /api/users
let _projectData       = [];     // [{name, components: [...]}]
let _pendingComponents = [];     // component names staged in the Add Components overlay
let _allIssues         = [];
let _currentId         = null;
let _sortCol     = 'id';
let _sortAsc     = false;
let _statusFilter   = 'open';
let _priorityFilter = 'all';
let _projectFilter  = 'all';
let _detailDirty = false;
let _darkMode    = false;

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

async function sha256(text) {
    const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
    return Array.from(new Uint8Array(buf)).map(b => b.toString(16).padStart(2,'0')).join('');
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

// =====================================================================
// API LAYER
// =====================================================================

async function apiFetch(url, options = {}) {
    if (!options.headers) options.headers = {};
    if (_credentials) options.headers['Authorization'] = _credentials;

    const res = await fetch(url, options);

    if (res.status === 401) {
        _currentUser = null;
        _credentials = null;
        sessionStorage.removeItem(SESSION_KEY);
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
    const username = document.getElementById('login-user').value.trim();
    const password = document.getElementById('login-pass').value;
    const err      = document.getElementById('login-error');
    const btn      = document.getElementById('login-submit-btn');

    err.textContent = '';
    if (!username || !password) { err.textContent = 'Username and password are required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Signing in…';

    try {
        const hash  = await sha256(password);
        const creds = 'Basic ' + btoa(username + ':' + hash);

        const res = await fetch('/api/login', {
            method: 'POST',
            headers: { 'Authorization': creds },
        });

        if (!res.ok) {
            err.textContent = 'Invalid username or password.';
            return;
        }

        const user = await res.json();
        _credentials = creds;
        _currentUser = { username: user.username, display_name: user.display_name, is_admin: !!user.is_admin };
        sessionStorage.setItem(SESSION_KEY, JSON.stringify({ user: _currentUser, creds: _credentials }));

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
    document.getElementById('menu-add-user').style.display         = adminDisplay;
    document.getElementById('menu-edit-user').style.display        = adminDisplay;
    document.getElementById('menu-add-project').style.display      = adminDisplay;
    document.getElementById('menu-add-component').style.display    = adminDisplay;
    document.getElementById('menu-manage-projects').style.display  = adminDisplay;
    document.getElementById('app').style.display = '';
    await refreshIssues();
    await populateAssigneeDropdowns();
    await populateProjectDropdowns();
}

function doLogout() {
    _currentUser = null;
    _credentials = null;
    sessionStorage.removeItem(SESSION_KEY);
    document.getElementById('app').style.display = 'none';
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

        document.getElementById('detail-save-btn').style.display = 'none';
        document.getElementById('detail-delete-btn').style.display = (_currentUser && _currentUser.is_admin) ? '' : 'none';
        _detailDirty = false;

        renderComments(comments);
        document.getElementById('comment-input').value = '';
        document.getElementById('detail-panel').style.display = '';

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
    const btn       = document.getElementById('detail-save-btn');

    err.textContent = '';
    if (!title)     { err.textContent = 'Title is required.'; return; }
    if (!project)   { err.textContent = 'Project is required.'; return; }
    if (!component) { err.textContent = 'Component is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Saving…';

    try {
        const { issue } = await updateIssue(_currentId, title, desc, priority, status, assignee, project, component);
        _detailDirty = false;
        btn.style.display = 'none';
        document.getElementById('detail-updated').textContent = fmtDateTime(issue.updated_at);

        _allIssues = await fetchIssues();
        renderIssues(_allIssues);

    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Save failed.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Save Changes';
    }
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

function openAddUser() {
    _closeMenuOnOutside();
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
}

async function submitAddUser() {
    const username     = document.getElementById('au-username').value.trim();
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
        const passwordHash = await sha256(password);
        await apiPost('/api/users', { username, display_name: displayName, password_hash: passwordHash, is_admin: isAdmin });
        await populateAssigneeDropdowns();
        hideAddUser();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to add user.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Add User';
    }
}

function openEditUser() {
    _closeMenuOnOutside();
    const sel = document.getElementById('eu-username');
    sel.innerHTML = ['<option value="">Choose user…</option>']
        .concat(_userList.map(u => `<option value="${esc(u.username)}">${esc(u.display_name || u.username)} (${esc(u.username)})</option>`))
        .join('');
    sel.value = '';
    document.getElementById('eu-display-name').value  = '';
    document.getElementById('eu-password').value       = '';
    document.getElementById('eu-confirm').value         = '';
    document.getElementById('eu-admin').checked         = false;
    document.getElementById('eu-error').textContent    = '';
    document.getElementById('eu-delete-btn').style.display = 'none';
    document.getElementById('edit-user-overlay').style.display = 'flex';
}

function hideEditUser() {
    document.getElementById('edit-user-overlay').style.display = 'none';
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
        const passwordHash = password ? await sha256(password) : '';
        await apiPut(`/api/users/${encodeURIComponent(username)}`, {
            display_name: displayName, password_hash: passwordHash, is_admin: isAdmin,
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

function openAddProject() {
    _closeMenuOnOutside();
    document.getElementById('ap-name').value = '';
    document.getElementById('ap-error').textContent = '';
    document.getElementById('add-project-overlay').style.display = 'flex';
    document.getElementById('ap-name').focus();
}

function hideAddProject() {
    document.getElementById('add-project-overlay').style.display = 'none';
}

async function submitAddProject() {
    const name = document.getElementById('ap-name').value.trim();
    const err  = document.getElementById('ap-error');
    const btn  = document.getElementById('ap-submit-btn');

    err.textContent = '';
    if (!name) { err.textContent = 'Project name is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Adding…';
    try {
        await apiPost('/api/projects', { name });
        await populateProjectDropdowns();
        hideAddProject();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Failed to add project.';
    } finally {
        btn.disabled = false;
        btn.textContent = 'Add Project';
    }
}

function openAddComponent() {
    _closeMenuOnOutside();
    _pendingComponents = [];
    document.getElementById('ac-name').value = '';
    document.getElementById('ac-error').textContent = '';
    const sel = document.getElementById('ac-project');
    sel.innerHTML = ['<option value="">Choose project…</option>']
        .concat(_projectData.map(p => `<option value="${esc(p.name)}">${esc(p.name)}</option>`))
        .join('');
    sel.value = '';
    renderPendingComponents();
    document.getElementById('add-component-overlay').style.display = 'flex';
}

function hideAddComponent() {
    document.getElementById('add-component-overlay').style.display = 'none';
}

function onAcProjectChange() {
    _pendingComponents = [];
    document.getElementById('ac-error').textContent = '';
    renderPendingComponents();
    document.getElementById('ac-name').focus();
}

function addPendingComponent() {
    const name = document.getElementById('ac-name').value.trim();
    const err  = document.getElementById('ac-error');
    err.textContent = '';

    const projectName = document.getElementById('ac-project').value;
    if (!projectName) { err.textContent = 'Select a project first.'; return; }
    if (!name)        { err.textContent = 'Enter a component name.'; return; }

    const nameLower = name.toLowerCase();

    const project = _projectData.find(p => p.name === projectName);
    if (project && project.components.some(c => c.toLowerCase() === nameLower)) {
        err.textContent = `"${name}" already exists in this project.`;
        return;
    }
    if (_pendingComponents.some(c => c.toLowerCase() === nameLower)) {
        err.textContent = `"${name}" is already in the list.`;
        return;
    }

    _pendingComponents.push(name);
    document.getElementById('ac-name').value = '';
    document.getElementById('ac-name').focus();
    renderPendingComponents();
}

function removePendingComponent(index) {
    _pendingComponents.splice(index, 1);
    renderPendingComponents();
}

function renderPendingComponents() {
    const listDiv = document.getElementById('ac-pending-list');
    const itemsDiv = document.getElementById('ac-pending-items');
    const btn = document.getElementById('ac-submit-btn');

    if (_pendingComponents.length === 0) {
        listDiv.style.display = 'none';
        btn.disabled = true;
        return;
    }
    listDiv.style.display = '';
    btn.disabled = false;
    itemsDiv.innerHTML = _pendingComponents.map((name, i) => `
        <div class="ac-pending-item">
            <span class="ac-pending-name">${esc(name)}</span>
            <button class="btn-trash" onclick="removePendingComponent(${i})" title="Remove">&#x1F5D1;</button>
        </div>`).join('');
}

async function submitAddComponent() {
    const project = document.getElementById('ac-project').value;
    const err     = document.getElementById('ac-error');
    const btn     = document.getElementById('ac-submit-btn');

    err.textContent = '';
    if (!project)                    { err.textContent = 'Select a project.'; return; }
    if (_pendingComponents.length === 0) { err.textContent = 'Add at least one component.'; return; }

    btn.disabled = true;
    btn.textContent = 'Saving…';

    const failures = {};
    for (const name of [..._pendingComponents]) {
        try {
            await apiPost(`/api/projects/${encodeURIComponent(project)}/components`, { name });
        } catch (e) {
            if (e.message === 'Unauthorized') { btn.textContent = 'Save'; return; }
            failures[name] = e.message || 'failed';
        }
    }

    await populateProjectDropdowns();

    if (Object.keys(failures).length > 0) {
        _pendingComponents = Object.keys(failures);
        renderPendingComponents();
        err.textContent = 'Some components could not be added: ' +
            Object.entries(failures).map(([n, m]) => `${n} (${m})`).join('; ');
        btn.disabled = false;
        btn.textContent = 'Save';
    } else {
        hideAddComponent();
    }
}

function openManageProjects() {
    _closeMenuOnOutside();
    document.getElementById('mp-error').textContent = '';
    const projSel = document.getElementById('mp-project');
    projSel.innerHTML = ['<option value="">Choose project…</option>']
        .concat(_projectData.map(p => `<option value="${esc(p.name)}">${esc(p.name)}</option>`))
        .join('');
    projSel.value = '';
    const compSel = document.getElementById('mp-component');
    compSel.innerHTML = '<option value="">Choose component…</option>';
    compSel.disabled = true;
    document.getElementById('manage-projects-overlay').style.display = 'flex';
}

function hideManageProjects() {
    document.getElementById('manage-projects-overlay').style.display = 'none';
}

function onMpProjectChange() {
    const projectName = document.getElementById('mp-project').value;
    const sel = document.getElementById('mp-component');
    document.getElementById('mp-error').textContent = '';
    if (!projectName) {
        sel.innerHTML = '<option value="">Choose component…</option>';
        sel.disabled = true;
        return;
    }
    const project = _projectData.find(p => p.name === projectName);
    sel.innerHTML = ['<option value="">Choose component…</option>',
        '<option value="__all__">All components (delete entire project)</option>']
        .concat((project ? project.components : []).map(c => `<option value="${esc(c)}">${esc(c)}</option>`))
        .join('');
    sel.disabled = false;
}

async function confirmDeleteProjectOrComponent() {
    const project   = document.getElementById('mp-project').value;
    const component = document.getElementById('mp-component').value;
    const err       = document.getElementById('mp-error');
    const btn       = document.getElementById('mp-delete-btn');

    err.textContent = '';
    if (!project)   { err.textContent = 'Select a project.'; return; }
    if (!component) { err.textContent = 'Select a component (or "All components" to delete the project).'; return; }

    const isAll = component === '__all__';
    const msg = isAll
        ? `Delete project "${project}" and all its components? This cannot be undone.`
        : `Delete component "${component}" from project "${project}"? This cannot be undone.`;
    if (!confirm(msg)) return;

    btn.disabled = true;
    try {
        if (isAll) {
            await apiDelete(`/api/projects/${encodeURIComponent(project)}`);
        } else {
            await apiDelete(`/api/projects/${encodeURIComponent(project)}/components/${encodeURIComponent(component)}`);
        }
        await populateProjectDropdowns();
        hideManageProjects();
    } catch (e) {
        if (e.message !== 'Unauthorized') err.textContent = e.message || 'Delete failed.';
    } finally {
        btn.disabled = false;
    }
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
    document.getElementById('settings-overlay').style.display = 'flex';
}

function hideSettings() {
    document.getElementById('settings-overlay').style.display = 'none';
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

function loadPrefs() {
    try {
        const p = JSON.parse(localStorage.getItem(PREFS_KEY));
        if (p && p.darkMode) {
            _darkMode = true;
            document.body.classList.add('dark');
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
// INITIALIZATION
// =====================================================================

async function init() {
    loadPrefs();

    const saved = sessionStorage.getItem(SESSION_KEY);
    if (saved) {
        try {
            const { user, creds } = JSON.parse(saved);
            if (user && creds) {
                _currentUser = user;
                _credentials = creds;
                await launchApp();
                return;
            }
        } catch {}
    }

    showLogin();
}

document.addEventListener('DOMContentLoaded', init);
