#!/bin/bash
# Bootstrap: resolve auth cookie, install deps if needed, then run the MCP server.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Use python3.11+ (mcp requires >= 3.10)
PYTHON=$(which python3.11 2>/dev/null || which python3.12 2>/dev/null || which python3.13 2>/dev/null || which python3)

# ── Cookie resolution ─────────────────────────────────────────────────────────
# Priority order (first non-empty value wins):
#   1. ROCKET_MONEY_COOKIE env var (set directly)
#   2. rocket_money_cookie env var (Viper lowercases YAML keys)
#   3. ~/.soulacy/secrets/rocket_money_cookie file (never touched by Soulacy)
#   4. config.yaml env block (last resort — Soulacy may overwrite this)
SECRETS_FILE="$HOME/.soulacy/secrets/rocket_money_cookie"

if [ -z "$ROCKET_MONEY_COOKIE" ]; then
    if [ -n "$rocket_money_cookie" ]; then
        # Viper lowercased variant
        export ROCKET_MONEY_COOKIE="$rocket_money_cookie"
    elif [ -f "$SECRETS_FILE" ] && [ -s "$SECRETS_FILE" ]; then
        # Dedicated secrets file — preferred durable store
        export ROCKET_MONEY_COOKIE="$(cat "$SECRETS_FILE")"
    else
        # Last resort: read from config.yaml (may be wiped by Soulacy on MCP reconnect)
        _COOKIE=$("$PYTHON" - <<'PYEOF' 2>/dev/null
import yaml, os
try:
    cfg = yaml.safe_load(open(os.path.expanduser('~/.soulacy/config.yaml')))
    env = cfg.get('mcp',{}).get('servers',{}).get('rocketmoney',{}).get('env',{})
    cookie = env.get('ROCKET_MONEY_COOKIE') or env.get('rocket_money_cookie') or ''
    print(cookie, end='')
except Exception:
    pass
PYEOF
)
        if [ -n "$_COOKIE" ]; then
            export ROCKET_MONEY_COOKIE="$_COOKIE"
        fi
    fi
fi

# ── Dep check (browser_cookie3 excluded — its import triggers macOS Keychain) ─
if ! "$PYTHON" -c "import mcp, httpx" 2>/dev/null; then
    echo "[rocketmoney-mcp] Installing dependencies..." >&2
    "$PYTHON" -m pip install --quiet --break-system-packages mcp httpx browser-cookie3 >&2 || \
    "$PYTHON" -m pip install --quiet mcp httpx browser-cookie3 >&2 || true
    echo "[rocketmoney-mcp] Install done." >&2
fi

exec "$PYTHON" "$SCRIPT_DIR/server_core.py"
