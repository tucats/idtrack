#!/usr/bin/env bash
# =============================================================================
# install-service-macos.sh — Install or remove idtrack as a launchd service
#                             on macOS.
# =============================================================================
#
# idtrack is installed as either a LaunchAgent or a LaunchDaemon:
#
#   LaunchAgent (default)
#     Lives in ~/Library/LaunchAgents/.  Runs as the current user.  Started
#     automatically when the user logs in and stopped when they log out.
#     Suitable for personal use on a developer's Mac.  No sudo required.
#
#   LaunchDaemon (--system)
#     Lives in /Library/LaunchDaemons/.  Runs as a specified user (see
#     --run-as).  Started at boot before any user logs in.  Suitable for a
#     Mac that serves as a shared team server.  Requires sudo.
#
# Usage:
#   ./tools/install-service-macos.sh [options]
#   ./tools/install-service-macos.sh --uninstall [--system]
#
# Options:
#   --binary PATH       Path to the idtrack binary.
#                       Default: the first 'idtrack' found in PATH, then
#                       ./idtrack in the current directory.
#
#   --database PATH     Path to the SQLite database file.
#                       Default (agent):  $HOME/.idtrack/idtrack.db
#                       Default (daemon): /var/lib/idtrack/idtrack.db
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
#
#   --idle-timeout DURATION      Idle logout timer. Default: off.
#   --app-name TEXT              Custom application name.
#   --app-description TEXT       Custom tagline.
#
#   --log PATH          Path to the log file.
#                       Default (agent):  $HOME/.idtrack/idtrack.log
#                       Default (daemon): /var/log/idtrack/idtrack.log
#
#   --label LABEL       launchd service label (reverse-domain notation).
#                       Default: com.github.tucats.idtrack
#
#   --system            Install as a LaunchDaemon rather than a LaunchAgent.
#                       Requires sudo.
#
#   --run-as USERNAME   User the daemon runs as (--system only).
#                       Default: the current user.
#
#   --uninstall         Stop and remove the service instead of installing it.
#
#   --help, -h          Show this help and exit.
#
# Examples:
#   # Install as a LaunchAgent (personal use, runs at login)
#   ./tools/install-service-macos.sh --database ~/.idtrack/idtrack.db
#
#   # Install as a LaunchDaemon (shared server, runs at boot)
#   sudo ./tools/install-service-macos.sh \
#     --system --run-as tom \
#     --database /var/lib/idtrack/idtrack.db \
#     --cert /etc/ssl/certs/idtrack.crt \
#     --key  /etc/ssl/private/idtrack.key
#
#   # Remove the service
#   ./tools/install-service-macos.sh --uninstall
#   sudo ./tools/install-service-macos.sh --uninstall --system
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
LABEL="com.github.tucats.idtrack"
BINARY=""
DATABASE=""
PORT=""
CERT_FILE=""
KEY_FILE=""
BACKUP_INTERVAL=""
BACKUP_COUNT=""
BACKUP_AGE=""
IDLE_TIMEOUT=""
APP_NAME=""
APP_DESC=""
LOG_PATH=""
SYSTEM_MODE=0
RUN_AS="${USER}"
UNINSTALL=0

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
        --idle-timeout)    IDLE_TIMEOUT="$2";    shift 2 ;;
        --app-name)        APP_NAME="$2";        shift 2 ;;
        --app-description) APP_DESC="$2";        shift 2 ;;
        --log)          LOG_PATH="$2";        shift 2 ;;
        --label)        LABEL="$2";           shift 2 ;;
        --system)       SYSTEM_MODE=1;        shift ;;
        --run-as)       RUN_AS="$2";          shift 2 ;;
        --uninstall)    UNINSTALL=1;          shift ;;
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
# Determine plist destination and launchd domain based on mode
# ---------------------------------------------------------------------------
if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
    PLIST_DIR="/Library/LaunchDaemons"
    LAUNCHD_DOMAIN="system"
else
    PLIST_DIR="${HOME}/Library/LaunchAgents"
    LAUNCHD_DOMAIN="gui/$(id -u)"
fi

PLIST_PATH="${PLIST_DIR}/${LABEL}.plist"

# ---------------------------------------------------------------------------
# Uninstall path
# ---------------------------------------------------------------------------
if [[ "${UNINSTALL}" -eq 1 ]]; then
    if [[ ! -f "${PLIST_PATH}" ]]; then
        echo "Service plist not found: ${PLIST_PATH}"
        echo "Nothing to remove."
        exit 0
    fi

    echo "Stopping and removing idtrack service (${LABEL})..."

    # bootout stops and unloads the service.  Errors here are non-fatal
    # because the service may already be stopped.
    launchctl bootout "${LAUNCHD_DOMAIN}/${LABEL}" 2>/dev/null || true

    rm -f "${PLIST_PATH}"

    echo "Service removed.  The plist has been deleted:"
    echo "  ${PLIST_PATH}"
    echo ""
    echo "Your database and log files have not been touched."
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

# Resolve to absolute path.
BINARY="$(cd "$(dirname "${BINARY}")" && pwd)/$(basename "${BINARY}")"

if [[ ! -x "${BINARY}" ]]; then
    echo "error: binary not executable: ${BINARY}" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Apply defaults that depend on mode
# ---------------------------------------------------------------------------
if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
    : "${DATABASE:=/var/lib/idtrack/idtrack.db}"
    : "${LOG_PATH:=/var/log/idtrack/idtrack.log}"
else
    : "${DATABASE:=${HOME}/.idtrack/idtrack.db}"
    : "${LOG_PATH:=${HOME}/.idtrack/idtrack.log}"
fi

# Resolve database to absolute path (the directory must already exist or
# we create it below).
DB_DIR="$(dirname "${DATABASE}")"
DB_FILE="$(basename "${DATABASE}")"

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
# Create required directories and resolve all paths to absolute
# ---------------------------------------------------------------------------
mkdir -p "${DB_DIR}"
mkdir -p "$(dirname "${LOG_PATH}")"

# Resolve DATABASE and LOG_PATH to absolute paths now that their directories
# exist.  launchd services may run with a working directory of / (daemons) or
# the user's home (agents), so a relative path in the plist would silently
# point at the wrong location.
DATABASE="$(cd "${DB_DIR}" && pwd)/${DB_FILE}"
LOG_PATH="$(cd "$(dirname "${LOG_PATH}")" && pwd)/$(basename "${LOG_PATH}")"

if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
    # Ensure the directories are owned by the user the daemon runs as
    # so idtrack can read and write the database and log.
    chown "${RUN_AS}" "${DB_DIR}" "$(dirname "${LOG_PATH}")" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Build the ProgramArguments array for the plist.
# Each argument becomes its own <string> element.
# ---------------------------------------------------------------------------

# Start with the fixed arguments every invocation needs.
PROG_ARGS="        <string>${BINARY}</string>
        <string>serve</string>
        <string>--foreground</string>
        <string>--database</string>
        <string>${DATABASE}</string>"

# Append optional arguments only when the caller supplied a value.
[[ -n "${PORT}"            ]] && PROG_ARGS+="
        <string>--port</string>
        <string>${PORT}</string>"

[[ -n "${CERT_FILE}"       ]] && PROG_ARGS+="
        <string>--server-cert</string>
        <string>${CERT_FILE}</string>
        <string>--server-key</string>
        <string>${KEY_FILE}</string>"

[[ -n "${BACKUP_INTERVAL}" ]] && PROG_ARGS+="
        <string>--backup-interval</string>
        <string>${BACKUP_INTERVAL}</string>"

[[ -n "${BACKUP_COUNT}"    ]] && PROG_ARGS+="
        <string>--backup-count</string>
        <string>${BACKUP_COUNT}</string>"

[[ -n "${BACKUP_AGE}"      ]] && PROG_ARGS+="
        <string>--backup-age</string>
        <string>${BACKUP_AGE}</string>"

[[ -n "${IDLE_TIMEOUT}"    ]] && PROG_ARGS+="
        <string>--idle-timeout</string>
        <string>${IDLE_TIMEOUT}</string>"

[[ -n "${APP_NAME}"        ]] && PROG_ARGS+="
        <string>--app-name</string>
        <string>${APP_NAME}</string>"

[[ -n "${APP_DESC}"        ]] && PROG_ARGS+="
        <string>--app-description</string>
        <string>${APP_DESC}</string>"

# ---------------------------------------------------------------------------
# Generate the plist
# ---------------------------------------------------------------------------
# launchd reads this XML file to know how to start, stop, and supervise the
# service.  Key fields:
#
#   Label           — unique identifier for the service within launchd
#   ProgramArguments — the command to run (first element is the binary path)
#   RunAtLoad       — start the service as soon as the plist is loaded
#   KeepAlive       — restart the process automatically if it exits
#   StandardOutPath — where stdout is written (idtrack logs go here)
#   StandardErrorPath — where stderr is written (same file as stdout)
#   WorkingDirectory — the process's initial working directory
#   UserName        — (LaunchDaemon only) which user to run the process as

PLIST_CONTENT="<?xml version=\"1.0\" encoding=\"UTF-8\"?>
<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\"
  \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">
<plist version=\"1.0\">
<dict>

    <!-- Unique label that identifies this service to launchd. -->
    <key>Label</key>
    <string>${LABEL}</string>

    <!-- The command and its arguments.  idtrack must run with --foreground
         so that the server process stays alive and launchd can manage it.
         Without --foreground, idtrack re-execs itself as a background child
         and exits, which causes launchd to conclude the service failed and
         immediately try to restart it in a tight loop. -->
    <key>ProgramArguments</key>
    <array>
${PROG_ARGS}
    </array>

    <!-- Start the service immediately when the plist is loaded (at login
         for LaunchAgents, at boot for LaunchDaemons). -->
    <key>RunAtLoad</key>
    <true/>

    <!-- Restart the server automatically if it exits for any reason.
         Combined with RunAtLoad this makes the service self-healing. -->
    <key>KeepAlive</key>
    <true/>

    <!-- Capture the server's log output.  Both stdout and stderr are
         written to the same file for simplicity. -->
    <key>StandardOutPath</key>
    <string>${LOG_PATH}</string>
    <key>StandardErrorPath</key>
    <string>${LOG_PATH}</string>"

# LaunchDaemon: add UserName so the daemon doesn't run as root.
if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
    PLIST_CONTENT+="

    <!-- Run as this user rather than root. -->
    <key>UserName</key>
    <string>${RUN_AS}</string>"
fi

PLIST_CONTENT+="

</dict>
</plist>"

# ---------------------------------------------------------------------------
# If the service is already loaded, unload it before writing the new plist.
# This allows the script to be re-run to update an existing installation.
# ---------------------------------------------------------------------------
if launchctl list "${LABEL}" &>/dev/null; then
    echo "Existing service found — stopping it before updating..."
    launchctl bootout "${LAUNCHD_DOMAIN}/${LABEL}" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Write the plist
# ---------------------------------------------------------------------------
mkdir -p "${PLIST_DIR}"
printf '%s\n' "${PLIST_CONTENT}" > "${PLIST_PATH}"

# LaunchDaemons must be owned by root and not writable by group/other,
# otherwise launchd refuses to load them.
if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
    chown root:wheel "${PLIST_PATH}"
    chmod 644 "${PLIST_PATH}"
fi

# ---------------------------------------------------------------------------
# Load (bootstrap) the service
# ---------------------------------------------------------------------------
# launchctl bootstrap registers the plist with launchd and, because
# RunAtLoad is true, starts the service immediately.
launchctl bootstrap "${LAUNCHD_DOMAIN}" "${PLIST_PATH}"

# ---------------------------------------------------------------------------
# Print summary and management instructions
# ---------------------------------------------------------------------------
echo ""
echo "idtrack service installed and started."
echo ""
echo "  Plist        : ${PLIST_PATH}"
echo "  Binary       : ${BINARY}"
echo "  Database     : ${DATABASE}"
echo "  Log          : ${LOG_PATH}"
if [[ -n "${CERT_FILE}" ]]; then
echo "  TLS cert     : ${CERT_FILE}"
echo "  TLS key      : ${KEY_FILE}"
else
echo "  TLS cert     : built-in self-signed"
fi
if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
echo "  Mode         : LaunchDaemon (starts at boot, runs as ${RUN_AS})"
else
echo "  Mode         : LaunchAgent  (starts at login)"
fi
echo ""
echo "Managing the service:"
echo ""
echo "  Check status :"
echo "    launchctl list ${LABEL}"
echo ""
echo "  Stop         :"
echo "    launchctl kill SIGTERM ${LAUNCHD_DOMAIN}/${LABEL}"
echo ""
echo "  Start        :"
echo "    launchctl kickstart ${LAUNCHD_DOMAIN}/${LABEL}"
echo ""
echo "  View logs    :"
echo "    tail -f ${LOG_PATH}"
echo ""
echo "  Uninstall    :"
if [[ "${SYSTEM_MODE}" -eq 1 ]]; then
echo "    sudo ./tools/install-service-macos.sh --uninstall --system"
else
echo "    ./tools/install-service-macos.sh --uninstall"
fi
