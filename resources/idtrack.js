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

let _credentials = null;   // 'Basic base64(username:sha256hash)'
let _currentUser = null;   // { username, display_name }
let _userMap     = {};     // username -> display_name
let _allIssues   = [];
let _currentId   = null;
let _sortCol     = 'id';
let _sortAsc     = false;
let _statusFilter   = 'open';
let _priorityFilter = 'all';
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

async function createIssue(title, description, priority, assignee) {
    return apiPost('/api/issues', { title, description, priority, assignee });
}

async function updateIssue(id, title, description, priority, status, assignee) {
    return apiPut(`/api/issues/${id}`, { title, description, priority, status, assignee });
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
    document.getElementById('app').style.display = '';
    await refreshIssues();
    await populateAssigneeDropdowns();
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

function setStatusFilter(val, btn) {
    _statusFilter = val;
    document.querySelectorAll('.filter-btn[data-status]').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    renderIssues(_allIssues);
}

function setPriorityFilter(val, btn) {
    _priorityFilter = val;
    document.querySelectorAll('.filter-btn[data-priority]').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
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
    const colMap = { id:'col-id', title:'col-title', priority:'col-priority', status:'col-status', reporter:'col-reporter', assignee:'col-assignee', created_at:'col-date' };
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
        if (q) {
            const haystack = [issue.title, issue.description, issue.reporter, issue.assignee].join(' ').toLowerCase();
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
            <td class="col-priority">${priorityBadge(issue.priority)}</td>
            <td class="col-status">${statusBadge(issue.status)}</td>
            <td class="col-reporter">${esc(displayName(issue.reporter))}</td>
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
    const title    = document.getElementById('detail-title').value.trim();
    const status   = document.getElementById('detail-status').value;
    const priority = document.getElementById('detail-priority').value;
    const assignee = document.getElementById('detail-assignee').value;
    const desc     = document.getElementById('detail-desc').value.trim();
    const err      = document.getElementById('detail-error');
    const btn      = document.getElementById('detail-save-btn');

    err.textContent = '';
    if (!title) { err.textContent = 'Title is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Saving…';

    try {
        const { issue } = await updateIssue(_currentId, title, desc, priority, status, assignee);
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
    await populateAssigneeDropdowns();
    document.getElementById('new-issue-overlay').style.display = 'flex';
    document.getElementById('ni-title').focus();
}

function hideNewIssue() {
    document.getElementById('new-issue-overlay').style.display = 'none';
}

async function submitNewIssue() {
    const title    = document.getElementById('ni-title').value.trim();
    const priority = document.getElementById('ni-priority').value;
    const assignee = document.getElementById('ni-assignee').value;
    const desc     = document.getElementById('ni-desc').value.trim();
    const err      = document.getElementById('ni-error');
    const btn      = document.getElementById('ni-submit-btn');

    err.textContent = '';
    if (!title) { err.textContent = 'Title is required.'; return; }

    btn.disabled = true;
    btn.textContent = 'Creating…';

    try {
        await createIssue(title, desc, priority, assignee);
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
