#!/usr/bin/env bash
# run-runtime.sh — launch the Soulacy gateway as a clean RUNTIME instance,
# fully separated from this development checkout.
#
# WHY THIS EXISTS
#   Launching `./bin/soulacy serve` from the repo root makes the config loader
#   fall back to the repo's dev ./config.yaml (whose agent_dirs point at
#   examples/agents) — so dev artifacts leak into the running instance. This
#   script pins the runtime workspace to ~/.soulacy/soulspace and launches from
#   OUTSIDE the repo, so the dev config can never shadow the real install.
#
# USAGE
#   Build in the repo:   make build
#   Run the runtime:     ./scripts/run-runtime.sh            (foreground; Ctrl-C to stop)
#   Override workspace:  SOULACY_WORKSPACE=~/other ./scripts/run-runtime.sh
#   Extra serve flags:   ./scripts/run-runtime.sh --port 9000
set -euo pipefail

# Repo dir = parent of this script's dir (lets us find the built binary).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN="$REPO_DIR/bin/soulacy"

# Dedicated runtime workspace (config, agents, secrets, DBs) — NOT the repo.
RUNTIME_WORKSPACE="${SOULACY_WORKSPACE:-$HOME/.soulacy/soulspace}"

if [[ ! -x "$BIN" ]]; then
  echo "error: $BIN not found or not executable." >&2
  echo "Build it first:  (cd \"$REPO_DIR\" && make build)" >&2
  exit 1
fi

mkdir -p "$RUNTIME_WORKSPACE"
export SOULACY_WORKSPACE="$RUNTIME_WORKSPACE"

echo "→ Soulacy runtime"
echo "   binary    : $BIN"
echo "   workspace : $RUNTIME_WORKSPACE"
echo "   (launched from the workspace so the repo's dev config can't leak in)"
echo

# cd OUT of the repo so '.' (the last config-search fallback) is never the repo.
# Foreground run; Ctrl-C stops it. exec so signals go straight to the gateway.
cd "$RUNTIME_WORKSPACE"
exec "$BIN" serve "$@"
