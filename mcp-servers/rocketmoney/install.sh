#!/bin/bash
# Install Rocket Money MCP server dependencies
set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pip3 install -r "$SCRIPT_DIR/requirements.txt" --break-system-packages 2>/dev/null || \
    pip3 install -r "$SCRIPT_DIR/requirements.txt"
echo "✓ Rocket Money MCP server dependencies installed"
echo ""
echo "The server auto-extracts cookies from Chrome (you must be logged in to"
echo "app.rocketmoney.com). To override, set ROCKET_MONEY_COOKIE env var in"
echo "your Soulacy config.yaml under mcp.servers.rocketmoney.env."
