#!/usr/bin/env bash
set -euo pipefail

AWS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
mkdir -p "$TMP_DIR/var/lib/soulacy" "$TMP_DIR/bin"
cat > "$TMP_DIR/var/lib/soulacy/config.yaml" <<'YAML'
server:
  port: 1947
costs:
  enforcement_mode: soft
  daily_budget_usd: 12
runtime:
  default_max_turns: 20
YAML
cat > "$TMP_DIR/bin/docker" <<'SH'
#!/usr/bin/env bash
[[ "${1:-}" == exec ]] && { echo PONG; exit 0; }
[[ "$*" == *"--entrypoint id"*" -u redis"* ]] && { echo 999; exit 0; }
[[ "$*" == *"--entrypoint id"*" -g redis"* ]] && { echo 999; exit 0; }
exit 0
SH
chmod +x "$TMP_DIR/bin/docker"
cat > "$TMP_DIR/bin/chown" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$TMP_DIR/bin/chown"

SETTINGS='{"enabled":true,"workspace_id":"ws_test123","membership_ttl":"24h","draft_ttl":"24h","max_active_members":100,"allowed_providers":["nvidia"],"allowed_models":["nvidia/test"],"allowed_tools":["web_search"],"backend":"redis","redis_url":"redis://127.0.0.1:6379","per_user_rpm":20,"per_user_tokens_day":50000}'
SETTINGS_JSON_BASE64="$(printf '%s' "$SETTINGS" | base64 | tr -d '\n')" \
LOCAL_REDIS=true PATH="$TMP_DIR/bin:$PATH" SOULACY_PUBLIC_DEMO_ROOT="$TMP_DIR" \
  "$AWS_DIR/remote-public-demo.sh" enable >/dev/null

CONFIG="$TMP_DIR/var/lib/soulacy/config.yaml"
grep -q '^public_demo:$' "$CONFIG"
grep -q '  workspace_id: "ws_test123"' "$CONFIG"
grep -q '^rate_limit:$' "$CONFIG"
grep -q '  redis_url: "redis://127.0.0.1:6379"' "$CONFIG"
grep -q '  enforcement_mode: hard' "$CONFIG"
grep -q '  daily_budget_usd: 12' "$CONFIG"
grep -q '^runtime:$' "$CONFIG"

AGENT_ROOT="$TMP_DIR/var/lib/soulacy/.soulacy/soulspace/workspaces/ws_test123/agents"
test -f "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
test -f "$AGENT_ROOT/demo-data-storyteller/SOUL.yaml"
test -f "$AGENT_ROOT/demo-workflow-guide/SOUL.yaml"
grep -q 'soulacy.public_demo: "true"' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'provider: "nvidia"' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'model: "nvidia/test"' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'completion_criteria:' "$AGENT_ROOT/demo-workflow-guide/SOUL.yaml"
grep -q 'builtins: \["web_search"\]' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"

PATH="$TMP_DIR/bin:$PATH" SOULACY_PUBLIC_DEMO_ROOT="$TMP_DIR" \
  "$AWS_DIR/remote-public-demo.sh" disable >/dev/null
grep -A1 '^public_demo:$' "$CONFIG" | grep -q 'enabled: false'
test ! -e "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
test ! -e "$AGENT_ROOT/demo-data-storyteller/SOUL.yaml"
test ! -e "$AGENT_ROOT/demo-workflow-guide/SOUL.yaml"
grep -q '  daily_budget_usd: 12' "$CONFIG"
printf 'remote-public-demo tests passed\n'
