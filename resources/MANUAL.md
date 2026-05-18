# idtrack User Manual

**idtrack** is a lightweight, self-hosted issue tracker that runs as a single binary over HTTPS. This manual covers everything you need to get started, use the web application, and administer the system.

---

## Table of Contents

1. [Bootstrapping the System](#1-bootstrapping-the-system)
2. [CLI Reference](#2-cli-reference)
3. [Web Application — Regular Users](#3-web-application--regular-users)
4. [Web Application — Admin Features](#4-web-application--admin-features)
5. [Settings and Preferences](#5-settings-and-preferences)
6. [Running as a System Service](#6-running-as-a-system-service)
7. [Running in a Docker Container](#7-running-in-a-docker-container)

---

## 1. Bootstrapping the System

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

> **Note:** By default, idtrack uses a built-in self-signed TLS certificate. Your browser will show a security warning on first visit. Accept the exception to proceed — this is expected for an internal tool. If you have a certificate from a trusted authority, you can configure it with `idtrack default --server-cert` and `--server-key`.

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

## 2. CLI Reference

All CLI commands follow the form `idtrack <command> [subcommand] [options]`.

### `idtrack default`

Save default settings to `~/.idtrack/defaults.json`. At least one flag is required. Running `idtrack default` with no flags prints the current saved defaults.

| Flag | Description |
| --- | --- |
| `--port N` | Default HTTPS port (default: 8443) |
| `--database PATH` | Default path to the SQLite database file |
| `--server-cert PATH` | Path to a PEM-encoded TLS certificate file. If omitted, the built-in self-signed certificate is used. Use `off` to revert to the built-in certificate. |
| `--server-key PATH` | Path to a PEM-encoded TLS private key file. If omitted, the built-in self-signed key is used. Use `off` to revert to the built-in key. |
| `--idle-timeout DURATION` | Idle logout timer, e.g. `30m`, `1h`, `90s`. Use `0` or `off` to disable. |
| `--app-name TEXT` | Custom application name shown in the header and About dialog |
| `--app-description TEXT` | Custom tagline shown under the name on the login screen and About dialog |
| `--backup-interval DURATION` | How often to create a backup, e.g. `1h`, `30m`. Use `0` or `off` to disable backups entirely. |
| `--backup-count N` | Maximum number of backup files to keep. Oldest files are deleted first when the count is exceeded. Use `0` or `off` for no limit. |
| `--backup-age DURATION` | Delete backup files whose name-embedded timestamp is older than this duration, e.g. `168h` (7 days). Use `0` or `off` for no age limit. |
| `--backup-size SIZE` | Maximum total disk space for backup files, e.g. `500mb`, `2gb`. Accepts suffixes `b`, `kb`, `mb`, `gb`, `tb` (case-insensitive); decimal values like `.5gb` are accepted. When the total size exceeds this limit, older backups are thinned using a Time Machine-style density algorithm — see [Retention policy](#retention-policy) below. Use `0` or `off` for no size limit. |

The path given to `--server-cert` and `--server-key` must already exist; the command validates the file before saving and stores its absolute path.

```sh
idtrack default --port 9000 --database ~/myproject/issues.db
idtrack default --idle-timeout 30m
idtrack default --app-name "ACME Tracker" --app-description "ACME Engineering Issues"
idtrack default --backup-interval 1h --backup-count 24 --backup-age 168h
idtrack default --backup-interval 1h --backup-size 500mb
idtrack default --server-cert /etc/ssl/certs/mysite.crt --server-key /etc/ssl/private/mysite.key
```

---

### `idtrack serve`

Start the server in the background.

| Flag | Description |
| --- | --- |
| `--port N` | Override the port for this run |
| `--database PATH` | Override the database path for this run |
| `--server-cert PATH` | Override the TLS certificate file for this run |
| `--server-key PATH` | Override the TLS private key file for this run |

The server process is detached from the terminal. Its PID is written to `~/.idtrack/idtrack.pid` and its output is logged to `~/.idtrack/idtrack.log`.

If `--server-cert` and `--server-key` are not specified (either on the command line or via `idtrack default`), the server uses its built-in self-signed certificate. Your browser will show a security warning for self-signed certificates — this is expected. To avoid the warning, configure a certificate from a trusted authority using the flags above.

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

## 3. Web Application — Regular Users

Navigate to `https://localhost:8443` (or your configured host and port) to access the web interface.

### Mobile and Responsive Layout

The web app adapts automatically to the size of your browser window or device screen.

**On phones and small screens (up to about 600 px wide):**

- The filter controls move to a scrollable strip below the header rather than sitting inside it.
- The issue list shows only the **#**, **Title**, **Priority**, and **Status** columns. All other columns — Project, Component, Reporter, Assignee, Created, Resolved, and Comments — are hidden to keep the table readable. All fields remain visible when you open an issue.
- The **Columns ▾** button is hidden on phone screens. Column preferences apply on larger screens only.
- Dialogs and overlays slide up from the bottom of the screen.
- Form fields that normally sit side-by-side (such as the two password fields) stack vertically.

**On tablets and medium screens (up to about 900 px wide):**

- The issue list and the detail panel stack vertically instead of sitting side by side.
- Opening an issue takes over the full screen. Tap **← Back** to return to the list.

If you prefer to see the full desktop layout regardless of screen size — for example, when using a phone in landscape mode and are comfortable with pinch-to-zoom — see [Always show desktop version](#settings) in the Settings section.

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

#### Choosing Columns

Click the **Columns ▾** button in the top-right corner of the header (between **+ New Issue** and your name) to open a column picker. Check or uncheck any column to show or hide it immediately. The **#** (ID) and **Title** columns are always visible and cannot be hidden.

| Column | Shown by default | Description |
| --- | --- | --- |
| Project | Yes | The project the issue belongs to |
| Component | Yes | The component within that project |
| Status | Yes | Open or Resolved |
| Priority | Yes | High, Medium, or Low |
| Reporter | No | Who filed the issue |
| Assigned To | Yes | Who the issue is currently assigned to |
| Created | Yes | When the issue was first opened |
| Resolved | No | When the issue was resolved; blank if still open or if the date is unknown |
| Comments | No | Number of comments on the issue |

Your column choices are saved in the browser and persist across sessions on the same device. On phone-sized screens the **Columns ▾** button is hidden and the compact column set (# Title Priority Status) is always used regardless of your preferences — use **Always show desktop version** in Settings if you need access to all columns on a small screen.

> **Note on the Resolved date:** When a previously open issue is marked Resolved the resolved date is recorded automatically. If an issue is reopened and later resolved again, a fresh resolved date is stamped. For issues that were resolved before this feature was added, the system uses the timestamp of the most recent comment as the best available approximation; issues with no comments will show no resolved date.

#### Sorting

Click any column header to sort by that column. Click again to reverse the sort order. Sortable columns: **#**, **Title**, **Project**, **Component**, **Priority**, **Status**, **Reporter**, **Assigned To**, **Created**, **Resolved**. The **Comments** column is not sortable. Hidden columns can still be sorted — the sort applies to all matching issues, not only those visible in the current page.

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

## 4. Web Application — Admin Features

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

### Backups

Backups are controlled entirely via the CLI and are not configurable from the web UI. When backups are enabled the server automatically maintains copies of the SQLite database file in an `idtrack-backups/` directory placed alongside the database file.

#### Enabling backups

Set `--backup-interval` to a non-zero duration with `idtrack default`:

```sh
idtrack default --backup-interval 1h
```

You can combine backup settings in one command:

```sh
# Keep the 24 most recent backups, discarding any older than 7 days
idtrack default --backup-interval 1h --backup-count 24 --backup-age 168h

# Cap total backup storage at 500 MB using density-aware thinning
idtrack default --backup-interval 1h --backup-size 500mb
```

To disable backups, set the interval back to zero:

```sh
idtrack default --backup-interval 0
```

#### How backups work

When the server starts with a non-zero backup interval it:

1. Creates an `idtrack-backups/` directory next to the database file (if it does not already exist).
2. Writes an initial backup immediately, before serving any requests.
3. Writes a new backup automatically every `--backup-interval` thereafter.

Backup files are named `idtrack-YYYYMMDDTHHMMSS.db` (UTC timestamp embedded in the filename). Alphabetical order equals chronological order, which makes it easy to identify the most recent backup.

While a backup copy is in progress the server briefly pauses new requests so the database file is stable. In-flight requests complete normally; any requests that arrive during the copy wait a fraction of a second and then proceed without error. No data is lost and clients see no visible interruption.

#### Retention policy

After each backup the server applies the configured retention rules in the following order. All three strategies can be active at the same time; each is applied only if its threshold is set.

1. **Size limit** (`--backup-size SIZE`): If the total disk space used by all backup files exceeds the configured size, older backups are thinned using a *density-aware* algorithm that preserves the most useful backups:
   - Every backup from the **last hour** is kept.
   - The **most recent backup per hour** is kept for each of the preceding 23 hours.
   - The **most recent backup per day** is kept for older backups.

   When trimming is needed, files are deleted in priority order: extra copies within each hourly slot first (newest slot first), then extra copies within each daily slot, then the hourly backup that is about to age into the daily range (if a daily backup already covers that day), and finally the oldest daily backup. This approach mirrors Apple Time Machine's philosophy of keeping denser recent history and sparser older history.

2. **Count limit** (`--backup-count N`): If there are still more than N backup files after size thinning, the oldest are deleted until only N remain.

3. **Age limit** (`--backup-age DURATION`): Any backup file whose name-embedded timestamp is older than `now − DURATION` is deleted. The filename is used as the age source, not the file-system modification time.

#### Restoring from a backup

To restore the database to a previous state:

1. Stop the server: `idtrack stop`
2. Replace the live database file with the desired backup:
   ```sh
   cp /path/to/idtrack-backups/idtrack-20260517T120000.db /path/to/idtrack.db
   ```
3. Restart the server: `idtrack serve`

Backup files are complete, self-contained SQLite databases and can be opened with any SQLite-compatible tool for inspection or data recovery.

---

## 5. Settings and Preferences

Open the hamburger menu (☰) and choose **Settings**.

### Dark Mode

Toggle **Dark Mode** on or off. This preference is saved in your browser's `localStorage` and persists across sessions.

### Keep me logged in

When enabled, the server issues a 30-day session cookie and stores your user display information in `localStorage` so the next time you open the app you are signed in automatically without re-entering your password. No password or credential is stored in the browser — only your display name and username.

If the 30-day session cookie has expired, the first page load will redirect you to the login screen regardless of this setting.

Choosing **Sign out** always clears the stored information and invalidates the session, regardless of this setting. The next visit will require a manual login.

> **Note:** The long-lived session persists until you sign out or the 30-day cookie expires. Use this setting only on devices you trust and control.

### Issues per page

Choose how many issues are loaded at a time as you scroll through the list: **10**, **25**, **50** (default), **100**, or **200**. Larger values fetch more rows per network request; smaller values are faster to load and useful for testing filters. The page size is saved in your browser and persists across sessions.

> **Note:** The full issue list is still available regardless of this setting — when you scroll to the bottom of the loaded rows, the next page loads automatically. The counter above the list ("Showing X of Y issues") tells you how many have been loaded versus the total matching the current filters.

### Always show desktop version

When this setting is enabled the responsive layout rules are disabled entirely, and the app renders exactly as it does on a desktop browser regardless of screen width. All columns selected in the column picker are visible, the filter bar stays in the header, and dialogs appear centred rather than as bottom drawers.

This is useful on a phone or tablet when you want access to columns or layout elements that are otherwise hidden on small screens. The trade-off is that everything will be smaller than normal — you will likely need to pinch-to-zoom to read and interact with parts of the page comfortably.

The preference is saved in your browser's `localStorage` and persists across sessions, on the same device and browser.

### Sign Out

Open the hamburger menu and choose **Sign out**. This clears your session, removes any stored credentials (if **Keep me logged in** was on), and returns you to the login screen.

---

## About

Open the hamburger menu and choose **About** to see the version number, build date, and a link to the project on GitHub.

---

## 6. Running as a System Service

idtrack can be installed as an automatically started background service using the platform's native service manager. Two helper scripts in the `tools/` directory handle installation, configuration, and removal.

| Platform | Service manager | Script |
| --- | --- | --- |
| macOS | launchd | `tools/install-service-macos.sh` |
| Linux (Ubuntu, Debian, Fedora, RHEL, Arch, …) | systemd | `tools/install-service-linux.sh` |

Both scripts accept the same idtrack configuration options (database path, port, TLS certificate, backup settings, branding) and generate the appropriate service definition file from those values.

### macOS — launchd

macOS manages background services through **launchd**, its built-in service supervisor. Services are described by XML property list (`.plist`) files that specify the command to run, where to write logs, and whether to restart on failure.

idtrack can be installed as either a **LaunchAgent** or a **LaunchDaemon**:

- **LaunchAgent** (default) — runs as your user account, starts automatically when you log in. No `sudo` required. Suitable for a personal Mac.
- **LaunchDaemon** (`--system`) — runs as a specified user, starts at boot before any login. Requires `sudo`. Suitable for a Mac serving as a shared team server.

**Install as a LaunchAgent (personal use):**

```sh
./tools/install-service-macos.sh
```

The database defaults to `~/.idtrack/idtrack.db`. Log output goes to `~/.idtrack/idtrack.log`.

**Install as a LaunchDaemon (shared server):**

```sh
sudo ./tools/install-service-macos.sh \
  --system --run-as yourusername \
  --database /var/lib/idtrack/idtrack.db
```

**With a custom TLS certificate and backup settings:**

```sh
./tools/install-service-macos.sh \
  --cert /etc/ssl/certs/idtrack.crt \
  --key  /etc/ssl/private/idtrack.key \
  --backup-interval 1h \
  --backup-count 48 \
  --backup-age 168h \
  --backup-size 2gb
```

**Managing the service after installation:**

```sh
# Check whether the service is running
launchctl list com.github.tucats.idtrack

# Stop the service
launchctl kill SIGTERM gui/$(id -u)/com.github.tucats.idtrack

# Start the service
launchctl kickstart gui/$(id -u)/com.github.tucats.idtrack

# View logs
tail -f ~/.idtrack/idtrack.log

# Remove the service
./tools/install-service-macos.sh --uninstall
```

Running the install script a second time stops the running service, replaces the plist, and restarts it — useful for updating the configuration without manually uninstalling first.

### Linux — systemd

Modern Linux distributions use **systemd** to manage services. Services are described by unit files that declare the command to run, the user to run it as, and restart behavior.

idtrack can be installed as either a **system service** or a **user service**:

- **System service** (default) — runs as a dedicated `idtrack` system account (created automatically), starts at boot. Requires `sudo`. Suitable for a server.
- **User service** (`--user-service`) — runs as your account, starts when your systemd session begins. No `sudo` required. Suitable for a personal workstation.

**Install as a system service:**

```sh
sudo ./tools/install-service-linux.sh
```

The database defaults to `/var/lib/idtrack/idtrack.db` and is owned by the `idtrack` service account. The account is created automatically if it does not exist.

**Install as a user service:**

```sh
./tools/install-service-linux.sh --user-service
```

The database defaults to `~/.idtrack/idtrack.db`.

> **Note:** A user service only runs while your systemd session is active. To keep it running when you are not logged in, enable *lingering* for your account:
> ```sh
> loginctl enable-linger $USER
> ```

**With a custom TLS certificate and backup settings:**

```sh
sudo ./tools/install-service-linux.sh \
  --cert /etc/ssl/certs/idtrack.crt \
  --key  /etc/ssl/private/idtrack.key \
  --backup-interval 1h \
  --backup-count 48 \
  --backup-age 168h \
  --backup-size 2gb
```

**Managing the service after installation:**

```sh
# Check status
systemctl status idtrack

# Stop / start / restart
systemctl stop    idtrack
systemctl start   idtrack
systemctl restart idtrack

# View logs (live)
journalctl -u idtrack -f

# Remove the service
sudo ./tools/install-service-linux.sh --uninstall
```

For user services, prefix each `systemctl` command with `--user`:
```sh
systemctl --user status  idtrack
systemctl --user restart idtrack
journalctl --user -u idtrack -f
```

Running the install script again on an already-installed service stops it, updates the unit file, and restarts it.

### All Options

Both install scripts accept the same set of options:

| Option | Description |
| --- | --- |
| `--binary PATH` | Path to the idtrack binary. Default: first `idtrack` in PATH, then `./idtrack`. |
| `--database PATH` | Path to the SQLite database file. |
| `--port PORT` | Server port. Default: 8443. |
| `--cert PATH` | PEM TLS certificate file (optional). |
| `--key PATH` | PEM TLS private key file (optional). |
| `--backup-interval DURATION` | Backup frequency, e.g. `1h`. Use `off` to disable. |
| `--backup-count N` | Maximum backups to keep. Use `off` for no limit. |
| `--backup-age DURATION` | Delete backups older than this. Use `off` for no limit. |
| `--backup-size SIZE` | Maximum total backup storage, e.g. `500mb`, `2gb`. Use `off` for no limit. |
| `--idle-timeout DURATION` | Idle logout timer, e.g. `30m`. Use `off` to disable. |
| `--app-name TEXT` | Custom application name. |
| `--app-description TEXT` | Custom tagline. |
| `--uninstall` | Stop and remove the service. |

macOS-only options:

| Option | Description |
| --- | --- |
| `--system` | Install as a LaunchDaemon (requires sudo). |
| `--run-as USERNAME` | User the daemon runs as (`--system` only). |
| `--log PATH` | Log file path. Default: `~/.idtrack/idtrack.log`. |
| `--label LABEL` | launchd service label. Default: `com.github.tucats.idtrack`. |

Linux-only options:

| Option | Description |
| --- | --- |
| `--user-service` | Install as a user service instead of a system service. |
| `--service-user NAME` | System account to run the service as. Default: `idtrack`. |

---

## 7. Running in a Docker Container

idtrack can run inside a Docker container. The container image is built from source using the provided `Dockerfile` and helper scripts in the `tools/` directory. The SQLite database and backup files are stored on the host machine via a bind mount, so they survive container restarts and upgrades.

### Prerequisites

- Docker installed and running on the host.
- The idtrack source repository (for building the image).

### Building the Image

```sh
./tools/build-container.sh
```

This compiles the binary inside a Docker builder stage, then packages it into a minimal Alpine-based runtime image. The image is tagged with the current version number (from `tools/buildver.txt`) and also as `latest`:

```
idtrack:1.0-34
idtrack:latest
```

Additional build options:

```sh
# Force a full rebuild (bypass the layer cache)
./tools/build-container.sh --no-cache

# Build for Linux arm64 (e.g. Raspberry Pi, AWS Graviton)
./tools/build-container.sh --platform linux/arm64

# Build and push to a registry
./tools/build-container.sh --name ghcr.io/myorg/idtrack --push
```

### Starting the Container

```sh
./tools/start-container.sh --data /path/to/your/data
```

The `--data` flag is required. It specifies a directory on the **host** machine where the database and backup files will be stored. The directory must already exist.

```sh
# Create the data directory first if it doesn't exist
mkdir -p /var/lib/idtrack

# Start with default settings (port 8443, built-in self-signed certificate)
./tools/start-container.sh --data /var/lib/idtrack
```

The container runs detached (in the background) by default. Open your browser to `https://localhost:8443` and complete the normal onboarding flow to create the first admin account.

For first-run verification, use `--foreground` to watch the startup log directly in the terminal:

```sh
./tools/start-container.sh --data /var/lib/idtrack --foreground
```

### Data Persistence

The database file (`idtrack.db`) and backup directory (`idtrack-backups/`) are written to the directory you specify with `--data`. This directory is bind-mounted at `/data` inside the container. The data is entirely outside the container's writable layer — it is safe to remove, upgrade, or recreate the container without losing any data.

> **Backups** work identically inside the container. The `idtrack-backups/` directory will appear inside your `--data` directory alongside the database file.

### Using a Custom TLS Certificate

By default, idtrack uses its built-in self-signed certificate. To avoid browser security warnings, provide a certificate from a trusted authority:

```sh
./tools/start-container.sh \
  --data /var/lib/idtrack \
  --cert /etc/ssl/certs/tracker.example.com.crt \
  --key  /etc/ssl/private/tracker.example.com.key
```

The certificate and key files are mounted read-only inside the container. Both flags must always be supplied together.

### Common Options

| Option | Description |
| --- | --- |
| `--data DIR` | Host directory for the database and backups (required) |
| `--port PORT` | Host port to expose (default: 8443) |
| `--name NAME` | Container name (default: `idtrack`) |
| `--image IMAGE` | Image to run (default: `idtrack:latest`) |
| `--restart POLICY` | Docker restart policy (default: `unless-stopped`) |
| `--cert PATH` | Host path to a TLS certificate PEM file |
| `--key PATH` | Host path to the matching TLS private key PEM file |
| `--backup-interval DURATION` | How often to back up, e.g. `1h`, `30m`. Use `off` to disable. |
| `--backup-count N` | Maximum number of backups to keep (`off` = no limit) |
| `--backup-age DURATION` | Delete backups older than this, e.g. `168h` (`off` = no limit) |
| `--backup-size SIZE` | Maximum total backup storage, e.g. `500mb`, `2gb` (`off` = no limit) |
| `--idle-timeout DURATION` | Idle logout timer, e.g. `30m` (`off` = disabled) |
| `--app-name TEXT` | Custom application name shown in the header |
| `--app-description TEXT` | Custom tagline on the login screen |
| `--foreground` | Run in the terminal foreground instead of detached |

### Example: Full Production Setup

```sh
./tools/start-container.sh \
  --data    /var/lib/idtrack \
  --port    443 \
  --name    idtrack \
  --cert    /etc/ssl/certs/tracker.example.com.crt \
  --key     /etc/ssl/private/tracker.example.com.key \
  --backup-interval 1h \
  --backup-count    48 \
  --backup-age      168h \
  --backup-size     2gb \
  --idle-timeout    30m \
  --app-name        "ACME Tracker"
```

### Managing a Running Container

```sh
# View live logs
docker logs -f idtrack

# Stop the container (data is preserved)
docker stop idtrack

# Remove the container (data is preserved — it lives in --data)
docker rm idtrack

# Upgrade to a new image
docker stop idtrack
docker rm idtrack
./tools/build-container.sh        # or pull the new image
./tools/start-container.sh --data /var/lib/idtrack   # restart with same options
```

### Technical Note on Foreground Mode

`idtrack serve` normally re-executes the binary as a detached background process — a pattern that works well for direct installation on a host but is incompatible with containers, because the container's main process exits immediately, causing Docker to stop it. The scripts and Dockerfile both use the `--foreground` flag (an internal server flag) to bypass the re-exec mechanism and keep the server running as PID 1 in the container, which is the correct pattern for containerized services.

---

idtrack — lightweight self-hosted issue tracking
