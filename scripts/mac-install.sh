#!/usr/bin/env bash
# mac-install.sh — One-click Soulacy installer for macOS
#
# Run from the repo root:
#   bash scripts/mac-install.sh
#
# Or from a release tarball:
#   curl -sSL https://get.soulacy.dev/mac | bash
#
# What this does:
#   1. Builds GUI (npm) + Go binaries (make all)
#   2. Installs soulacy and sy to /usr/local/bin
#   3. Installs the Python SDK
#   4. Creates ~/.soulacy/ with default config, agents/, plugins/, skills/
#   5. Registers a LaunchAgent so the gateway starts on login
#   6. Opens the GUI in your browser

set -euo pipefail
cd "$(dirname "$0")/.."

# ── Colours ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'

log()  { echo -e "${GREEN}▶${NC}  $*"; }
warn() { echo -e "${YELLOW}⚠${NC}  $*"; }
err()  { echo -e "${RED}✗${NC}  $*" >&2; exit 1; }
ok()   { echo -e "${GREEN}✓${NC}  $*"; }

echo ""
echo -e "${BLUE}${BOLD}  ╔═══════════════════════════════════╗${NC}"
echo -e "${BLUE}${BOLD}  ║     Soulacy — macOS Installer     ║${NC}"
echo -e "${BLUE}${BOLD}  ╚═══════════════════════════════════╝${NC}"
echo ""

# ── Prerequisites ─────────────────────────────────────────────────────────────
log "Checking prerequisites..."

command -v go  >/dev/null 2>&1 || err "Go not found. Install from https://go.dev/dl/"
command -v npm >/dev/null 2>&1 || err "Node/npm not found. Install from https://nodejs.org"
go version | grep -qE 'go1\.(2[2-9]|[3-9][0-9])' || \
    warn "Go 1.22+ recommended. You have $(go version | awk '{print $3}')"
ok "Go $(go version | awk '{print $3}'), npm $(npm --version)"

# ── Build ─────────────────────────────────────────────────────────────────────
log "Building GUI + binaries (this takes ~60s on first run)..."
make all 2>&1 | tee /tmp/soulacy-build.log || {
    echo ""
    err "Build failed — see /tmp/soulacy-build.log for details."
}
ok "Build complete"

# ── Install binaries ──────────────────────────────────────────────────────────
INSTALL_DIR="/usr/local/bin"
log "Installing soulacy and sy to ${INSTALL_DIR}..."

if [ -w "$INSTALL_DIR" ]; then
    cp bin/soulacy "$INSTALL_DIR/soulacy"
    cp bin/sy      "$INSTALL_DIR/sy"
else
    warn "Need sudo to write to ${INSTALL_DIR}"
    sudo cp bin/soulacy "$INSTALL_DIR/soulacy"
    sudo cp bin/sy      "$INSTALL_DIR/sy"
fi
ok "soulacy and sy installed to ${INSTALL_DIR}"

# ── Python SDK ────────────────────────────────────────────────────────────────
if command -v pip3 >/dev/null 2>&1; then
    log "Installing Python SDK..."
    pip3 install -e sdk/python --quiet && ok "Python SDK installed (editable)" \
        || warn "Python SDK install failed — run: pip3 install -e sdk/python"
else
    warn "pip3 not found — skipping Python SDK"
fi

# ── Data directory ────────────────────────────────────────────────────────────
DATA_DIR="${HOME}/.soulacy"
log "Setting up data directory: ${DATA_DIR}"

mkdir -p \
    "${DATA_DIR}/agents" \
    "${DATA_DIR}/plugins" \
    "${DATA_DIR}/skills" \
    "${DATA_DIR}/tools" \
    "${DATA_DIR}/memory" \
    "${DATA_DIR}/logs"

if [ ! -f "${DATA_DIR}/config.yaml" ]; then
    cp configs/default.yaml "${DATA_DIR}/config.yaml"
    ok "Default config written to ${DATA_DIR}/config.yaml"
else
    ok "Existing config preserved at ${DATA_DIR}/config.yaml"
fi

# ── LaunchAgent (auto-start on login) ────────────────────────────────────────
PLIST_SRC="scripts/com.soulacy.gateway.plist"
PLIST_DEST="${HOME}/Library/LaunchAgents/com.soulacy.soulacy.plist"
AGENT_LABEL="com.soulacy.soulacy"

log "Installing LaunchAgent (auto-start on login)..."
mkdir -p "${HOME}/Library/LaunchAgents"

# Substitute the real install dir and username
sed \
    -e "s|__INSTALL_DIR__|${INSTALL_DIR}|g" \
    -e "s|REPLACE_WITH_USERNAME|$(whoami)|g" \
    "$PLIST_SRC" > "$PLIST_DEST"

# Unload any existing agent first (ignore errors if not loaded)
launchctl unload -w "$PLIST_DEST" 2>/dev/null || true
launchctl load -w "$PLIST_DEST"
ok "LaunchAgent installed — gateway will start automatically on login"

# ── Start now ─────────────────────────────────────────────────────────────────
log "Starting Soulacy gateway..."
launchctl start "$AGENT_LABEL" 2>/dev/null || soulacy serve &
sleep 2

GATEWAY_URL="http://localhost:18789"
if curl -sf "${GATEWAY_URL}/api/v1/health" >/dev/null 2>&1; then
    ok "Gateway is running at ${GATEWAY_URL}"
else
    warn "Gateway may still be starting — check /tmp/soulacy-gateway.log"
fi

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}${BOLD}  Soulacy installed successfully! 🎉${NC}"
echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "  GUI:    ${GATEWAY_URL}"
echo "  Config: ${DATA_DIR}/config.yaml"
echo "  Logs:   /tmp/soulacy-gateway.log"
echo ""
echo "  Quick commands:"
echo -e "    ${BLUE}sy agent list${NC}                    List agents"
echo -e "    ${BLUE}sy chat --agent hello-world \"Hi\"${NC}  Chat with an agent"
echo -e "    ${BLUE}sy server status${NC}                 Check gateway status"
echo ""
echo "  To uninstall the LaunchAgent:"
echo "    launchctl unload -w ~/Library/LaunchAgents/com.soulacy.soulacy.plist"
echo ""

# Open the GUI
if command -v open >/dev/null 2>&1; then
    sleep 1
    open "$GATEWAY_URL"
fi
