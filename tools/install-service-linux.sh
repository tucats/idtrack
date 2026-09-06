#!/usr/bin/env bash
# =============================================================================
# install-service-linux.sh — Install or remove idtrack as a systemd service
#                             on Linux.
# =============================================================================
#
# idtrack is installed as either a system service or a user service:
#
#   System service (default)
#     Unit file lives in /etc/systemd/system/.  Runs as a dedicated
#     'idtrack' system user (created automatically if it does not exist).
#     Started at boot and restarts automatically on failure.
#     Requires sudo.  Suitable for shared servers.
#
#   User service (--user-service)
#     Unit file lives in ~/.config/systemd/user/.  Runs as the current
#     user.  Started when that user's systemd session begins (typically at
#     login or when lingering is enabled — see NOTE below).
#     No sudo required.  Suitable for personal workstations.
#
# NOTE on user-service auto-start at boot:
#   By default, a user's systemd session only runs while they are logged in.
#   To keep the service running even when no one is logged into the desktop,
#   enable lingering for your account:
#     loginctl enable-linger $USER
#
# Requirements:
#   - A Linux distribution with systemd (Ubuntu 16.04+, Debian 8+,
#     Fedora 15+, RHEL/CentOS 7+, Arch, openSUSE 12.1+).
#   - System-service mode requires sudo.
#
# Usage:
#   ./tools/install-service-linux.sh [options]
#   ./tools/install-service-linux.sh --uninstall [--user-service]
#
# Options:
#   --binary PATH       Path to the idtrack binary.
#                       Default: the first 'idtrack' found in PATH, then
#                       ./idtrack in the current directory.
#
#   --database PATH     Path to the SQLite database file.
#                       Default (system): /var/lib/idtrack/idtrack.db
#                       Default (user):   $HOME/.idtrack/idtrack.db
#
#   --port PORT         HTTPS port the server listens on.  Default: 8443.
#
#   --cert PATH         Path to a PEM TLS certificate file (optional).
#   --key  PATH         Path to the matching PEM private key file (optional).
#                       Both must be supplied together.
#
#   --backup-interval DURATION   Backup interval, e.g. 1h, 30m. Default: off.
#   --backup-count N             Maximum number of backups. Default: off.
#   --backup-age DURATION        Delete backups older than this. Default: off.
#   --backup-size SIZE           Total backup size limit, e.g. 500mb. Default: off.
#
#   --idle-timeout DURATION      Idle logout timer. Default: off.
#   --app-name TEXT              Custom application name.
#   --app-description TEXT       Custom tagline.
#
#   --insecure true|false        Listen with plain HTTP instead of TLS (for
#                                 use behind a TLS-terminating reverse proxy).
#                                 Default: false.
#   --base-path PATH             Mount the whole app under this URL prefix,
#                                 e.g. /idtrack, instead of the origin root.
#
#   --use-defaults       Fill in any option above that was not given
#                       explicitly on this command line from the current
#                       `idtrack default` settings for the account that will
#                       run the service (see "Settings persistence" below).
#                       An explicit flag always wins over an inherited
#                       default.
#
#   --service-user NAME  (System mode only) The system user the service runs
#                        as.  Created automatically if it does not exist.
#                        Default: idtrack
#
#   --user-service      Install as a user service instead of a system service.
#                       No sudo required; runs as the current user.
#
#   --uninstall         Stop, disable, and remove the service.
#
#   --help, -h          Show this help and exit.
#
# Examples:
#   # System service with default settings (database at /var/lib/idtrack/)
#   sudo ./tools/install-service-linux.sh
#
#   # System service with custom TLS and backup settings
#   sudo ./tools/install-service-linux.sh \
#     --database /srv/idtrack/idtrack.db \
#     --cert /etc/ssl/certs/idtrack.crt \
#     --key  /etc/ssl/private/idtrack.key \
#     --backup-interval 1h \
#     --backup-count 48 \
#     --backup-age 168h
#
#   # User service (no sudo needed)
#   ./tools/install-service-linux.sh --user-service
#
#   # System service picking up port/database/backup/etc. from `idtrack
#   # default`, overriding only the port
#   sudo ./tools/install-service-linux.sh --use-defaults --port 9443
#
#   # Remove system service
#   sudo ./tools/install-service-linux.sh --uninstall
#
#   # Remove user service
#   ./tools/install-service-linux.sh --uninstall --user-service
#
# Settings persistence:
#   `idtrack serve` only accepts --port, --database, --server-cert/
#   --server-key, --insecure, and --base-path on its own command line —
#   everything else (backup-interval/-count/-age/-size, idle-timeout,
#   app-name, app-description) is read by the server straight out of
#   ~/.idtrack/defaults.json at startup, with no per-invocation flag. So this
#   script writes those values into defaults.json with `idtrack default`
#   before generating the unit file, rather than (incorrectly) passing them
#   as ExecStart arguments to `idtrack serve`, where they would make the
#   service fail immediately with "unknown option".
#
#   For a user service this defaults.json is simply $HOME/.idtrack/ for the
#   invoking user. For a system service, though, the server runs as
#   --service-user — normally a freshly created system account with no home
#   directory at all (see `useradd --no-create-home` below), so there is
#   nowhere sensible for that account's $HOME to resolve to on its own. This
#   script gives the service account a fixed home at /var/lib/idtrack purely
#   for ~/.idtrack/defaults.json (independent of wherever --database itself
#   points), and the generated unit file sets the same HOME= so the running
#   server resolves the identical path at boot.
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
BINARY=""
DATABASE=""
PORT=""
CERT_FILE=""
KEY_FILE=""
BACKUP_INTERVAL=""
BACKUP_COUNT=""
BACKUP_AGE=""
BACKUP_SIZE=""
IDLE_TIMEOUT=""
APP_NAME=""
APP_DESC=""
INSECURE=""
BASE_PATH=""
SERVICE_USER="idtrack"
USER_SERVICE=0
UNINSTALL=0
USE_DEFAULTS=0

# Fixed home for the system service account's ~/.idtrack/defaults.json — see
# "Settings persistence" in the header comment above for why this can't just
# be the account's own (usually absent) home directory.
SERVICE_HOME="/var/lib/idtrack"

SERVICE_NAME="idtrack"

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --binary)       BINARY="$2";          shift 2 ;;
        --database)     DATABASE="$2";        shift 2 ;;
        --port)         PORT="$2";            shift 2 ;;
        --cert)         CERT_FILE="$2";       shift 2 ;;
        --key)          KEY_FILE="$2";        shift 2 ;;
        --backup-interval) BACKUP_INTERVAL="$2"; shift 2 ;;
        --backup-count)    BACKUP_COUNT="$2";    shift 2 ;;
        --backup-age)      BACKUP_AGE="$2";      shift 2 ;;
        --backup-size)     BACKUP_SIZE="$2";     shift 2 ;;
        --idle-timeout)    IDLE_TIMEOUT="$2";    shift 2 ;;
        --app-name)        APP_NAME="$2";        shift 2 ;;
        --app-description) APP_DESC="$2";        shift 2 ;;
        --insecure)        INSECURE="$2";        shift 2 ;;
        --base-path)       BASE_PATH="$2";       shift 2 ;;
        --use-defaults)    USE_DEFAULTS=1;       shift ;;
        --service-user)    SERVICE_USER="$2";    shift 2 ;;
        --user-service)    USER_SERVICE=1;       shift ;;
        --uninstall)       UNINSTALL=1;          shift ;;
        --help|-h)
            sed -n '/^# Usage:/,/^# ====/p' "$0" | sed 's/^# \?//'
            exit 0
            ;;
        *)
            echo "error: unknown option: $1" >&2
            echo "Run '$0 --help' for usage." >&2
            exit 1
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Verify systemd is available
# ---------------------------------------------------------------------------
if ! command -v systemctl &>/dev/null; then
    echo "error: systemctl not found — this script requires systemd." >&2
    echo "Supported distributions: Ubuntu 16.04+, Debian 8+, Fedora 15+," >&2
    echo "RHEL/CentOS 7+, Arch Linux, openSUSE 12.1+." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Determine unit file location and systemctl scope
# ---------------------------------------------------------------------------
if [[ "${USER_SERVICE}" -eq 1 ]]; then
    UNIT_DIR="${HOME}/.config/systemd/user"
    SYSTEMCTL="systemctl --user"
    # User services run as the current user — no dedicated service account.
    SERVICE_USER="${USER}"
else
    UNIT_DIR="/etc/systemd/system"
    SYSTEMCTL="systemctl"

    # System mode requires root (sudo).
    if [[ "${EUID}" -ne 0 ]]; then
        echo "error: system service installation requires root privileges." >&2
        echo "Run with sudo, or use --user-service to install for your account only." >&2
        exit 1
    fi
fi

UNIT_FILE="${UNIT_DIR}/${SERVICE_NAME}.service"

# ---------------------------------------------------------------------------
# Uninstall path
# ---------------------------------------------------------------------------
if [[ "${UNINSTALL}" -eq 1 ]]; then
    if [[ ! -f "${UNIT_FILE}" ]]; then
        echo "Unit file not found: ${UNIT_FILE}"
        echo "Nothing to remove."
        exit 0
    fi

    echo "Stopping and disabling idtrack service..."

    # Stop and disable; errors are non-fatal if the service was already stopped.
    ${SYSTEMCTL} stop    "${SERVICE_NAME}" 2>/dev/null || true
    ${SYSTEMCTL} disable "${SERVICE_NAME}" 2>/dev/null || true

    rm -f "${UNIT_FILE}"
    ${SYSTEMCTL} daemon-reload

    echo "Service removed.  Your database files have not been touched."
    exit 0
fi

# ---------------------------------------------------------------------------
# Locate the idtrack binary
# ---------------------------------------------------------------------------
if [[ -z "${BINARY}" ]]; then
    if command -v idtrack &>/dev/null; then
        BINARY="$(command -v idtrack)"
    elif [[ -x "./idtrack" ]]; then
        BINARY="$(pwd)/idtrack"
    else
        echo "error: idtrack binary not found." >&2
        echo "Install idtrack to a directory in PATH, or pass --binary /path/to/idtrack." >&2
        exit 1
    fi
fi

# Resolve to an absolute path.
BINARY="$(cd "$(dirname "${BINARY}")" && pwd)/$(basename "${BINARY}")"

if [[ ! -x "${BINARY}" ]]; then
    echo "error: binary not executable: ${BINARY}" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# System service: create the dedicated service account
# ---------------------------------------------------------------------------
# Done here — before any defaults are read or written below — so that the
# --use-defaults / settings-persistence steps further down have an account
# to run `idtrack default` as. The data directory itself (which may differ
# from SERVICE_HOME if --database points elsewhere) is created later, once
# DATABASE is resolved.
if [[ "${USER_SERVICE}" -eq 0 ]]; then
    # Create the system user if it doesn't already exist.
    # -r / --system  — no home directory, no password, UID in the system range
    # -s             — login shell set to nologin for security
    # -M             — don't create a home directory
    if ! id "${SERVICE_USER}" &>/dev/null; then
        echo "Creating system user '${SERVICE_USER}'..."
        NOLOGIN="$(command -v nologin 2>/dev/null || echo /usr/sbin/nologin)"
        useradd --system --no-create-home --shell "${NOLOGIN}" "${SERVICE_USER}"
    fi

    mkdir -p "${SERVICE_HOME}"
    chown "${SERVICE_USER}:${SERVICE_USER}" "${SERVICE_HOME}"
fi

# idtrack_default: run `idtrack default ...` as the account that will
# actually run the service, so it reads/writes the same defaults.json the
# server itself will read at boot (see "Settings persistence" above).
idtrack_default() {
    if [[ "${USER_SERVICE}" -eq 0 ]]; then
        sudo -u "${SERVICE_USER}" env HOME="${SERVICE_HOME}" "${BINARY}" default "$@"
    else
        "${BINARY}" default "$@"
    fi
}

# ---------------------------------------------------------------------------
# --use-defaults: inherit unset options from the current idtrack defaults
# ---------------------------------------------------------------------------
# `idtrack default --export-shell` prints the target account's existing
# ~/.idtrack/defaults.json as IDTRACK_DEFAULT_<NAME> shell assignments (see
# commands/defaults.go's exportDefaultsShell). Eval'ing that output and then
# filling with bash's "${VAR:=fallback}" form only touches a variable this
# script did not already receive an explicit value for above — an explicit
# flag on this command line always wins over an inherited default.
if [[ "${USE_DEFAULTS}" -eq 1 ]]; then
    eval "$(idtrack_default --export-shell)"

    : "${DATABASE:=${IDTRACK_DEFAULT_DATABASE}}"
    : "${PORT:=${IDTRACK_DEFAULT_PORT}}"
    : "${CERT_FILE:=${IDTRACK_DEFAULT_SERVER_CERT}}"
    : "${KEY_FILE:=${IDTRACK_DEFAULT_SERVER_KEY}}"
    : "${BACKUP_INTERVAL:=${IDTRACK_DEFAULT_BACKUP_INTERVAL}}"
    : "${BACKUP_COUNT:=${IDTRACK_DEFAULT_BACKUP_COUNT}}"
    : "${BACKUP_AGE:=${IDTRACK_DEFAULT_BACKUP_AGE}}"
    : "${BACKUP_SIZE:=${IDTRACK_DEFAULT_BACKUP_SIZE}}"
    : "${IDLE_TIMEOUT:=${IDTRACK_DEFAULT_IDLE_TIMEOUT}}"
    : "${APP_NAME:=${IDTRACK_DEFAULT_APP_NAME}}"
    : "${APP_DESC:=${IDTRACK_DEFAULT_APP_DESCRIPTION}}"
    : "${INSECURE:=${IDTRACK_DEFAULT_INSECURE}}"
    : "${BASE_PATH:=${IDTRACK_DEFAULT_BASE_PATH}}"
fi

# ---------------------------------------------------------------------------
# Apply defaults that depend on mode
# ---------------------------------------------------------------------------
if [[ "${USER_SERVICE}" -eq 1 ]]; then
    : "${DATABASE:=${HOME}/.idtrack/idtrack.db}"
else
    : "${DATABASE:=/var/lib/idtrack/idtrack.db}"
fi

DB_DIR="$(dirname "${DATABASE}")"

# ---------------------------------------------------------------------------
# Validate cert/key — both must be present together
# ---------------------------------------------------------------------------
if [[ -n "${CERT_FILE}" && -z "${KEY_FILE}" ]]; then
    echo "error: --cert requires --key to also be specified" >&2
    exit 1
fi
if [[ -n "${KEY_FILE}" && -z "${CERT_FILE}" ]]; then
    echo "error: --key requires --cert to also be specified" >&2
    exit 1
fi

if [[ -n "${CERT_FILE}" ]]; then
    [[ -f "${CERT_FILE}" ]] || { echo "error: cert file not found: ${CERT_FILE}" >&2; exit 1; }
    [[ -f "${KEY_FILE}"  ]] || { echo "error: key file not found: ${KEY_FILE}" >&2; exit 1; }
    CERT_FILE="$(cd "$(dirname "${CERT_FILE}")" && pwd)/$(basename "${CERT_FILE}")"
    KEY_FILE="$(cd  "$(dirname "${KEY_FILE}")"  && pwd)/$(basename "${KEY_FILE}")"
fi

# ---------------------------------------------------------------------------
# Create the data directory (the service account itself was already created
# above, before defaults were read/written)
# ---------------------------------------------------------------------------
if [[ "${USER_SERVICE}" -eq 0 ]]; then
    # Create the database directory and give the service user ownership so
    # idtrack can create and write the database and backup files. This may
    # be the same directory as SERVICE_HOME (the default --database path
    # lives under it) or a different one if --database was overridden —
    # either way it's independent of where the account's defaults.json
    # lives.
    mkdir -p "${DB_DIR}"
    chown "${SERVICE_USER}:${SERVICE_USER}" "${DB_DIR}"
    chmod 750 "${DB_DIR}"
else
    # User service: just ensure the database directory exists.
    mkdir -p "${DB_DIR}"
    mkdir -p "${UNIT_DIR}"
fi

# Resolve DATABASE to an absolute path now that the directory exists.
# systemd services start with a working directory of / by default, so a
# relative path in ExecStart= would silently resolve to the wrong location.
DATABASE="$(cd "${DB_DIR}" && pwd)/$(basename "${DATABASE}")"

# ---------------------------------------------------------------------------
# Persist settings that `idtrack serve` cannot take directly.
# ---------------------------------------------------------------------------
# See "Settings persistence" in the header comment: backup-*, idle-timeout,
# and the branding options only take effect via ~/.idtrack/defaults.json, so
# they are saved here with `idtrack default` rather than added to
# ExecStart= (which only ever invokes `idtrack serve`).
DEFAULT_ARGS=()
[[ -n "${BACKUP_INTERVAL}" ]] && DEFAULT_ARGS+=(--backup-interval "${BACKUP_INTERVAL}")
[[ -n "${BACKUP_COUNT}"    ]] && DEFAULT_ARGS+=(--backup-count "${BACKUP_COUNT}")
[[ -n "${BACKUP_AGE}"      ]] && DEFAULT_ARGS+=(--backup-age "${BACKUP_AGE}")
[[ -n "${BACKUP_SIZE}"     ]] && DEFAULT_ARGS+=(--backup-size "${BACKUP_SIZE}")
[[ -n "${IDLE_TIMEOUT}"    ]] && DEFAULT_ARGS+=(--idle-timeout "${IDLE_TIMEOUT}")
[[ -n "${APP_NAME}"        ]] && DEFAULT_ARGS+=(--app-name "${APP_NAME}")
[[ -n "${APP_DESC}"        ]] && DEFAULT_ARGS+=(--app-description "${APP_DESC}")

if [[ "${#DEFAULT_ARGS[@]}" -gt 0 ]]; then
    echo "Saving server settings to idtrack defaults..."
    idtrack_default "${DEFAULT_ARGS[@]}"
    echo ""
fi

# ---------------------------------------------------------------------------
# Build the ExecStart command line
# ---------------------------------------------------------------------------
EXEC_START="${BINARY} serve --foreground --database ${DATABASE}"

[[ -n "${PORT}"      ]] && EXEC_START+=" --port ${PORT}"
[[ -n "${CERT_FILE}" ]] && EXEC_START+=" --server-cert ${CERT_FILE} --server-key ${KEY_FILE}"
[[ -n "${INSECURE}"  ]] && EXEC_START+=" --insecure ${INSECURE}"
[[ -n "${BASE_PATH}" ]] && EXEC_START+=" --base-path ${BASE_PATH}"

# ---------------------------------------------------------------------------
# Generate the systemd unit file
# ---------------------------------------------------------------------------
# The [Unit] section describes the service and declares ordering constraints.
# The [Service] section defines how to run and supervise the process.
# The [Install] section tells systemd which target activates this service.
#
# Type=simple
#   The process started by ExecStart IS the service process.  systemd
#   considers the service started as soon as the process is running.
#   idtrack uses --foreground so it stays as the main process (no fork).
#
# Restart=on-failure
#   Restart the service if it exits with a non-zero status or is killed by
#   a signal.  Does not restart on clean exit (exit code 0) or SIGTERM.
#
# RestartSec=5s
#   Wait 5 seconds before each restart attempt to avoid a tight restart
#   loop if idtrack repeatedly fails to start (e.g. port already in use).
#
# WantedBy=multi-user.target
#   The service is enabled when the system reaches the standard multi-user
#   (non-graphical server) runlevel.  This is the conventional target for
#   background network services.

if [[ "${USER_SERVICE}" -eq 1 ]]; then
    # User services don't specify User= / Group= (they always run as the
    # current user) and use default.target instead of multi-user.target.
    UNIT_CONTENT="[Unit]
Description=idtrack — lightweight self-hosted issue tracker
Documentation=https://github.com/tucats/idtrack
After=network.target

[Service]
Type=simple
WorkingDirectory=${DB_DIR}
ExecStart=${EXEC_START}
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target"
else
    UNIT_CONTENT="[Unit]
Description=idtrack — lightweight self-hosted issue tracker
Documentation=https://github.com/tucats/idtrack
After=network.target
Wants=network.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
WorkingDirectory=${DB_DIR}
# HOME must match what the install script used above when running
# idtrack default as this account (see Settings persistence and
# SERVICE_HOME in the header comment) — otherwise the server would resolve
# ~/.idtrack/defaults.json to a different, non-existent path at boot and
# silently see none of the settings just configured.
Environment=HOME=${SERVICE_HOME}
ExecStart=${EXEC_START}
Restart=on-failure
RestartSec=5s
# Harden the service by restricting what the process can do.
# Remove or comment out these lines if they cause problems on older kernels.
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${DB_DIR} ${SERVICE_HOME}
PrivateTmp=true

[Install]
WantedBy=multi-user.target"
fi

# ---------------------------------------------------------------------------
# If the service is already running, stop it before writing the new unit file
# ---------------------------------------------------------------------------
if ${SYSTEMCTL} is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
    echo "Existing service is running — stopping it before updating..."
    ${SYSTEMCTL} stop "${SERVICE_NAME}"
fi

# ---------------------------------------------------------------------------
# Write the unit file
# ---------------------------------------------------------------------------
printf '%s\n' "${UNIT_CONTENT}" > "${UNIT_FILE}"

# ---------------------------------------------------------------------------
# Reload, enable, and start
# ---------------------------------------------------------------------------
# daemon-reload tells systemd to re-read all unit files from disk.
# This must be run after adding or changing a unit file.
${SYSTEMCTL} daemon-reload

# enable creates the symlink that makes the service start automatically
# (at boot for system services, at login for user services).
${SYSTEMCTL} enable "${SERVICE_NAME}"

# start runs the service right now without waiting for the next boot.
${SYSTEMCTL} start "${SERVICE_NAME}"

# ---------------------------------------------------------------------------
# Print summary and management instructions
# ---------------------------------------------------------------------------
echo ""
echo "idtrack service installed and started."
echo ""
echo "  Unit file    : ${UNIT_FILE}"
echo "  Binary       : ${BINARY}"
echo "  Database     : ${DATABASE}"
if [[ -n "${CERT_FILE}" ]]; then
echo "  TLS cert     : ${CERT_FILE}"
echo "  TLS key      : ${KEY_FILE}"
else
echo "  TLS cert     : built-in self-signed"
fi
if [[ -n "${INSECURE}" ]]; then
echo "  Insecure     : ${INSECURE}"
fi
if [[ -n "${BASE_PATH}" ]]; then
echo "  Base path    : ${BASE_PATH}"
fi
if [[ "${#DEFAULT_ARGS[@]}" -gt 0 ]]; then
echo "  Other settings saved to idtrack defaults (backup/idle-timeout/branding)"
fi
if [[ "${USER_SERVICE}" -eq 1 ]]; then
echo "  Mode         : user service (runs as ${USER})"
echo ""
echo "  NOTE: To keep the service running when you are not logged in:"
echo "    loginctl enable-linger ${USER}"
else
echo "  Mode         : system service (runs as ${SERVICE_USER}, starts at boot)"
echo "  Defaults home: ${SERVICE_HOME} (idtrack default / defaults.json for ${SERVICE_USER})"
fi
echo ""
echo "Managing the service:"
echo ""
echo "  Status       : ${SYSTEMCTL} status  ${SERVICE_NAME}"
echo "  Stop         : ${SYSTEMCTL} stop    ${SERVICE_NAME}"
echo "  Start        : ${SYSTEMCTL} start   ${SERVICE_NAME}"
echo "  Restart      : ${SYSTEMCTL} restart ${SERVICE_NAME}"
echo "  View logs    : journalctl -u ${SERVICE_NAME} -f"
echo ""
echo "  Uninstall    :"
if [[ "${USER_SERVICE}" -eq 1 ]]; then
echo "    ./tools/install-service-linux.sh --uninstall --user-service"
else
echo "    sudo ./tools/install-service-linux.sh --uninstall"
fi
