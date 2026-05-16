# idtrack User Manual

**idtrack** is a lightweight, self-hosted issue tracker that runs as a single binary over HTTPS. This manual covers everything you need to get started, use the web application, and administer the system.

---

## Table of Contents

1. [Bootstrapping the System](#bootstrapping)
2. [CLI Reference](#cli-reference)
3. [Web Application — Regular Users](#web-app-users)
4. [Web Application — Admin Features](#web-app-admin)
5. [Settings and Preferences](#settings)

---

## 1. Bootstrapping the System {#bootstrapping}

A fresh idtrack installation requires a few steps before the web app is usable.

### Step 1 — Create an admin user

The database is created automatically on first run, but you need at least one user to log in. Create an initial admin account using the CLI:

```
idtrack user --add alice:s3cr3tpass --name "Alice Smith" --admin true
```

- Replace `alice` with your chosen username and `s3cr3tpass` with a strong password.
- `--name` sets the display name shown in the web UI.
- `--admin true` grants full administrative access.

### Step 2 — Set defaults (optional)

Save your preferred port and database path so you don't need to specify them every time:

```
idtrack default --port 8443 --database /var/data/idtrack.db
```

These values are written to `~/.idtrack/defaults.json` and used as fallbacks by all commands.

### Step 3 — Define at least one project

Issues must belong to a project and component. Create your first project:

```
idtrack define --project "My Project"
```

Then add one or more components to it:

```
idtrack define --project "My Project" --component "Backend"
idtrack define --project "My Project" --component "Frontend"
```

### Step 4 — Start the server

```
idtrack serve
```

The server starts in the background, listening on HTTPS port 8443 by default. Open your browser to `https://localhost:8443`.

> **Note:** idtrack uses a self-signed TLS certificate. Your browser will show a security warning on first visit. Accept the exception to proceed — this is expected for an internal tool.

---

## 2. CLI Reference {#cli-reference}

All CLI commands follow the form `idtrack <verb> [flags]`.

### `idtrack default`

Save default settings to `~/.idtrack/defaults.json`. At least one flag is required.

| Flag | Description |
| --- | --- |
| `--port N` | Default HTTPS port (default: 8443) |
| `--database PATH` | Default path to the SQLite database file |

```
idtrack default --port 9000 --database ~/myproject/issues.db
```

---

### `idtrack serve`

Start the server in the background.

| Flag | Description |
| --- | --- |
| `--port N` | Override the port for this run |
| `--database PATH` | Override the database path for this run |

The server process is detached from the terminal. Its PID is written to `~/.idtrack/idtrack.pid` and its output is logged to `~/.idtrack/idtrack.log`.

---

### `idtrack stop`

Stop the running server. Reads the PID from `~/.idtrack/idtrack.pid` and sends SIGTERM.

---

### `idtrack version`

Print the version number and build timestamp.

```
idtrack version
Version: 1.0-8
Built:   2025-05-15 14:32:00 UTC
```

---

### `idtrack user`

Manage user accounts. All `user` commands accept an optional `--database PATH` flag.

#### List users

```
idtrack user --list
```

Prints a table showing USERNAME, DISPLAY NAME, ADMIN status, and LAST LOGIN time.

#### Add a user

```
idtrack user --add username:password [--name "Display Name"] [--admin true|false]
```

- If `--name` is omitted, the username is used as the display name.
- If `--admin` is omitted, the user is created as a non-admin.
- If the username already exists, the record is updated (upsert).

```
idtrack user --add bob:password123 --name "Bob Jones"
idtrack user --add carol:pass --name "Carol" --admin true
```

#### Update a user

```
idtrack user --update username [--name "New Name"] [--password newpass] [--admin true|false]
```

Only the fields you specify are changed; omitted fields are left unchanged. The user must already exist.

```
idtrack user --update bob --name "Robert Jones"
idtrack user --update bob --password newpass --admin true
```

#### Delete a user

```
idtrack user --delete username
```

Permanently removes the user record. Issues and comments that reference the username retain the username string.

---

### `idtrack define`

Create a new project or add a component to an existing project.

#### Create a project

```
idtrack define --project "Project Name"
```

#### Add a component to a project

```
idtrack define --project "Project Name" --component "Component Name"
```

The project must exist before a component can be added to it.

---

### `idtrack delete`

Remove a project (and all its components) or remove a single component.

#### Delete a project

```
idtrack delete --project "Project Name"
```

This also deletes all components belonging to that project. **This operation fails if any open or resolved issues reference the project.** The error message lists the affected issue IDs.

#### Delete a component

```
idtrack delete --project "Project Name" --component "Component Name"
```

**This operation fails if any issues reference that component.** The error message lists the affected issue IDs.

To proceed with a delete that has blocking issues, you must first re-assign or delete those issues via the web app or edit the database directly.

---

## 3. Web Application — Regular Users {#web-app-users}

Navigate to `https://localhost:8443` (or your configured host and port) to access the web interface.

### Logging In

Enter your username and password on the login screen and click **Sign In**. Your session is preserved for the life of the browser tab — if you refresh the page, you are not required to log in again. Closing the browser tab ends your session.

### Issue List

After login, the main screen shows the issue list. By default it shows all **Open** issues.

#### Filtering Issues

Four filter controls sit above the issue list:

- **Search** — Filter by text that appears anywhere in the title or description.
- **Status** — Choose *All*, *Open*, or *Resolved*.
- **Priority** — Choose *All*, *High*, *Medium*, or *Low*.
- **Project** — Choose *All* or a specific project name.

Filters are cumulative: all active filters are applied simultaneously.

#### Sorting

Click any column header to sort by that column. Click again to reverse the sort order. Sortable columns: **#**, **Title**, **Project**, **Component**, **Priority**, **Status**, **Assignee**, **Created**.

### Viewing an Issue

Click any row in the issue list to open the detail panel on the right. The detail panel shows:

- Issue number (read-only)
- Title (editable)
- Description (editable, multi-line)
- Reporter (read-only — who created the issue)
- Assignee (editable dropdown — choose from all users)
- Project (editable dropdown)
- Component (editable dropdown — filtered to the selected project)
- Priority (editable — High, Medium, or Low)
- Status (editable — Open or Resolved)
- Created and Updated timestamps (read-only)
- Comments thread

If you edit any field, a **Save Changes** button appears at the bottom of the detail panel. Click it to persist your changes, or click **Cancel** to discard them.

> **Note:** If you switch the **Project**, the **Component** resets to *Choose component…*. You must select a new component before saving.

### Adding a Comment

Scroll to the bottom of the detail panel and type in the comment box. Click **Add Comment** to post. Comments are shown in chronological order with the author's display name and the time posted.

### Creating a New Issue

Click the **+ New Issue** button (top right of the issue list). A form slides in asking for:

- **Title** (required)
- **Description** (optional, multi-line)
- **Project** (required — choose from the dropdown)
- **Component** (required — populated after a project is selected)
- **Priority** (required — High, Medium, or Low)
- **Assignee** (optional)

Click **Create** to submit. The new issue appears in the list immediately.

---

## 4. Web Application — Admin Features {#web-app-admin}

Users with **Admin** privilege see additional items in the hamburger menu (☰, top right of the screen).

### Add User

Opens a form to create a new user account:

- **Username** — must be unique
- **Display Name** — shown in the UI; defaults to username if left blank
- **Admin** — toggle to grant admin access
- **Password** / **Confirm Password** — must match; minimum one character

Click **Create User** to save. The new user can log in immediately.

### Edit User

Opens a form to modify an existing account:

1. Select the user from the dropdown.
2. Change any fields: Display Name, Admin status, or Password.
3. Leave **New Password** blank to keep the current password unchanged.
4. Click **Save Changes**.

The **Delete User** button permanently removes the selected account. You cannot delete your own account via this UI.

### Add Project

Opens a form to create a new project. Enter the project name and click **Add Project**. The new project is immediately available in issue dropdowns and the project filter.

### Add Components

Opens a multi-add form for adding one or more components to an existing project:

1. Select the **Project** from the dropdown.
2. Type a component name and click **Add to List** (or press Enter). Duplicate names (case-insensitive) are rejected.
3. Repeat to build up a list of components.
4. Click the **×** next to any list item to remove it before saving.
5. Click **Save Components** to add all listed components at once.

If any component fails to save (e.g., a race condition where another admin created the same name), the failed items remain in the list and an error is shown.

### Delete Project / Component

Opens a form to remove a project or a single component:

- To **delete a component**: choose the project and the specific component, then click **Delete**.
- To **delete an entire project** (including all its components): choose the project and select **All components**, then click **Delete**.

If any issues reference the project or component being deleted, the deletion is refused and the affected issue IDs are displayed. Re-assign or delete those issues first, then retry.

### Delete Issue

When viewing an issue in the detail panel, admins see a **Delete Issue** button in the panel header. Clicking it prompts for confirmation, then permanently removes the issue and all its comments.

### Delete Comment

Each comment has a trash-can icon (🗑) visible only to admins. Clicking it prompts for confirmation, then permanently removes that comment.

---

## 5. Settings and Preferences {#settings}

### Dark Mode

Open the hamburger menu (☰) and choose **Settings**. Toggle **Dark Mode** on or off. This preference is saved in your browser's `localStorage` and persists across sessions.

### Sign Out

Open the hamburger menu and choose **Sign out**. This clears your session data and returns you to the login screen.

---

## About

Open the hamburger menu and choose **About** to see the version number, build date, and a link to the project on GitHub.

---

*idtrack — lightweight self-hosted issue tracking*
