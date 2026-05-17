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

A fresh idtrack installation is up and running in three steps.

### Step 1 — Set defaults (optional)

Save your preferred port and database path so you don't need to specify them every time:

```sh
idtrack default --port 8443 --database /var/data/idtrack.db
```

These values are written to `~/.idtrack/defaults.json` and used as fallbacks by all commands.

### Step 2 — Start the server

```sh
idtrack serve
```

The server starts in the background, listening on HTTPS port 8443 by default. Open your browser to `https://localhost:8443`.

> **Note:** idtrack uses a self-signed TLS certificate. Your browser will show a security warning on first visit. Accept the exception to proceed — this is expected for an internal tool.

### Step 3 — Create the first admin account

When no users exist in the database, the web app detects this automatically and shows an **onboarding dialog** instead of the normal login screen. Fill in:

- **Username** — the login name for the first admin account
- **Display Name** — how the name appears in the UI (defaults to username if left blank)
- **Password** / **Confirm Password** — must match

Click **Create Account**. The account is created with full admin privileges and you are logged in immediately.

> **Alternative:** If you prefer to create the first user from the command line rather than the browser, you can run `idtrack user add username:password --admin true` before starting the server. The onboarding dialog only appears when the database has no users.

### Step 4 — Define at least one project

Issues must belong to a project and component. You can define them from the web UI (via the admin hamburger menu) or the CLI:

```sh
idtrack define project "My Project"
idtrack define component "My Project" Backend
idtrack define component "My Project" Frontend
```

---

## 2. CLI Reference {#cli-reference}

All CLI commands follow the form `idtrack <command> [subcommand] [options]`.

### `idtrack default`

Save default settings to `~/.idtrack/defaults.json`. At least one flag is required.

| Flag | Description |
| --- | --- |
| `--port N` | Default HTTPS port (default: 8443) |
| `--database PATH` | Default path to the SQLite database file |
| `--idle-timeout DURATION` | Idle logout timer, e.g. `30m`, `1h`, `90s`. Use `0` to disable. |

```sh
idtrack default --port 9000 --database ~/myproject/issues.db
idtrack default --idle-timeout 30m
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

```text
idtrack version
Version: 1.0-8
Built:   2025-05-15 14:32:00 UTC
```

---

### `idtrack user`

Manage user accounts. The first argument after `user` is a subcommand: `list`, `add`, `update`, or `delete`. All variants accept an optional `--database PATH` flag.

#### List users

```sh
idtrack user list
```

Prints a table showing USERNAME, DISPLAY NAME, ADMIN status, and LAST LOGIN time.

#### Add a user

```sh
idtrack user add <username:password> [--name "Display Name"] [--admin true|false]
```

- The argument is `username:password` as a single token separated by `:`.
- If `--name` is omitted, the username is used as the display name.
- If `--admin` is omitted, the user is created as a non-admin.
- If the username already exists, the record is updated (upsert).

```sh
idtrack user add bob:password123 --name "Bob Jones"
idtrack user add carol:pass --name "Carol" --admin true
```

#### Update a user

```sh
idtrack user update <username> [--name "New Name"] [--password newpass] [--admin true|false]
```

Only the fields you specify are changed; omitted fields are left unchanged. The user must already exist.

```sh
idtrack user update bob --name "Robert Jones"
idtrack user update bob --password newpass --admin true
```

#### Delete a user

```sh
idtrack user delete <username>
```

Permanently removes the user record. Issues and comments that reference the username retain the username string.

---

### `idtrack define`

Create a new project or add a component to an existing project. The first argument after `define` is a subcommand: `project` or `component`. Both variants accept an optional `--database PATH` flag.

#### Create a project

```sh
idtrack define project <name>
```

#### Add a component to a project

```sh
idtrack define component <project-name> <component-name>
```

The project must exist before a component can be added to it.

```sh
idtrack define project "My Project"
idtrack define component "My Project" Backend
idtrack define component "My Project" Frontend
```

---

### `idtrack delete`

Remove a project (and all its components) or remove a single component. The first argument after `delete` is a subcommand: `project` or `component`. Both variants accept an optional `--database PATH` flag.

#### Delete a project

```sh
idtrack delete project <name>
```

This also deletes all components belonging to that project. **This operation fails if any open or resolved issues reference the project.** The error message lists the affected issue IDs.

#### Delete a component

```sh
idtrack delete component <project-name> <component-name>
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

If you edit any field, a **Save Changes** button appears. Click it to persist your changes, or click **← Back** to discard them.

> **Note:** If you switch the **Project**, the **Component** resets to *Choose component…*. You must select a new component before saving.

#### Changing Status

**Open → Resolved:** When you save an issue with status changed to *Resolved*, a dialog appears asking for optional resolution details:

- **Fixed Version** — the version in which the fix was delivered (optional, e.g. `1.2-34`).
- **Comment** — a free-text description of the resolution (optional).

If either field is filled in, the information is posted as a comment at the same time the status change is saved. A non-empty **Fixed Version** appears as *Fixed in \<version\>* at the top of the comment, followed by any free-text comment.

> **Note:** An **Assignee** is required before an issue can be saved as *Resolved*. The save is blocked with an error if the assignee field is empty.

**Resolved → Open:** Reopening a resolved issue always requires a comment — the dialog will not confirm until a reason is entered. The reason is posted as a comment at the same time the status is changed back to *Open*.

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

### Edit Users…

Opens a panel listing all current user accounts — username, display name, admin status, and last login time. All user management is done from this single overlay.

**To add a user**, click **Add User…**. Fill in:

- **Username** — must be unique; automatically converted to lower-case
- **Display Name** — shown in the UI; defaults to username if left blank
- **Password** / **Confirm Password** — must match; minimum one character
- **Admin privileges** — toggle to grant full admin access

Click **Add User** to save. The new user can log in immediately.

**To edit a user**, click anywhere on that user's row. The edit form opens pre-populated with their current details. You can change any combination of:

- **Display Name**
- **Password** — leave blank to keep the current password unchanged
- **Admin privileges**

Click **Save Changes** to apply.

**To delete a user**, open their edit form (click the row), then click **Delete User** and confirm. You cannot delete your own account. Issues and comments that reference the deleted username retain the username string.

After any add, edit, or delete operation — and when cancelled — the overlay automatically returns to the refreshed user list.

### Edit Projects…

Opens a two-screen interface for managing all projects and their components.

**Project list** — The first screen lists all defined projects, each row showing the project name and component count. Click a row to open its detail screen. Click **+ New Project** to create a project.

**Project detail — existing project**

Click a project row to view and manage its components:

- Each existing component has a trash-can icon (🗑). Click it to permanently delete that component (confirmation required). The deletion is refused if any issues still reference the component; affected issue IDs are shown.
- Type a name in the **Add Component** field and click **Add** (or press Enter) to immediately add a component to the project. Duplicate names are rejected (case-insensitive check).
- Click **Delete Project** to permanently remove the project and all its components. The deletion is refused if any issues reference the project.

**Project detail — new project**

Click **+ New Project** to open the new-project form:

- Enter the **Project Name** (must be unique; case-insensitive duplicate check is applied).
- Optionally stage components before creating: type a name and click **Add**; duplicate names are rejected.
- Click **Create Project** to create the project and all staged components at once. On success the view switches to the existing-project detail for the new project, where you can continue adding components.

Click **← Back** to return to the project list from any detail screen.

### Delete Issue

When viewing an issue in the detail panel, admins see a **Delete** button in the panel header. Clicking it prompts for confirmation, then permanently removes the issue and all its comments.

### Delete Comment

Each comment has a trash-can icon (🗑) visible only to admins. Clicking it prompts for confirmation, then permanently removes that comment.

---

## 5. Settings and Preferences {#settings}

Open the hamburger menu (☰) and choose **Settings**.

### Dark Mode

Toggle **Dark Mode** on or off. This preference is saved in your browser's `localStorage` and persists across sessions.

### Keep me logged in

When enabled, your credentials are stored in your browser's `localStorage` so that the next time you open the app you are signed in automatically without having to enter your password.

Choosing **Sign out** always clears the stored credentials, regardless of this setting. The next visit will require a manual login.

> **Note:** Stored credentials persist until you sign out. Use this setting only on devices you trust and control.

### Sign Out

Open the hamburger menu and choose **Sign out**. This clears your session, removes any stored credentials (if **Keep me logged in** was on), and returns you to the login screen.

---

## About

Open the hamburger menu and choose **About** to see the version number, build date, and a link to the project on GitHub.

---

idtrack — lightweight self-hosted issue tracking
