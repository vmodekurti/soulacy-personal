# syntax=docker/dockerfile:1.6
#
# Dockerfile — production image for running Soulacy via docker compose.
#
# Stages:
#   gui      → builds the Svelte dashboard with Node 20
#   gobuild  → compiles soulacy + sy with cgo (sqlite-vec, mattn/go-sqlite3)
#   runtime  → slim Debian image with Python 3 + the SDK + both binaries
#
# Usage (standalone):
#   docker build -t soulacy .
#   docker run -p 1947:1947 -v ~/.soulacy:/home/soulacy/.soulacy soulacy
#
# Usage (full stack):
#   docker compose up   ← starts gateway + Postgres + Qdrant

# ── Stage 1: GUI ─────────────────────────────────────────────────────────────
FROM node:20-bookworm-slim AS gui
WORKDIR /src/gui
COPY gui/package.json gui/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    npm install --no-audit --no-fund --silent
COPY gui ./
RUN npm run build
# Output: /src/gui/dist  (copied to /src/internal/webui/dist in gobuild)

# ── Stage 2: Go binary ───────────────────────────────────────────────────────
FROM golang:1.26.6-bookworm AS gobuild
ARG VERSION=dev
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config libsqlite3-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
# go.mod has a local replace (github.com/soulacy/soulacy/sdk => ./sdk), so
# `go mod download` must be able to read the replacement module's go.mod.
# Copy just that file first to keep this layer cacheable.
COPY sdk/go.mod ./sdk/go.mod
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Inject the Svelte build so the gateway binary embeds the GUI.
# gui/vite.config.js sets build.outDir to ../internal/webui/dist, so the gui
# stage emits the bundle at /src/internal/webui/dist (not /src/gui/dist).
COPY --from=gui /src/internal/webui/dist /src/internal/webui/dist

ENV CGO_ENABLED=1
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
        -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=${VERSION}" \
        -o /out/soulacy ./cmd/soulacy \
    && go build \
        -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=${VERSION}" \
        -o /out/sy ./cmd/sy

# ── Stage 3: Runtime ─────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

# Runtime + agent tooling. soulacy itself only needs libsqlite3 + ca-certificates;
# everything else is so the system agent's shell_exec tasks — cloning repos,
# installing MCP servers and pip/npm packages, unzipping archives, building
# native extensions, inspecting JSON — work out of the box instead of failing
# with "command not found". This is one layer for cache/size; trim packages you
# don't need (build-essential and nodejs are the largest). Inline comments are
# intentionally NOT used inside the install list: backslash-continuation joins
# the lines, so a '#' would comment out every package after it.
RUN apt-get update && apt-get install -y --no-install-recommends \
        libsqlite3-0 ca-certificates \
        python3 python3-pip python3-dev python3-venv pipx \
        nodejs npm \
        git curl wget unzip zip tar xz-utils \
        build-essential pkg-config \
        jq ripgrep less file procps \
    && rm -rf /var/lib/apt/lists/*

# Python SDK — agents written in Python work without any extra setup.
# The SDK is experimental and not yet published to PyPI, so it is NOT installed
# here. Once published via CI, add an explicit (non-hedged) install step.
# RUN pip3 install --break-system-packages soulacy

RUN useradd --create-home --shell /usr/sbin/nologin soulacy

COPY --from=gobuild --chown=soulacy /out/soulacy /usr/local/bin/soulacy
COPY --from=gobuild --chown=soulacy /out/sy      /usr/local/bin/sy

# Data directory — mount a volume here to persist agents, memory, and logs.
RUN mkdir -p /home/soulacy/.soulacy && chown soulacy:soulacy /home/soulacy/.soulacy
VOLUME ["/home/soulacy/.soulacy"]

USER soulacy
WORKDIR /home/soulacy

# NOTE: do NOT pin SOULACY_CONFIG_PATH to an explicit file here. Doing so forces
# explicit-file config mode, and a missing file becomes a hard startup error —
# which defeats the first-run bootstrap that *creates* config.yaml. With the var
# unset, config loading searches ~/.soulacy (the mounted volume), tolerates a
# missing file on first run, and writes a fresh config.yaml + API key there.

# Canonical install-path hints, available to EVERY process in the container
# (docker exec, the `sy` CLI, and agent shell tools), so nothing has to guess
# where things live. For the standard soulspace layout (a fresh data volume)
# these paths are deterministic and match what the engine computes at runtime.
#
# Two deliberate omissions:
#  - SOULACY_CONFIG_PATH is NOT set: the config loader reads it, and pinning it
#    to a file breaks first-run bootstrap (see the note above). The hint below
#    is named SOULACY_CONFIG_FILE precisely because the loader ignores it.
#  - SOULACY_WORKSPACE is NOT set: the workspace resolver reads it and pinning
#    it would override legacy-layout detection. The engine still injects the
#    correctly-resolved paths into agent shell tools at runtime, so accuracy is
#    preserved even on non-default layouts.
ENV SOULACY_CONFIG_FILE=/home/soulacy/.soulacy/soulspace/config.yaml \
    SOULACY_AGENTS_DIR=/home/soulacy/.soulacy/soulspace/agents \
    SOULACY_SKILLS_DIR=/home/soulacy/.soulacy/soulspace/skills \
    SOULACY_PLUGINS_DIR=/home/soulacy/.soulacy/soulspace/plugins \
    SOULACY_MCP_DIR=/home/soulacy/.soulacy/soulspace/mcp-servers

EXPOSE 1947

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fs http://localhost:1947/api/v1/health || exit 1

ENTRYPOINT ["soulacy"]
CMD ["serve"]
