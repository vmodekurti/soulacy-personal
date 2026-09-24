# Dockerfile — production image for running Soulacy via docker compose.
#
# Stages:
#   gui      → builds the Svelte dashboard with Node 20
#   gobuild  → compiles soulacy + sy with cgo (sqlite-vec, mattn/go-sqlite3)
#   runtime  → slim Python 3.12 image + the SDK + both binaries
#
# Usage (standalone):
#   docker build -t soulacy .
#   docker run -p 18789:18789 -v ~/.soulacy:/home/soulacy/.soulacy soulacy
#
# Usage (full stack):
#   docker compose up   ← starts gateway + Postgres + Qdrant

# ── Stage 1: GUI ─────────────────────────────────────────────────────────────
FROM node:20-bookworm-slim AS gui
WORKDIR /src/gui
COPY gui/package.json gui/package-lock.json* ./
RUN npm install --no-audit --no-fund --silent
COPY gui ./
RUN npm run build
# Output: /src/gui/dist  (copied to /src/internal/webui/dist in gobuild)

# ── Stage 2: Go binary ───────────────────────────────────────────────────────
FROM golang:1.26.6-bookworm AS gobuild
# Empty, not "dev": an unset build arg falls through to the VERSION file in the
# repo below, so a build that nobody parameterised still knows what it is.
ARG VERSION=
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential pkg-config libsqlite3-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
# go.mod has a local replace (github.com/soulacy/soulacy/sdk => ./sdk), so
# `go mod download` must be able to read the replacement module's go.mod.
# Copy just that file first to keep this layer cacheable.
COPY sdk/go.mod ./sdk/go.mod
RUN go mod download

COPY . .
# Inject the Svelte build so the gateway binary embeds the GUI.
# gui/vite.config.js sets build.outDir to ../internal/webui/dist, so the gui
# stage emits the bundle at /src/internal/webui/dist (not /src/gui/dist).
COPY --from=gui /src/internal/webui/dist /src/internal/webui/dist

ENV CGO_ENABLED=1
# Where the version comes from, in order:
#   1. --build-arg VERSION=…      (docker compose and deploy-main.sh pass
#                                  `git describe`, so a host build is exact)
#   2. the VERSION file in the repo (a platform that cannot pass build args —
#                                  Railway, Render, Coolify — still reports the
#                                  release it was built from, instead of "dev")
#   3. "dev"                       (someone building an unreleased tree)
#
# Without step 2 every managed-platform image self-reported "dev", the update
# checker skipped it as an incomparable dev build, and a redeploy that worked
# looked exactly like one that never happened (#227).
RUN VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo dev)}" \
    && echo "building version ${VERSION}" \
    && go build \
        -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=${VERSION}" \
        -o /out/soulacy ./cmd/soulacy \
    && go build \
        -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=${VERSION}" \
        -o /out/sy ./cmd/sy

# ── Stage 3: Runtime ─────────────────────────────────────────────────────────
FROM python:3.12-slim-bookworm AS runtime

# Runtime + agent tooling. soulacy itself only needs libsqlite3 + ca-certificates;
# everything else is so the system agent's shell_exec tasks — cloning repos,
# installing MCP servers and pip/npm packages, unzipping archives, building
# native extensions, inspecting JSON — work out of the box instead of failing
# with "command not found". This is one layer for cache/size; trim packages you
# don't need (build-essential and nodejs are the largest). Inline comments are
# intentionally NOT used inside the install list: backslash-continuation joins
# the lines, so a '#' would comment out every package after it.
RUN apt-get update && apt-get install -y --no-install-recommends \
        libsqlite3-0 ca-certificates gosu \
        git curl wget unzip zip tar xz-utils \
        build-essential pkg-config \
        jq ripgrep less file procps \
    && rm -rf /var/lib/apt/lists/* \
    && python3 -m pip install --no-cache-dir pipx

# Chromium's shared libraries — OFF by default.
#
# Most installs never drive a browser, and this is ~30MB on disk they would
# carry anyway. But the libraries are system packages, so an unprivileged
# deployment cannot add them later: that is why they were put in the image in
# the first place.
#
# They no longer have to be. The libraries only have to be FOUND, not
# installed: unpacked into a directory on LD_LIBRARY_PATH they load from the
# mounted volume exactly as well as from /usr/lib. So a deployment that wants a
# local browser downloads a ~12MB bundle once (see
# scripts/build-browser-libs.sh), and it survives redeploys because it lives on
# the volume.
#
# Build with --build-arg WITH_BROWSER_LIBS=1 to bake them in instead — worth it
# for an image you build yourself and always use for browsing, and pointless
# for everyone else.
ARG WITH_BROWSER_LIBS=0
RUN if [ "$WITH_BROWSER_LIBS" = "1" ]; then \
        apt-get update && apt-get install -y --no-install-recommends \
            libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 \
            libcairo2 libcups2 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0 \
            libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 \
            libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 libxi6 libexpat1 \
            fonts-liberation \
        && rm -rf /var/lib/apt/lists/*; \
    fi

# Browsers are downloaded, not baked in: they are large, they update on their
# own cadence, and a browser inside the image would have to be re-downloaded on
# every rebuild. This path is on the mounted volume (see docker-compose.yml),
# so one install survives every deploy.
ENV PLAYWRIGHT_BROWSERS_PATH=/home/soulacy/.soulacy/playwright-browsers

# Node 20, taken from the stage that already has it rather than from apt.
#
# Debian bookworm's `nodejs` package is 18, and 18 is now below the floor for
# the MCP servers people actually install: @playwright/mcp refuses to start on
# it, printing "Playwright requires Node.js 20 or higher" and exiting — which
# reaches the gateway as "stdio transport closed before response", a message
# that says nothing about Node and sends you looking at the config instead.
#
# /usr/local/bin precedes /usr/bin on PATH, and the GUI stage is the same
# Debian base, so this is one copy rather than a second package manager.
COPY --from=gui /usr/local/bin/node /usr/local/bin/node
COPY --from=gui /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && node --version && npm --version

# Python SDK — agents written in Python work without any extra setup.
# The SDK is experimental and not yet published to PyPI, so it is NOT installed
# here. Once published via CI, add an explicit (non-hedged) install step.
# RUN pip3 install --break-system-packages soulacy

RUN useradd --create-home --shell /usr/sbin/nologin soulacy

COPY --from=gobuild --chown=soulacy /out/soulacy /usr/local/bin/soulacy
COPY --from=gobuild --chown=soulacy /out/sy      /usr/local/bin/sy
COPY --chown=soulacy --chmod=755 deploy/common/docker-entrypoint.sh /usr/local/bin/soulacy-entrypoint

# Data directory — mount a volume here to persist agents, memory, and logs.
# The hosting platform owns the volume declaration. Keeping this as a normal
# directory makes the image compatible with Railway's Dockerfile validator
# while Docker Compose and Railway templates can still mount it persistently.
RUN mkdir -p /home/soulacy/.soulacy && chown soulacy:soulacy /home/soulacy/.soulacy

WORKDIR /home/soulacy

# The entrypoint starts as root only long enough to initialize and repair the
# ownership of a platform-mounted data volume, then drops permanently to the
# unprivileged soulacy user before starting the gateway.

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
ENV SOULACY_SERVER_HOST=0.0.0.0 \
    SOULACY_SERVER_PORT=18789 \
    SOULACY_CONFIG_FILE=/home/soulacy/.soulacy/soulspace/config.yaml \
    SOULACY_AGENTS_DIR=/home/soulacy/.soulacy/soulspace/agents \
    SOULACY_SKILLS_DIR=/home/soulacy/.soulacy/soulspace/skills \
    SOULACY_PLUGINS_DIR=/home/soulacy/.soulacy/soulspace/plugins \
    SOULACY_MCP_DIR=/home/soulacy/.soulacy/soulspace/mcp-servers

EXPOSE 18789

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fs "http://localhost:${PORT:-${SOULACY_SERVER_PORT:-18789}}/" >/dev/null || exit 1

ENTRYPOINT ["soulacy-entrypoint"]
CMD ["serve"]
