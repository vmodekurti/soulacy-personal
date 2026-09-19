#!/usr/bin/env bash
# Soulacy remote access — reach your gateway from your phone anywhere, privately.
#
#   One-liner (piped from curl):
#     curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/remote.sh | bash
#
#   Local checkout:
#     ./remote.sh
#
# What it does, and why this way:
#   Your gateway listens on 127.0.0.1 (localhost) and home networks sit behind
#   NAT, so a phone off your Wi-Fi cannot reach it. This wires up Tailscale — a
#   private, encrypted (WireGuard) mesh VPN — and publishes the gateway INTO your
#   tailnet with `tailscale serve`:
#     1. Installs Tailscale if it is missing.
#     2. Signs this machine into your tailnet (opens a browser once).
#     3. `tailscale serve` proxies your tailnet to the gateway on localhost.
#        The gateway stays bound to 127.0.0.1 — nothing is opened on your LAN or
#        the public internet, and no router port-forwarding is needed. Only your
#        own devices, signed into the same tailnet, can reach it.
#     4. Prints the address + API key to pair your iPhone with.
#
# On your phone (once): install Tailscale, sign into the SAME account, then open
# Soulacy → Settings → add gateway, and paste the address + API key shown below.
#
# Undo:  tailscale serve --http=<port> off     (stop publishing)
#        sudo tailscale down                    (leave the tailnet)
#
# Environment overrides:
#   SOULACY_PORT=18789     gateway port to publish (default 18789)
#   SOULACY_NONINTERACTIVE=1  fail instead of prompting (for CI/automation)

set -euo pipefail

# ── Output helpers ───────────────────────────────────────────────────────────
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; NC='\033[0m'
ok()   { printf "${GREEN}✓${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}⚠${NC}  %s\n" "$*"; }
err()  { printf "${RED}✗${NC} %s\n" "$*" >&2; }
hdr()  { printf "\n${BOLD}%s${NC}\n" "$*"; }
die()  { err "$*"; exit 1; }

PORT="${SOULACY_PORT:-18789}"
OS="$(uname -s)"

# ── 0. Is the gateway actually up on this machine? ───────────────────────────
hdr "Step 1: Check the local gateway"
if curl -fsS --max-time 4 "http://127.0.0.1:${PORT}/api/v1/health" >/dev/null 2>&1; then
  ok "Gateway is responding on 127.0.0.1:${PORT}"
else
  warn "No gateway answered on 127.0.0.1:${PORT}."
  warn "Start it first (e.g. 'soulacy serve', or 'sy daemon start'), then re-run this."
  # Not fatal: publishing still works once the gateway comes up.
fi

# ── 1. Install Tailscale if missing ──────────────────────────────────────────
hdr "Step 2: Tailscale"
if command -v tailscale >/dev/null 2>&1; then
  ok "Tailscale is already installed ($(tailscale version 2>/dev/null | head -1))"
else
  warn "Tailscale is not installed. Installing…"
  case "$OS" in
    Darwin)
      if command -v brew >/dev/null 2>&1; then
        brew install tailscale
        # The Homebrew formula ships the CLI + daemon but does not start it; the
        # system daemon needs one sudo step. (The Mac App Store app is an
        # alternative that manages its own daemon.)
        if ! tailscale status >/dev/null 2>&1; then
          warn "Starting the Tailscale system daemon (needs sudo, once)…"
          sudo tailscaled install-system-daemon || die "Could not start tailscaled. Install the Tailscale app from https://tailscale.com/download and re-run."
        fi
      else
        die "Homebrew not found. Install Tailscale from https://tailscale.com/download (or 'brew install tailscale'), then re-run."
      fi
      ;;
    Linux)
      curl -fsSL https://tailscale.com/install.sh | sh || die "Tailscale install failed. See https://tailscale.com/download"
      ;;
    *)
      die "Unsupported OS '$OS'. This script supports macOS and Linux."
      ;;
  esac
  ok "Tailscale installed"
fi

# ── 2. Bring this machine onto the tailnet ───────────────────────────────────
hdr "Step 3: Sign in to your tailnet"
backend_state() { tailscale status --json 2>/dev/null | sed -n 's/.*"BackendState"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1; }
if [ "$(backend_state)" = "Running" ]; then
  ok "Already signed in to Tailscale"
else
  if [ "${SOULACY_NONINTERACTIVE:-0}" = "1" ]; then
    die "Not signed into Tailscale and SOULACY_NONINTERACTIVE=1. Run 'tailscale up' first."
  fi
  warn "A browser will open to sign this machine into your tailnet."
  # sudo is needed on Linux (and on macOS with the system daemon) to change state.
  if [ "$OS" = "Linux" ]; then sudo tailscale up || die "'tailscale up' failed"; else tailscale up || sudo tailscale up || die "'tailscale up' failed"; fi
  [ "$(backend_state)" = "Running" ] || die "Tailscale did not reach the Running state."
  ok "Signed in"
fi

# ── 3. Publish the gateway into the tailnet ──────────────────────────────────
hdr "Step 4: Publish the gateway to your tailnet"
# `tailscale serve --http=<port>` proxies tailnet:<port> -> localhost:<port>,
# reachable only by your own tailnet devices over the encrypted WireGuard mesh.
# --bg keeps it running after this script exits.
if tailscale serve --bg --http="${PORT}" "http://127.0.0.1:${PORT}" 2>/tmp/soulacy-serve.err; then
  ok "Publishing tailnet port ${PORT} → gateway on localhost"
else
  # Older CLIs use a different `serve` grammar; surface the real error rather
  # than guessing, so the operator can act on it.
  err "'tailscale serve' failed:"
  sed 's/^/    /' /tmp/soulacy-serve.err >&2 || true
  die "Update Tailscale (brew upgrade tailscale) and re-run, or see 'tailscale serve --help'."
fi

# ── 4. Work out the address + key to pair with ───────────────────────────────
DNSNAME="$(tailscale status --json 2>/dev/null | sed -n 's/.*"DNSName"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
DNSNAME="${DNSNAME%.}" # strip trailing dot
TSIP="$(tailscale ip -4 2>/dev/null | head -1)"
[ -n "$DNSNAME" ] && ADDR="http://${DNSNAME}:${PORT}" || ADDR="http://${TSIP}:${PORT}"

# The gateway key is the `sy_`-prefixed api_key in config.yaml (provider keys
# have other formats, so match on the sy_ prefix rather than position, and
# accept it quoted or unquoted since the gateway rewrites the file both ways).
API_KEY=""
for f in \
  "$HOME/.soulacy/soulspace/config.yaml" \
  "$HOME/.soulacy/config.yaml" \
  "$HOME/Library/Application Support/soulacy/config.yaml"; do
  if [ -f "$f" ]; then
    API_KEY="$(awk '/^[[:space:]]*api_key:[[:space:]]*"?sy_/{ sub(/^[[:space:]]*api_key:[[:space:]]*"?/,""); sub(/".*$/,""); print; exit }' "$f")"
    [ -n "$API_KEY" ] && break
  fi
done

# ── 5. Tell the operator exactly what to do on the phone ─────────────────────
hdr "Done — your gateway is reachable over Tailscale"
printf "  Address:  ${BOLD}%s${NC}\n" "$ADDR"
[ -n "$TSIP" ] && printf "  (or:      ${BOLD}http://%s:%s${NC})\n" "$TSIP" "$PORT"
if [ -n "$API_KEY" ]; then
  printf "  API key:  ${BOLD}%s${NC}\n" "$API_KEY"
else
  printf "  API key:  see 'server.api_key' in your config.yaml, or the Soulacy GUI.\n"
fi
# A QR of the address is a convenience for typing it on the phone.
if command -v qrencode >/dev/null 2>&1; then
  printf "\n  Scan to fill the address:\n"
  qrencode -t ANSIUTF8 "$ADDR" | sed 's/^/    /'
fi
cat <<EOF

On your iPhone (one time):
  1. Install Tailscale from the App Store and sign into the SAME account.
  2. Open Soulacy → Settings → add a gateway.
  3. Enter the Address above and the API key, then connect.

It now works from anywhere your phone has internet — no Wi-Fi, ports, or domain
needed. Traffic is encrypted end to end by Tailscale.

To stop publishing later:  tailscale serve --http=${PORT} off
To leave the tailnet:       sudo tailscale down
EOF
