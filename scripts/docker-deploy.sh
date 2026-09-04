#!/usr/bin/env bash
#
# deploy.sh — Interactive Docker deploy helper.
#
# Builds a Docker image from a Dockerfile, runs it as a container, publishes a
# host port, and prints a URL you can open from your machine. Every parameter
# has a sensible default and can be entered interactively, supplied via an
# environment variable, or passed as a CLI flag — so the same script works for
# a quick local spin-up and for unattended/CI deploys.
#
# Quick start (interactive):
#   ./scripts/deploy.sh
#
# Non-interactive (accept all defaults, no prompts):
#   ./scripts/deploy.sh --yes
#
# Override any parameter via flag or env var, e.g.:
#   ./scripts/deploy.sh --host-port 9000 --name myapp
#   HOST_PORT=9000 APP_NAME=myapp ./scripts/deploy.sh --yes
#
# This script is intentionally generic. The defaults below target the Soulacy
# gateway, but you can point IMAGE/Dockerfile/ports at any web service.
#
set -euo pipefail

# ──────────────────────────────────────────────────────────────────────────────
# Defaults (override via env or flags). Env vars win over these; flags win over
# env; interactive answers win over everything.
# ──────────────────────────────────────────────────────────────────────────────
APP_NAME="${APP_NAME:-soulacy}"            # container + image name
IMAGE="${IMAGE:-${APP_NAME}:latest}"       # image tag to build/run
DOCKERFILE="${DOCKERFILE:-Dockerfile}"     # Dockerfile path (relative to context)
BUILD_CONTEXT="${BUILD_CONTEXT:-.}"        # docker build context
HOST_PORT="${HOST_PORT:-8080}"             # port published on the host
CONTAINER_PORT="${CONTAINER_PORT:-18789}"  # port the app listens on inside
BIND_HOST="${BIND_HOST:-0.0.0.0}"          # in-container bind addr (0.0.0.0 so the published port is reachable)
DATA_DIR="${DATA_DIR:-$HOME/.soulacy}"     # host dir mounted for persistence ("" to skip)
DATA_MOUNT="${DATA_MOUNT:-/home/soulacy/.soulacy}"  # mount point inside container
# The skill loader also scans the cross-client location ~/.agents/skills, which
# lives OUTSIDE ~/.soulacy. Without this mount, skills installed there live only
# in the container layer and are lost on every redeploy. Persist it too. Set to
# "" to skip.
AGENTS_DIR="${AGENTS_DIR:-$HOME/.agents}"   # host dir for cross-client skills/agents ("" to skip)
AGENTS_MOUNT="${AGENTS_MOUNT:-/home/soulacy/.agents}"  # mount point inside container
API_KEY="${API_KEY:-}"                     # auth key; blank = auto-generate
# LLM provider API keys, passed into the container as
# SOULACY_LLM_PROVIDERS_<PROVIDER>_API_KEY so they are supplied fresh on every
# boot. This bypasses the encrypted credential vault, whose machine-id-derived
# key is ephemeral inside containers (vault entries would not survive restarts).
#
# Provide one or more "<provider>=<key>" pairs, e.g. "google=AIza... openai=sk-...".
# Accepted via the PROVIDER_KEYS env var (space-separated) or repeatable
# --provider-key flags. Provider names are case-insensitive (google, openai,
# anthropic, ollama, …) and are upper-cased into the env var name.
PROVIDER_KEYS="${PROVIDER_KEYS:-}"         # space-separated "<provider>=<key>" pairs; blank = none
HEALTH_PATH="${HEALTH_PATH:-/api/v1/health}" # path polled to confirm readiness ("" to skip)
# Where the gateway should reach Ollama. Inside a container `localhost` is the
# container itself, so a host-side Ollama must be addressed via the host. On
# Docker Desktop (macOS/Windows) that's host.docker.internal; on Linux the
# --add-host below maps it to the host gateway. Set to "" to skip entirely
# (e.g. cloud-only LLMs, or Ollama running in its own container/network).
OLLAMA_HOST="${OLLAMA_HOST:-host.docker.internal:11434}"  # host:port or full URL, or "" to skip
RESTART_POLICY="${RESTART_POLICY:-unless-stopped}"
DO_BUILD="${DO_BUILD:-ask}"                # yes | no | ask
ASSUME_YES="${ASSUME_YES:-0}"             # 1 = no prompts

# ──────────────────────────────────────────────────────────────────────────────
# Pretty output
# ──────────────────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; C=$'\033[36m'; N=$'\033[0m'
else
  B=""; G=""; Y=""; R=""; C=""; N=""
fi
info()  { printf "%s==>%s %s\n" "$C" "$N" "$*"; }
ok()    { printf "%s ✓ %s %s\n" "$G" "$N" "$*"; }
warn()  { printf "%s ! %s %s\n" "$Y" "$N" "$*" >&2; }
die()   { _DIED=1; printf "%s ✗ %s %s\n" "$R" "$N" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
${B}docker-deploy.sh${N} — interactive Docker deploy

Usage: $0 [options]

Options:
  --name NAME            Container/image base name        (default: $APP_NAME)
  --image TAG            Image tag to build/run           (default: $IMAGE)
  --dockerfile PATH      Dockerfile path                  (default: $DOCKERFILE)
  --context DIR          Build context                    (default: $BUILD_CONTEXT)
  --host-port PORT       Host port to publish             (default: $HOST_PORT)
  --container-port PORT  Port the app listens on inside   (default: $CONTAINER_PORT)
  --bind-host ADDR       In-container bind address        (default: $BIND_HOST)
  --data-dir DIR         Host dir to persist (empty=none) (default: $DATA_DIR)
  --api-key KEY          Auth key (empty = auto-generate)
  --provider-key P=KEY   LLM provider API key, e.g. google=AIza... or openai=sk-...
                         Repeatable. Passed as SOULACY_LLM_PROVIDERS_<P>_API_KEY.
  --health-path PATH     Readiness probe path (empty=none)(default: $HEALTH_PATH)
  --ollama-host HOST     Ollama host:port or URL the gateway should call
                         (empty=skip; default: $OLLAMA_HOST)
  --no-build             Run existing image, skip build
  --build                Force a build
  -y, --yes              Accept defaults, no prompts
  -h, --help             Show this help

All options also read from matching UPPER_SNAKE_CASE env vars.
EOF
}

# ──────────────────────────────────────────────────────────────────────────────
# Parse flags
# ──────────────────────────────────────────────────────────────────────────────
while [ $# -gt 0 ]; do
  case "$1" in
    --name)           APP_NAME="$2"; IMAGE="${APP_NAME}:latest"; shift 2 ;;
    --image)          IMAGE="$2"; shift 2 ;;
    --dockerfile)     DOCKERFILE="$2"; shift 2 ;;
    --context)        BUILD_CONTEXT="$2"; shift 2 ;;
    --host-port)      HOST_PORT="$2"; shift 2 ;;
    --container-port) CONTAINER_PORT="$2"; shift 2 ;;
    --bind-host)      BIND_HOST="$2"; shift 2 ;;
    --data-dir)       DATA_DIR="$2"; shift 2 ;;
    --api-key)        API_KEY="$2"; shift 2 ;;
    --provider-key)   PROVIDER_KEYS="${PROVIDER_KEYS:+$PROVIDER_KEYS }$2"; shift 2 ;;
    --health-path)    HEALTH_PATH="$2"; shift 2 ;;
    --ollama-host)    OLLAMA_HOST="$2"; shift 2 ;;
    --no-build)       DO_BUILD="no"; shift ;;
    --build)          DO_BUILD="yes"; shift ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage; exit 0 ;;
    *) die "Unknown option: $1  (try --help)" ;;
  esac
done

# ──────────────────────────────────────────────────────────────────────────────
# Helpers
# ──────────────────────────────────────────────────────────────────────────────
# ask VAR "Prompt" "default"  — prompts unless ASSUME_YES; assigns to VAR.
ask() {
  local __var="$1" __prompt="$2" __default="$3" __reply
  if [ "$ASSUME_YES" = "1" ]; then
    printf -v "$__var" '%s' "$__default"; return
  fi
  read -r -p "$(printf '%s%s%s [%s]: ' "$B" "$__prompt" "$N" "$__default")" __reply || true
  printf -v "$__var" '%s' "${__reply:-$__default}"
}

have() { command -v "$1" >/dev/null 2>&1; }

# ──────────────────────────────────────────────────────────────────────────────
# Friendly failure on any unexpected error (set -e aborts; this explains where).
# ──────────────────────────────────────────────────────────────────────────────
trap 'rc=$?; if [ "$rc" -ne 0 ] && [ "${_DIED:-0}" != "1" ]; then printf "\n%s ✗ %s deploy.sh stopped unexpectedly (exit %s near line %s). Nothing further was changed.\n" "$R" "$N" "$rc" "${BASH_LINENO[0]:-?}" >&2; fi' EXIT

# ──────────────────────────────────────────────────────────────────────────────
# Preflight — verify required dependencies, warn on missing optional ones.
# ──────────────────────────────────────────────────────────────────────────────
preflight() {
  local missing=()

  # --- Required: docker CLI + a reachable daemon -----------------------------
  if ! have docker; then
    missing+=("docker — install Docker Desktop (https://docs.docker.com/get-docker/) or the docker engine package")
  elif ! docker info >/dev/null 2>&1; then
    die "Docker is installed but the daemon isn't reachable.
       • macOS/Windows: start Docker Desktop and wait for it to report 'running'.
       • Linux: 'sudo systemctl start docker' (and add your user to the 'docker' group).
       • Permissions: a 'permission denied' here usually means you need to be in the docker group or use sudo."
  fi

  # 'docker build' needs either BuildKit or the legacy builder; either is fine,
  # but flag if the docker CLI is too stripped-down to build at all.
  if have docker && ! docker build --help >/dev/null 2>&1; then
    missing+=("docker build — your docker CLI cannot build images (install the full Docker CLI/buildx)")
  fi

  if [ "${#missing[@]}" -gt 0 ]; then
    printf '%s ✗ %s Missing required dependencies:\n' "$R" "$N" >&2
    for m in "${missing[@]}"; do printf '     • %s\n' "$m" >&2; done
    die "Install the items above and re-run. Nothing was changed."
  fi

  # --- Optional: detect an HTTP probe tool for the readiness check -----------
  if have curl;  then HTTP_TOOL="curl"
  elif have wget; then HTTP_TOOL="wget"
  else
    HTTP_TOOL=""
    warn "Neither curl nor wget found — will fall back to Docker's own health status (or skip the readiness wait)."
  fi

}
preflight

info "Deploy configuration"
PREV_NAME="$APP_NAME"
ask APP_NAME       "App / container name"        "$APP_NAME"
# If the user renamed the app and the image tag still carries the old name,
# suggest an image tag that follows the new name.
if [ "$APP_NAME" != "$PREV_NAME" ] && [ "$IMAGE" = "${PREV_NAME}:latest" ]; then
  IMAGE="${APP_NAME}:latest"
fi
ask IMAGE          "Image tag"                   "${IMAGE:-${APP_NAME}:latest}"
ask HOST_PORT      "Host port (open in browser)" "$HOST_PORT"
ask CONTAINER_PORT "Container port (app listens)" "$CONTAINER_PORT"
ask BIND_HOST      "In-container bind address"   "$BIND_HOST"
ask DATA_DIR       "Host data dir (blank = none)" "$DATA_DIR"
ask OLLAMA_HOST    "Ollama host:port the gateway calls (blank = cloud only)" "$OLLAMA_HOST"

# Normalize the Ollama target into a base URL the gateway env var expects.
OLLAMA_URL=""
if [ -n "$OLLAMA_HOST" ]; then
  case "$OLLAMA_HOST" in
    http://*|https://*) OLLAMA_URL="$OLLAMA_HOST" ;;
    *)                  OLLAMA_URL="http://$OLLAMA_HOST" ;;
  esac
fi

# API key handling: by default the gateway generates its own key on first run
# (written into config.yaml) and authenticates with THAT — the SOULACY_SERVER_API_KEY
# env var is ignored by the config loader for this field. So we don't force a key
# here; we read the real one back after startup. Supplying --api-key is optional
# and, if given, is applied to config.yaml after first boot (see below).
if [ -z "$API_KEY" ] && [ "$ASSUME_YES" != "1" ]; then
  ask API_KEY "Custom API key (blank = let the gateway generate one)" ""
fi
DESIRED_KEY="$API_KEY"   # empty = accept the gateway's generated key

# Validate port numbers (1–65535).
for p in "$HOST_PORT:host" "$CONTAINER_PORT:container"; do
  num="${p%%:*}"; label="${p##*:}"
  case "$num" in *[!0-9]*|"") die "The $label port must be numeric (got '$num')." ;; esac
  if [ "$num" -lt 1 ] || [ "$num" -gt 65535 ]; then
    die "The $label port must be 1–65535 (got $num)."
  fi
done

# Warn early if the host port already looks busy (best-effort; never fatal).
port_in_use() {
  local port="$1"
  if have lsof;  then lsof -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 && return 0
  elif have nc;  then nc -z localhost "$port" >/dev/null 2>&1 && return 0
  elif { exec 9<>"/dev/tcp/127.0.0.1/$port"; } 2>/dev/null; then exec 9>&- 9<&-; return 0
  fi
  return 1
}
if port_in_use "$HOST_PORT"; then
  warn "Host port $HOST_PORT is already in use. The container may fail to start — pick another with --host-port, or free it first."
fi

# Verify the data dir is creatable/writable before we commit to a deploy.
if [ -n "$DATA_DIR" ]; then
  if ! mkdir -p "$DATA_DIR" 2>/dev/null; then
    die "Cannot create data dir: $DATA_DIR (check the path and permissions, or pass --data-dir \"\" to run without persistence)."
  fi
  if [ ! -w "$DATA_DIR" ]; then
    die "Data dir is not writable: $DATA_DIR (fix permissions or pass --data-dir \"\")."
  fi
fi

# Decide whether to build
if [ "$DO_BUILD" = "ask" ]; then
  if [ "$ASSUME_YES" = "1" ]; then
    DO_BUILD="yes"
  else
    ask _b "Build image from $DOCKERFILE? (y/n)" "y"
    case "$_b" in [Yy]*) DO_BUILD="yes" ;; *) DO_BUILD="no" ;; esac
  fi
fi

# ──────────────────────────────────────────────────────────────────────────────
# Build
# ──────────────────────────────────────────────────────────────────────────────
if [ "$DO_BUILD" = "yes" ]; then
  [ -f "$BUILD_CONTEXT/$DOCKERFILE" ] || die "Dockerfile not found: $BUILD_CONTEXT/$DOCKERFILE"
  if [ ! -f "$BUILD_CONTEXT/.dockerignore" ]; then
    warn "No .dockerignore in $BUILD_CONTEXT — the build context may be huge and slow. See scripts/README or add one."
  fi
  info "Building image ${B}$IMAGE${N} from $DOCKERFILE ..."
  if ! docker build -t "$IMAGE" -f "$BUILD_CONTEXT/$DOCKERFILE" "$BUILD_CONTEXT"; then
    die "Image build failed (see the docker output above).
       Common causes: no network for base-image/dependency downloads, a failing
       build step, or out-of-disk. Re-run, or build manually to debug:
         docker build -t $IMAGE -f $BUILD_CONTEXT/$DOCKERFILE $BUILD_CONTEXT"
  fi
  ok "Image built: $IMAGE"
else
  docker image inspect "$IMAGE" >/dev/null 2>&1 || die "Image $IMAGE not found locally and build was skipped."
  info "Using existing image: $IMAGE"
fi

# ──────────────────────────────────────────────────────────────────────────────
# Replace any existing container with the same name
# ──────────────────────────────────────────────────────────────────────────────
if docker ps -a --format '{{.Names}}' | grep -qx "$APP_NAME"; then
  warn "Removing existing container named '$APP_NAME'."
  docker rm -f "$APP_NAME" >/dev/null
fi

# ──────────────────────────────────────────────────────────────────────────────
# Run
# ──────────────────────────────────────────────────────────────────────────────
RUN_ARGS=(
  --name "$APP_NAME"
  --detach
  --restart "$RESTART_POLICY"
  --publish "${HOST_PORT}:${CONTAINER_PORT}"
  --env "SOULACY_SERVER_HOST=${BIND_HOST}"
  --env "SOULACY_SERVER_PORT=${CONTAINER_PORT}"
  # NOTE: SOULACY_SERVER_API_KEY is intentionally NOT passed — the config loader
  # ignores it (no registered default for that key), so it would mislead. The
  # gateway generates its own key on first run; we read it back below.
)
if [ -n "$DATA_DIR" ]; then
  RUN_ARGS+=( --volume "${DATA_DIR}:${DATA_MOUNT}" )
  # Pin the workspace to the mounted volume. This makes skills, agents, memory,
  # and config always resolve to persistent storage — independent of HOME or any
  # stale ~/.soulacy/workspace pointer — so nothing installed through the
  # supported paths can be lost on a redeploy.
  RUN_ARGS+=( --env "SOULACY_WORKSPACE=${DATA_MOUNT}/soulspace" )
fi
# Persist the cross-client skills/agents location too (~/.agents). The skill
# loader also scans it, so skills installed there must survive a redeploy.
if [ -n "$AGENTS_DIR" ]; then
  mkdir -p "$AGENTS_DIR" 2>/dev/null || true
  RUN_ARGS+=( --volume "${AGENTS_DIR}:${AGENTS_MOUNT}" )
fi
if [ -n "$OLLAMA_URL" ]; then
  RUN_ARGS+=( --env "SOULACY_LLM_PROVIDERS_OLLAMA_BASE_URL=${OLLAMA_URL}" )
  # On Linux, host.docker.internal isn't built in — map it to the host gateway.
  # On Docker Desktop (macOS/Windows) this is a harmless no-op.
  case "$OLLAMA_URL" in
    *host.docker.internal*) RUN_ARGS+=( --add-host "host.docker.internal:host-gateway" ) ;;
  esac
fi

# LLM provider API keys → env vars. Injecting them on each run keeps the key
# present even though the container's credential vault is ephemeral (its
# machine-id-derived encryption key doesn't survive container restarts).
if [ -n "$PROVIDER_KEYS" ]; then
  for pair in $PROVIDER_KEYS; do
    name="${pair%%=*}"          # provider id, e.g. google
    key="${pair#*=}"            # the api key
    if [ -z "$name" ] || [ "$name" = "$pair" ] || [ -z "$key" ]; then
      die "Bad --provider-key '$pair' — expected <provider>=<key>, e.g. google=AIza..."
    fi
    # Upper-case the provider id for the env var name (bash 3.2 compatible).
    upper="$(printf '%s' "$name" | tr '[:lower:]' '[:upper:]')"
    RUN_ARGS+=( --env "SOULACY_LLM_PROVIDERS_${upper}_API_KEY=${key}" )
    info "Provider key set: ${name} → SOULACY_LLM_PROVIDERS_${upper}_API_KEY"
  done
fi

info "Starting container ..."
if ! CONTAINER_ID="$(docker run "${RUN_ARGS[@]}" "$IMAGE" 2>&1)"; then
  printf '%s\n' "$CONTAINER_ID" >&2
  die "Failed to start the container. If the message above mentions a port
       binding, host port $HOST_PORT is taken — re-run with --host-port <other>."
fi
ok "Container started: ${CONTAINER_ID:0:12}"

# ──────────────────────────────────────────────────────────────────────────────
# Wait for readiness — probe over HTTP if a tool exists, else fall back to
# Docker's own health/run state, else just report the URL.
# ──────────────────────────────────────────────────────────────────────────────
URL="http://localhost:${HOST_PORT}"

probe_http() {
  case "$HTTP_TOOL" in
    curl) curl -fs -o /dev/null "${URL}${HEALTH_PATH}" 2>/dev/null ;;
    wget) wget -q -O /dev/null "${URL}${HEALTH_PATH}" 2>/dev/null ;;
    *)    return 2 ;;  # no HTTP tool available
  esac
}

container_running() { docker ps --format '{{.Names}}' | grep -qx "$APP_NAME"; }

if [ -n "$HEALTH_PATH" ]; then
  info "Waiting for ${URL}${HEALTH_PATH} ..."
  ready=0; i=0
  while [ "$i" -lt 30 ]; do
    i=$((i + 1))
    # '|| rc=$?' keeps a failing probe from tripping 'set -e' (curl/wget return
    # non-zero while the service is still warming up — that's expected).
    rc=0; probe_http || rc=$?
    if [ "$rc" -eq 0 ]; then ready=1; break; fi
    if ! container_running; then
      warn "Container exited early. Recent logs:"; docker logs --tail 40 "$APP_NAME" 2>&1 || true
      die "Deploy failed — the container stopped running. Inspect logs above."
    fi
    if [ "$rc" -eq 2 ]; then
      # No HTTP probe tool: fall back to Docker health, else assume the running
      # container is good enough and stop waiting.
      hs="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$APP_NAME" 2>/dev/null || echo none)"
      case "$hs" in
        healthy) ready=1; break ;;
        none)    ready=2; break ;;  # no probe + no docker healthcheck → can't confirm
      esac
    fi
    sleep 2
  done
  case "$ready" in
    1) ok "Service is healthy." ;;
    2) warn "Couldn't actively verify readiness (no curl/wget and no container healthcheck). The container is running — try the URL below." ;;
    *) warn "Health check didn't pass within ~60s; the app may still be starting. Check: docker logs -f $APP_NAME" ;;
  esac
fi

# ──────────────────────────────────────────────────────────────────────────────
# Resolve the REAL API key — the one the gateway actually authenticates with.
# It is generated on first run and stored in config.yaml inside the container.
# ──────────────────────────────────────────────────────────────────────────────
CFG_IN_CONTAINER="${DATA_MOUNT}/config.yaml"

read_key_from_config() {
  # The server api_key has the distinctive `sy_` + 64-hex format, so we can pull
  # it unambiguously even though config.yaml may contain other *_api_key fields.
  docker exec "$APP_NAME" sh -c "cat '$CFG_IN_CONTAINER' 2>/dev/null" \
    | grep -oE 'sy_[0-9a-f]{64}' | head -n1
}

API_KEY=""
if container_running; then
  # Give the bootstrap a moment to write config.yaml if the probe finished first.
  for _ in 1 2 3 4 5; do
    API_KEY="$(read_key_from_config || true)"
    [ -n "$API_KEY" ] && break
    sleep 1
  done

  # If the user asked for a specific key and it differs, patch config.yaml
  # (replacing only the first/api server key) and restart so it takes effect.
  if [ -n "$DESIRED_KEY" ] && [ "$DESIRED_KEY" != "$API_KEY" ]; then
    info "Applying your custom API key and restarting ..."
    if docker exec "$APP_NAME" sh -c \
         "sed -i '0,/api_key:/{s|api_key: \".*\"|api_key: \"$DESIRED_KEY\"|}' '$CFG_IN_CONTAINER'" 2>/dev/null \
       && docker restart "$APP_NAME" >/dev/null; then
      sleep 2
      API_KEY="$(read_key_from_config || true)"
    else
      warn "Couldn't apply the custom key; keeping the gateway-generated one."
    fi
  fi
fi

# ──────────────────────────────────────────────────────────────────────────────
# Summary
# ──────────────────────────────────────────────────────────────────────────────
printf '\n%s%s──────────────────────────────────────────────%s\n' "$B" "$G" "$N"
printf '%s  Deployed:%s   %s\n' "$B" "$N" "$APP_NAME"
printf '%s  URL:%s        %s%s%s\n' "$B" "$N" "$C" "$URL" "$N"
if [ -n "$OLLAMA_URL" ]; then
  printf '%s  Ollama:%s     %s\n' "$B" "$N" "$OLLAMA_URL"
fi
if [ -n "$API_KEY" ]; then
  printf '%s  API key:%s    %s\n' "$B" "$N" "$API_KEY"
else
  printf '%s  API key:%s    %s(read it with: docker exec %s sh -c "grep api_key %s")%s\n' \
    "$B" "$N" "$Y" "$APP_NAME" "$CFG_IN_CONTAINER" "$N"
fi
printf '%s  Logs:%s       docker logs -f %s\n' "$B" "$N" "$APP_NAME"
printf '%s  Shell:%s      docker exec -it %s bash\n' "$B" "$N" "$APP_NAME"
printf '%s  Stop:%s       docker rm -f %s\n' "$B" "$N" "$APP_NAME"
printf '%s%s──────────────────────────────────────────────%s\n\n' "$B" "$G" "$N"
