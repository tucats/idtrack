#!/usr/bin/env bash
# =============================================================================
# build-container.sh — Build the idtrack Docker image.
# =============================================================================
#
# This script wraps 'docker build' so that:
#
#   • The version string (from tools/buildver.txt) and build timestamp are
#     injected into the binary via --build-arg, matching the behaviour of
#     the native 'tools/build' script.
#
#   • The image is tagged with both the version number and 'latest' so you
#     can pin containers to a specific release or always pull the newest one.
#
# Usage:
#   ./tools/build-container.sh [options]
#
# Options:
#   --name IMAGE_NAME   Base name for the image tags.
#                       Default: idtrack
#                       Example: --name ghcr.io/myorg/idtrack
#
#   --no-cache          Pass --no-cache to 'docker build', forcing every
#                       layer to be rebuilt from scratch.  Useful when a
#                       dependency update should not use a cached layer.
#
#   --platform SPEC     Target platform(s) for the image, e.g.:
#                         linux/amd64          (Intel/AMD servers)
#                         linux/arm64          (Apple Silicon, AWS Graviton)
#                         linux/amd64,linux/arm64  (multi-arch manifest)
#                       Requires Docker Buildx for multi-arch builds.
#                       Default: the host's native platform.
#
#   --push              Push both tags to a registry after building.
#                       The image name must include the registry host, e.g.
#                       ghcr.io/myorg/idtrack.  You must be logged in first:
#                         docker login ghcr.io
#
#   --help, -h          Show this help and exit.
#
# Examples:
#   # Build for the host platform and tag as 'idtrack:1.0-34' and 'idtrack:latest'
#   ./tools/build-container.sh
#
#   # Force a clean build with a custom image name
#   ./tools/build-container.sh --name myregistry.local/idtrack --no-cache
#
#   # Build for Linux arm64 (e.g. to deploy to a Raspberry Pi or Graviton host)
#   ./tools/build-container.sh --platform linux/arm64
#
#   # Build multi-arch and push to GitHub Container Registry
#   ./tools/build-container.sh \
#     --name ghcr.io/myorg/idtrack \
#     --platform linux/amd64,linux/arm64 \
#     --push
# =============================================================================

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
IMAGE_NAME="idtrack"
NO_CACHE=""
PLATFORM_ARG=""
PUSH=0

# Resolve paths relative to the script's location so the script can be run
# from any working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)
            IMAGE_NAME="$2"
            shift 2
            ;;
        --no-cache)
            NO_CACHE="--no-cache"
            shift
            ;;
        --platform)
            PLATFORM_ARG="$2"
            shift 2
            ;;
        --push)
            PUSH=1
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
# Read the version string from tools/buildver.txt.
# Fall back to "dev" if the file is absent (e.g. a sparse checkout).
# ---------------------------------------------------------------------------
VERSION_FILE="${REPO_ROOT}/tools/buildver.txt"
if [[ -f "${VERSION_FILE}" ]]; then
    # Strip any trailing whitespace or newline.
    VERSION=$(tr -d '[:space:]' < "${VERSION_FILE}")
else
    VERSION="dev"
    echo "warning: ${VERSION_FILE} not found; using version 'dev'" >&2
fi

# Build timestamp in the YYYYMMDDHHmmSS format that 'idtrack version' expects.
BUILD_TIME=$(date -u +%Y%m%d%H%M%S)

# ---------------------------------------------------------------------------
# Assemble the docker build command.
# We build the argument list as a Bash array so that spaces in values are
# handled correctly without any quoting gymnastics.
# ---------------------------------------------------------------------------
BUILD_ARGS=(
    "build"
    "--build-arg" "BUILD_VERSION=${VERSION}"
    "--build-arg" "BUILD_TIME=${BUILD_TIME}"
    "--tag"       "${IMAGE_NAME}:${VERSION}"
    "--tag"       "${IMAGE_NAME}:latest"
)

# --no-cache is an empty string when not requested; only append it when set.
[[ -n "${NO_CACHE}" ]] && BUILD_ARGS+=("${NO_CACHE}")

# --platform is optional; multi-arch builds require Docker Buildx.
if [[ -n "${PLATFORM_ARG}" ]]; then
    BUILD_ARGS+=("--platform" "${PLATFORM_ARG}")
fi

# The build context is the repository root (where the Dockerfile lives).
BUILD_ARGS+=("${REPO_ROOT}")

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------
echo "Building ${IMAGE_NAME}:${VERSION}  (build time: ${BUILD_TIME})"
echo ""

docker "${BUILD_ARGS[@]}"

echo ""
echo "Image built successfully:"
echo "  ${IMAGE_NAME}:${VERSION}"
echo "  ${IMAGE_NAME}:latest"

# ---------------------------------------------------------------------------
# Push (optional)
# ---------------------------------------------------------------------------
if [[ "${PUSH}" -eq 1 ]]; then
    echo ""
    echo "Pushing to registry..."
    docker push "${IMAGE_NAME}:${VERSION}"
    docker push "${IMAGE_NAME}:latest"
    echo "Push complete."
fi
