# syntax=docker/dockerfile:1
# =============================================================================
# Dockerfile — idtrack self-hosted issue tracker
# =============================================================================
#
# Two-stage build:
#   Stage 1  (builder) — compiles the Go binary inside the official Go image.
#   Stage 2  (runtime) — copies only the binary into a minimal Alpine image,
#                        discarding the entire Go toolchain and source tree.
#
# The SQLite database and backup files live outside the container on a
# host-mounted directory (see VOLUME /data below).  All server options —
# port, TLS certificate, backup schedule, branding — are passed as
# command-line flags at 'docker run' time rather than baked into the image,
# so the same image can serve many different configurations.
#
# Quick start (after building with tools/build-container.sh):
#
#   docker run -d \
#     --name idtrack \
#     -p 8443:8443 \
#     -v /path/to/your/data:/data \
#     idtrack:latest
#
# See tools/start-container.sh for a full-featured wrapper that handles
# TLS certificates, backup settings, and other options.
# =============================================================================


# =============================================================================
# Stage 1 — builder
# =============================================================================
FROM golang:1.25-alpine AS builder

# git       — required by 'go mod download' when any dependency is resolved
#             from a VCS URL rather than a module proxy.
# ca-certificates — required for HTTPS connections to the module proxy during
#             the build (proxy.golang.org uses TLS).
RUN apk add --no-cache git ca-certificates

WORKDIR /build

# Copy the module manifests before the source so Docker can cache the
# module-download layer independently.  Re-downloading modules only happens
# when go.mod or go.sum changes — not on every source edit.
COPY go.mod go.sum ./
RUN go mod download

# Copy the full source tree.  The .dockerignore file (in the repo root)
# excludes build artifacts, local databases, and other files that should
# not influence the image.
COPY . .

# Build-time arguments let the caller inject the version string and UTC
# timestamp so 'idtrack version' inside the container reports the same
# value as the image tag.  They default to "dev" / "" when the image is
# built without passing --build-arg (e.g. a plain 'docker build .').
ARG BUILD_VERSION=dev
ARG BUILD_TIME=""

# Build the binary.  Key flags:
#
#   CGO_ENABLED=0
#     idtrack uses modernc.org/sqlite, a pure-Go port of SQLite that
#     requires no C compiler or runtime.  Disabling CGO produces a fully
#     static binary that runs in any Linux container, including Alpine and
#     even 'scratch', without needing libc.
#
#   -trimpath
#     Strip local filesystem paths from stack traces.  Without this, error
#     output would embed the builder's directory structure (e.g.
#     /home/runner/go/src/...) which is both a privacy concern and noise.
#
#   -ldflags "-s -w ..."
#     -s  omit the symbol table
#     -w  omit DWARF debug information
#     Together these reduce binary size by roughly 30% with no effect on
#     runtime behaviour.  Debug info can still be obtained from the source.
#
#   -X main.BuildVersion / -X main.BuildTime
#     Inject the version string and timestamp at link time into the
#     package-level variables that 'idtrack version' prints.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w \
                -X main.BuildVersion=${BUILD_VERSION} \
                -X main.BuildTime=${BUILD_TIME}" \
      -o idtrack .


# =============================================================================
# Stage 2 — runtime image
# =============================================================================
FROM alpine:latest

# Install only what the running container needs:
#
#   ca-certificates — provides the system CA bundle.  The idtrack server
#     does not make outbound HTTPS connections today, but the bundle is
#     useful for health-check tooling (wget / curl) and makes the image
#     forward-compatible if that changes.
#
# Then create a dedicated non-root user and the /data directory in a single
# RUN layer to keep the image history small.
#
#   addgroup / adduser -S  — creates a system (no-login) account.  Running
#     as non-root follows the principle of least privilege: a container
#     escape or exploit cannot write to system paths if the process lacks
#     the necessary permissions.
#
#   /data  — the mount point for host-supplied persistent storage.  The
#     server writes the SQLite database and backup files here.  Giving
#     ownership to the idtrack user allows the server to create and write
#     these files without running as root.
RUN apk add --no-cache ca-certificates \
 && addgroup -S idtrack \
 && adduser  -S idtrack -G idtrack \
 && mkdir -p /data \
 && chown idtrack:idtrack /data

# Copy only the compiled binary from the builder stage.  Everything else
# (Go toolchain, module cache, source code) is discarded here — that is the
# entire point of the multi-stage build.
COPY --from=builder /build/idtrack /usr/local/bin/idtrack

# Switch to the non-root idtrack user for all subsequent instructions and
# at container runtime.  The USER directive applies to both RUN and CMD.
USER idtrack

# EXPOSE documents the port the server listens on but does NOT publish it.
# Use '-p HOST_PORT:8443' on 'docker run' (or 'ports:' in a Compose file)
# to make it reachable from outside the container.
EXPOSE 8443

# /data is the canonical mount point for host persistent storage.
#
# VOLUME declares /data as an externally managed directory.  When a host
# path is bind-mounted here (via '-v /host/path:/data'), the SQLite
# database file and the idtrack-backups/ directory survive container
# restarts, upgrades, and removals — they live on the host, not inside
# the container's writable layer.
#
# If you start the container without mounting /data, Docker creates an
# anonymous volume for it.  The data will persist across container restarts
# but will be lost when the container is removed with 'docker rm'.  Always
# use an explicit bind mount for production use.
VOLUME /data

# Default command.
#
# 'idtrack serve' without --foreground re-execs itself as a detached
# background child process and then exits immediately.  When run inside a
# container, that exit terminates the container because its main process
# (PID 1) has ended — the background child is orphaned and also killed.
#
# '--foreground' bypasses the re-exec mechanism and runs the HTTP server
# loop directly in the current process, blocking until the server stops.
# This is the correct pattern for containerised services: Docker (and the
# container runtime) manages the process lifecycle from outside; the process
# itself must stay in the foreground.
#
# The database path is pinned to /data/idtrack.db.  Override individual
# settings by appending flags to 'docker run', for example:
#
#   docker run ... idtrack:latest \
#     idtrack serve --foreground --database /data/mydb.db --port 9000
#
# The tools/start-container.sh script handles this construction for you.
CMD ["idtrack", "serve", "--foreground", "--database", "/data/idtrack.db"]
