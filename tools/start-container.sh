#!/usr/bin/env bash
# =============================================================================
# start-container.sh — Start the idtrack container.
# =============================================================================
#
# This script constructs and runs the 'docker run' command needed to start
# idtrack with the correct bind mounts, port mapping, and server options.
# It is the recommended way to launch the container in production.
#
# The only required argument is --data, which tells the script where on the
# host filesystem the SQLite database and backup files should live.
#
# Usage:
#   ./tools/start-container.sh --data DIR [options]
#
# Required:
#   --data DIR          Host directory to mount at /data inside the container.
#                       The SQLite database (idtrack.db) and backup directory
#                       (idtrack-backups/) will be created here on first run.
#                       The directory must already exist.
#                       Example: --data /var/lib/idtrack
#
# Connection:
#   --port PORT         Host port to map to the container's HTTPS port 8443.
#                       Default: 8443
#                       Example: --port 443
#
# Container identity:
#   --name NAME         Name assigned to the running container.
#                       Default: idtrack
#   --image IMAGE       Image name and optional tag to run.
#                       Default: idtrack:latest
#   --restart POLICY    Docker restart policy applied to the container:
#                         no              Never restart automatically.
#                         on-failure      Restart only on non-zero exit.
#                         always          Always restart (even after 'docker stop').
#                         unless-stopped  Restart unless explicitly stopped.
#                       Default: unless-stopped
#
# TLS certificate (optional):
#   By default, the server uses the built-in self-signed certificate.
#   To use a certificate issued by a trusted authority (recommended for any
#   deployment accessible beyond localhost), provide both flags together:
#
#   --cert PATH         Host path to a PEM-encoded TLS certificate file.
#                       The file is mounted read-only inside the container.
#                       Example: --cert /etc/ssl/certs/idtrack.crt
#   --key  PATH         Host path to the matching PEM-encoded private key.
#                       The file is mounted read-only inside the container.
#                       Example: --key /etc/ssl/private/idtrack.key
#
#   Both --cert and --key must be supplied together; specifying only one is
#   an error.
#
# Backup settings (optional — see 'idtrack default' in the manual):
#   --backup-interval DURATION   How often to write a backup, e.g. 1h, 30m.
#                                Use 0 or off to disable (default: off).
#   --backup-count N             Maximum number of backups to keep.
#                                Use 0 or off for no count limit (default: off).
#   --backup-age DURATION        Delete backups older than this, e.g. 168h.
#                                Use 0 or off for no age limit (default: off).
#
# Application branding (optional):
#   --idle-timeout DURATION      Idle logout timer, e.g. 30m, 1h.
#                                Use 0 or off to disable (default: off).
#   --app-name TEXT              Custom application name shown in the header.
#   --app-description TEXT       Custom tagline on the login screen.
#
# Run mode:
#   --foreground        Run the container in the foreground (docker run without
#                       -d).  The container's log output is printed directly to
#                       the terminal and the shell blocks until the container
#                       stops.  Useful for debugging or first-run verification.
#                       Default: run detached (in the background).
#
#   --help, -h          Show this help and exit.
#
# Examples:
#   # Minimal: database in /var/lib/idtrack, default port 8443
#   ./tools/start-container.sh --data /var/lib/idtrack
#
#   # Custom host port and container name
#   ./tools/start-container.sh \
#     --data /var/lib/idtrack \
#     --port 443 \
#     --name my-idtrack
#
#   # External TLS certificate
#   ./tools/start-container.sh \
#     --data /var/lib/idtrack \
#     --cert /etc/ssl/certs/tracker.example.com.crt \
#     --key  /etc/ssl/private/tracker.example.com.key
#
#   # Hourly backups, keep last 48, discard backups older than 7 days
#   ./tools/start-container.sh \
#     --data /var/lib/idtrack \
#     --backup-interval 1h \
#     --backup-count 48 \
#     --backup-age 168h
#
#   # First-run: run in foreground to watch the startup log
#   ./tools/start-container.sh --data /var/lib/idtrack --foreground
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
DATA_DIR=""
HOST_PORT=8443
CONTAINER_NAME="idtrack"
IMAGE="idtrack:latest"
RESTART_POLICY="unless-stopped"
CERT_FILE=""
KEY_FILE=""
BACKUP_INTERVAL=""
BACKUP_COUNT=""
BACKUP_AGE=""
IDLE_TIMEOUT=""
APP_NAME=""
APP_DESC=""
DETACH="-d"   # default: detached; cleared by --foreground

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --data)
            DATA_DIR="$2"
            shift 2
            ;;
        --port)
            HOST_PORT="$2"
            shift 2
            ;;
        --name)
            CONTAINER_NAME="$2"
            shift 2
            ;;
        --image)
            IMAGE="$2"
            shift 2
            ;;
        --restart)
            RESTART_POLICY="$2"
            shift 2
            ;;
        --cert)
            CERT_FILE="$2"
            shift 2
            ;;
        --key)
            KEY_FILE="$2"
            shift 2
            ;;
        --backup-interval)
            BACKUP_INTERVAL="$2"
            shift 2
            ;;
        --backup-count)
            BACKUP_COUNT="$2"
            shift 2
            ;;
        --backup-age)
            BACKUP_AGE="$2"
            shift 2
            ;;
        --idle-timeout)
            IDLE_TIMEOUT="$2"
            shift 2
            ;;
        --app-name)
            APP_NAME="$2"
            shift 2
            ;;
        --app-description)
            APP_DESC="$2"
            shift 2
            ;;
        --foreground)
            DETACH=""
            shift
            ;;
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
# Validate required arguments
# ---------------------------------------------------------------------------
if [[ -z "${DATA_DIR}" ]]; then
    echo "error: --data DIR is required" >&2
    echo "Run '$0 --help' for usage." >&2
    exit 1
fi

# Resolve the data directory to an absolute path and verify it exists.
# SQLite requires a real on-disk path; a relative path that drifts with the
# caller's working directory can cause 'database not found' errors.
DATA_DIR="$(cd "${DATA_DIR}" 2>/dev/null && pwd)" || {
    echo "error: data directory does not exist: ${DATA_DIR}" >&2
    exit 1
}

# Both --cert and --key must be provided together; one without the other
# would make the server fail to start with a mismatched credential error.
if [[ -n "${CERT_FILE}" && -z "${KEY_FILE}" ]]; then
    echo "error: --cert requires --key to also be specified" >&2
    exit 1
fi
if [[ -n "${KEY_FILE}" && -z "${CERT_FILE}" ]]; then
    echo "error: --key requires --cert to also be specified" >&2
    exit 1
fi

# Resolve cert/key to absolute paths and verify the files exist.
if [[ -n "${CERT_FILE}" ]]; then
    CERT_FILE="$(cd "$(dirname "${CERT_FILE}")" && pwd)/$(basename "${CERT_FILE}")" || {
        echo "error: cert file not found: ${CERT_FILE}" >&2
        exit 1
    }
    [[ -f "${CERT_FILE}" ]] || { echo "error: cert file not found: ${CERT_FILE}" >&2; exit 1; }
fi
if [[ -n "${KEY_FILE}" ]]; then
    KEY_FILE="$(cd "$(dirname "${KEY_FILE}")" && pwd)/$(basename "${KEY_FILE}")" || {
        echo "error: key file not found: ${KEY_FILE}" >&2
        exit 1
    }
    [[ -f "${KEY_FILE}" ]] || { echo "error: key file not found: ${KEY_FILE}" >&2; exit 1; }
fi

# ---------------------------------------------------------------------------
# Check for an existing container with the same name.
# Offer to remove it so the user isn't left with a confusing error from
# 'docker run' about a name conflict.
# ---------------------------------------------------------------------------
if docker inspect --format '{{.State.Status}}' "${CONTAINER_NAME}" &>/dev/null; then
    STATUS=$(docker inspect --format '{{.State.Status}}' "${CONTAINER_NAME}")
    echo "A container named '${CONTAINER_NAME}' already exists (status: ${STATUS})."
    echo ""
    echo "To remove it and start fresh:"
    echo "  docker rm -f ${CONTAINER_NAME}"
    echo ""
    echo "To view its logs:"
    echo "  docker logs ${CONTAINER_NAME}"
    exit 1
fi

# ---------------------------------------------------------------------------
# Assemble the docker run argument list.
# Using a Bash array avoids quoting problems — each element is passed as a
# separate argument to docker, so values containing spaces (e.g. app names)
# are handled correctly.
# ---------------------------------------------------------------------------

# docker run flags
DOCKER_ARGS=(
    "run"
    "--name"    "${CONTAINER_NAME}"
    "--restart" "${RESTART_POLICY}"
    "-p"        "${HOST_PORT}:8443"
    "-v"        "${DATA_DIR}:/data"
)

[[ -n "${DETACH}" ]] && DOCKER_ARGS+=("${DETACH}")

# Mount the TLS certificate files read-only.  They are placed at predictable
# paths inside the container (/certs/) so the server flags can reference them.
if [[ -n "${CERT_FILE}" ]]; then
    DOCKER_ARGS+=(
        "-v" "${CERT_FILE}:/certs/server.crt:ro"
        "-v" "${KEY_FILE}:/certs/server.key:ro"
    )
fi

DOCKER_ARGS+=("${IMAGE}")

# idtrack serve command and flags
SERVE_CMD=(
    "idtrack" "serve"
    "--foreground"
    "--database" "/data/idtrack.db"
)

# TLS: point the server at the mounted cert/key files.
if [[ -n "${CERT_FILE}" ]]; then
    SERVE_CMD+=("--server-cert" "/certs/server.crt")
    SERVE_CMD+=("--server-key"  "/certs/server.key")
fi

# Backup settings: only pass flags when the caller supplied a value.
[[ -n "${BACKUP_INTERVAL}" ]] && SERVE_CMD+=("--backup-interval" "${BACKUP_INTERVAL}")
[[ -n "${BACKUP_COUNT}"    ]] && SERVE_CMD+=("--backup-count"    "${BACKUP_COUNT}")
[[ -n "${BACKUP_AGE}"      ]] && SERVE_CMD+=("--backup-age"      "${BACKUP_AGE}")

# Application settings
[[ -n "${IDLE_TIMEOUT}" ]] && SERVE_CMD+=("--idle-timeout"    "${IDLE_TIMEOUT}")
[[ -n "${APP_NAME}"     ]] && SERVE_CMD+=("--app-name"        "${APP_NAME}")
[[ -n "${APP_DESC}"     ]] && SERVE_CMD+=("--app-description" "${APP_DESC}")

# ---------------------------------------------------------------------------
# Print what we are about to run so the operator can verify the configuration
# before anything is started.
# ---------------------------------------------------------------------------
echo "Starting idtrack container"
echo ""
echo "  Container name : ${CONTAINER_NAME}"
echo "  Image          : ${IMAGE}"
echo "  Host port      : ${HOST_PORT} → container 8443"
echo "  Data directory : ${DATA_DIR} → /data"

if [[ -n "${CERT_FILE}" ]]; then
    echo "  TLS cert       : ${CERT_FILE}"
    echo "  TLS key        : ${KEY_FILE}"
else
    echo "  TLS cert       : built-in self-signed"
fi

[[ -n "${BACKUP_INTERVAL}" ]] && echo "  Backup interval: ${BACKUP_INTERVAL}"
[[ -n "${BACKUP_COUNT}"    ]] && echo "  Backup count   : ${BACKUP_COUNT}"
[[ -n "${BACKUP_AGE}"      ]] && echo "  Backup age     : ${BACKUP_AGE}"
[[ -n "${IDLE_TIMEOUT}"    ]] && echo "  Idle timeout   : ${IDLE_TIMEOUT}"
[[ -n "${APP_NAME}"        ]] && echo "  App name       : ${APP_NAME}"
[[ -n "${APP_DESC}"        ]] && echo "  App description: ${APP_DESC}"

if [[ -z "${DETACH}" ]]; then
    echo "  Run mode       : foreground (Ctrl-C to stop)"
else
    echo "  Restart policy : ${RESTART_POLICY}"
fi

echo ""

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------
docker "${DOCKER_ARGS[@]}" "${SERVE_CMD[@]}"

# The section below only executes when running detached (background).
if [[ -n "${DETACH}" ]]; then
    echo "Container started."
    echo ""
    echo "  View logs  : docker logs -f ${CONTAINER_NAME}"
    echo "  Stop       : docker stop ${CONTAINER_NAME}"
    echo "  Remove     : docker rm ${CONTAINER_NAME}"
    echo "  Open       : https://localhost:${HOST_PORT}"
fi
