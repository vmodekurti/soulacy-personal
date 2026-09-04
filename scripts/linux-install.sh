#!/usr/bin/env bash
# linux-install.sh — One-click Soulacy installer for Linux
#
# Supports: Ubuntu 20.04+, Debian 11+, Fedora 38+, RHEL/CentOS 8+, Arch Linux
#
# Run:
#   curl -sSL https://get.soulacy.dev/linux | sudo bash
#   # or from the repo:
#   sudo bash scripts/linux-install.sh
#
# What this does:
#   1. Installs system dependencies (Go, Node, Python, SQLite dev libs)
#   2. Builds GUI + Go binaries (or downloads pre-built release binaries)
#   3. Installs soulacy and sy to /usr/local/bin
#   4. Installs the Python SDK
#   5. Creates the soulacy system user + /etc/soulacy/ + /var/lib/soulacy/
#   6. Registers and starts a systemd service (auto-restart, starts on boot)

set -euo pipefail

INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/soulacy"
DATA_DIR="/var/lib/soulacy"
SERVICE_FILE="/etc/systemd/system/soulacy.service"
SOULACY_USER="soulacy"
REPO="vmodekurti/soulacy-personal"
VERSION="${SOULACY_VERSION:-latest}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'

log()  { echo -e "${GREEN}▶${NC}  $*"; }
warn() { echo -e "${YELLOW}⚠${NC}  $*"; }
err()  { echo -e "${RED}✗${NC}  $*" >&2; exit 1; }
ok()   { echo -e "${GREEN}✓${NC}  $*"; }

[ "$(id -u)" -eq 0 ] || err "This script must be run as root (use sudo)."

echo ""
echo -e "${BLUE}${BOLD}  ╔════════════════════════════════════╗${NC}"
echo -e "${BLUE}${BOLD}  ║     Soulacy — Linux Installer      ║${NC}"
echo -e "${BLUE}${BOLD}  ╚════════════════════════════════════╝${NC}"
echo ""

# ── Detect distro ─────────────────────────────────────────────────────────────
detect_distro() {
    if   [ -f /etc/debian_version ]; then echo "debian"
    elif [ -f /etc/fedora-release ];  then echo "fedora"
    elif [ -f /etc/redhat-release ];  then echo "rhel"
    elif [ -f /etc/arch-release ];    then echo "arch"
    else echo "unknown"; fi
}

DISTRO=$(detect_distro)
log "Detected distro: ${DISTRO}"

# ── Install system dependencies ───────────────────────────────────────────────
install_deps() {
    log "Installing system dependencies..."
    case "$DISTRO" in
        debian)
            apt-get update -qq
            apt-get install -y --no-install-recommends \
                build-essential pkg-config libsqlite3-dev \
                python3 python3-pip curl git ca-certificates
            # Install Go if not present or too old
            if ! command -v go >/dev/null 2>&1 || ! go version | grep -qE 'go1\.(2[2-9]|[3-9][0-9])'; then
                log "Installing Go 1.24..."
                curl -fsSL https://go.dev/dl/go1.24.1.linux-$(dpkg --print-architecture).tar.gz \
                    | tar -C /usr/local -xz
                export PATH="/usr/local/go/bin:$PATH"
                echo 'export PATH="/usr/local/go/bin:$PATH"' >> /etc/profile.d/go.sh
            fi
            # Install Node if not present
            if ! command -v npm >/dev/null 2>&1; then
                log "Installing Node.js 20..."
                curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
                apt-get install -y nodejs
            fi
            ;;
        fedora|rhel)
            dnf install -y \
                gcc make pkg-config sqlite-devel \
                python3 python3-pip curl git ca-certificates
            if ! command -v go >/dev/null 2>&1; then
                dnf install -y golang
            fi
            if ! command -v npm >/dev/null 2>&1; then
                dnf module install -y nodejs:20
            fi
            ;;
        arch)
            pacman -Sy --noconfirm \
                base-devel sqlite python python-pip go nodejs npm curl git
            ;;
        *)
            warn "Unknown distro — assuming deps are installed."
            command -v go  >/dev/null 2>&1 || err "Go not found. Install from https://go.dev/dl/"
            command -v npm >/dev/null 2>&1 || err "Node/npm not found. Install from https://nodejs.org"
            ;;
    esac
    ok "System dependencies installed"
}

# ── Resolve version ───────────────────────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
    log "Resolving latest release..."
    VERSION=$(curl -sf "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/') 2>/dev/null || VERSION="dev"
fi

# ── Build or download binaries ────────────────────────────────────────────────
build_from_source() {
    log "Building from source..."
    install_deps

    SRCDIR="/tmp/soulacy-src"
    rm -rf "$SRCDIR"

    if [ -f "go.mod" ] && grep -q "soulacy" go.mod; then
        # Running from inside the repo
        SRCDIR="$(pwd)"
        log "Building in-place from repo..."
    else
        log "Cloning repository..."
        git clone --depth 1 "https://github.com/${REPO}.git" "$SRCDIR"
        cd "$SRCDIR"
    fi

    export PATH="/usr/local/go/bin:$PATH"
    log "Building GUI + binaries..."
    make all

    cp bin/soulacy "$INSTALL_DIR/soulacy"
    cp bin/sy      "$INSTALL_DIR/sy"
    chmod 755 "$INSTALL_DIR/soulacy" "$INSTALL_DIR/sy"
}

try_download_binaries() {
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        *) return 1 ;;
    esac

    BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
    TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR"' RETURN

    TARBALL="soulacy_${VERSION}_linux_${ARCH}.tar.gz"
    URL="${BASE_URL}/${TARBALL}"
    log "Downloading Soulacy bundle from ${URL}..."
    curl -fsSL -o "${TMPDIR}/${TARBALL}" "$URL" || return 1
    tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"
    [ -f "${TMPDIR}/soulacy" ] || return 1
    [ -f "${TMPDIR}/sy" ] || return 1
    cp "${TMPDIR}/soulacy" "$INSTALL_DIR/soulacy"
    cp "${TMPDIR}/sy" "$INSTALL_DIR/sy"
    chmod 755 "$INSTALL_DIR/soulacy" "$INSTALL_DIR/sy"
    return 0
}

log "Installing Soulacy ${VERSION} binaries..."
try_download_binaries || {
    warn "Pre-built binaries not available — building from source instead"
    build_from_source
}
ok "soulacy and sy installed to ${INSTALL_DIR}"

# ── Python SDK ────────────────────────────────────────────────────────────────
log "Installing Python SDK..."
pip3 install soulacy --quiet 2>/dev/null \
    || warn "Python SDK install failed — run: pip3 install soulacy"
ok "Python SDK installed"

# ── System user ───────────────────────────────────────────────────────────────
log "Creating system user '${SOULACY_USER}'..."
id "$SOULACY_USER" >/dev/null 2>&1 || \
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SOULACY_USER"
ok "User '${SOULACY_USER}' ready"

# ── Directories ───────────────────────────────────────────────────────────────
log "Creating directories..."
mkdir -p \
    "$CONFIG_DIR" \
    "$DATA_DIR/agents" \
    "$DATA_DIR/plugins" \
    "$DATA_DIR/skills" \
    "$DATA_DIR/memory" \
    "$DATA_DIR/logs"
chown -R "$SOULACY_USER:$SOULACY_USER" "$DATA_DIR"
chmod 750 "$CONFIG_DIR" "$DATA_DIR"

# Write default config if none exists
if [ ! -f "${CONFIG_DIR}/config.yaml" ]; then
    cat > "${CONFIG_DIR}/config.yaml" << 'EOF'
# Soulacy configuration — /etc/soulacy/config.yaml
# Full reference: https://docs.soulacy.dev/configuration

server:
  host: "0.0.0.0"
  port: 18789
  gui_enabled: true
  api_key: ""        # ⚠ Set this before exposing to a network

llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "llama3"

log:
  level: info
  format: json

storage:
  backend: sqlite

executor:
  backend: pool
  workers: 4
EOF
    chown root:"$SOULACY_USER" "${CONFIG_DIR}/config.yaml"
    chmod 640 "${CONFIG_DIR}/config.yaml"
    ok "Default config written to ${CONFIG_DIR}/config.yaml"
else
    ok "Existing config preserved at ${CONFIG_DIR}/config.yaml"
fi

# ── systemd service ───────────────────────────────────────────────────────────
log "Installing systemd service..."

# Use embedded service definition if available, otherwise write inline
if [ -f "scripts/soulacy.service" ]; then
    sed "s|__INSTALL_DIR__|${INSTALL_DIR}|g" scripts/soulacy.service > "$SERVICE_FILE"
else
    cat > "$SERVICE_FILE" << EOF
[Unit]
Description=Soulacy — self-hosted agentic framework
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SOULACY_USER}
Group=${SOULACY_USER}
ExecStart=${INSTALL_DIR}/soulacy serve
Restart=on-failure
RestartSec=10s
WorkingDirectory=${DATA_DIR}
Environment=SOULACY_CONFIG_PATH=${CONFIG_DIR}/config.yaml
Environment=HOME=${DATA_DIR}
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=${DATA_DIR} /tmp
PrivateTmp=yes
StandardOutput=journal
StandardError=journal
SyslogIdentifier=soulacy

[Install]
WantedBy=multi-user.target
EOF
fi

systemctl daemon-reload
systemctl enable soulacy
systemctl start soulacy
sleep 2

if systemctl is-active --quiet soulacy; then
    ok "Soulacy service started and enabled"
else
    warn "Service may not have started — check: journalctl -u soulacy -n 50"
fi

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}${BOLD}  Soulacy installed successfully!${NC}"
echo -e "${GREEN}${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "  GUI:    http://$(hostname -I | awk '{print $1}'):18789"
echo "  Config: ${CONFIG_DIR}/config.yaml"
echo ""
echo "  Manage the service:"
echo "    systemctl status  soulacy"
echo "    systemctl restart soulacy"
echo "    journalctl -u soulacy -f"
echo ""
echo "  CLI:"
echo "    sy agent list"
echo "    sy chat --agent hello-world \"Hi\""
echo ""
