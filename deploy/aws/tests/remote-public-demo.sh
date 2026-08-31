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
[[ "$*" == *"image inspect"*"{{.Id}}"* ]] && { printf 'sha256:%064d\n' 0; exit 0; }
exit 0
SH
chmod +x "$TMP_DIR/bin/docker"
cat > "$TMP_DIR/bin/chown" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$TMP_DIR/bin/chown"

SETTINGS='{"enabled":true,"workspace_id":"ws_test123","membership_ttl":"24h","draft_ttl":"24h","max_active_members":100,"allowed_providers":["nvidia"],"allowed_models":["nvidia/test"],"allowed_tools":["web_search","mcp__demo-decision-lab__weighted_decision_matrix"],"allowed_skills":["evidence-brief","decision-matrix","chart-storytelling"],"allowed_mcp_servers":["demo-decision-lab"],"backend":"redis","redis_url":"redis://127.0.0.1:6379","per_user_rpm":20,"per_user_tokens_day":50000}'
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
test -f "$AGENT_ROOT/demo-decision-analyst/SOUL.yaml"
grep -q 'soulacy.public_demo: "true"' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'provider: "nvidia"' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'model: "nvidia/test"' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'completion_criteria:' "$AGENT_ROOT/demo-workflow-guide/SOUL.yaml"
grep -q 'builtins: \["web_search"\]' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'skills: \["evidence-brief"\]' "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
grep -q 'mcp_servers: \["demo-decision-lab"\]' "$AGENT_ROOT/demo-decision-analyst/SOUL.yaml"
grep -q 'mcp_tools: \["mcp__demo-decision-lab__weighted_decision_matrix"\]' "$AGENT_ROOT/demo-decision-analyst/SOUL.yaml"

SKILL_ROOT="$TMP_DIR/var/lib/soulacy/.soulacy/soulspace/workspaces/ws_test123/skills"
test -f "$SKILL_ROOT/evidence-brief/SKILL.md"
test -f "$SKILL_ROOT/decision-matrix/SKILL.md"
test -f "$SKILL_ROOT/chart-storytelling/SKILL.md"
grep -q '^name: decision-matrix$' "$SKILL_ROOT/decision-matrix/SKILL.md"

MCP_DB="$TMP_DIR/var/lib/soulacy/.soulacy/soulspace/workspaces/ws_test123/data/workspace-mcp-servers.db"
test "$(python3 - "$MCP_DB" <<'PY'
import sqlite3, sys
connection = sqlite3.connect(sys.argv[1])
print(connection.execute("SELECT count(*) FROM workspace_mcp_servers WHERE workspace_id='ws_test123' AND id='demo-decision-lab' AND transport='container' AND container_network='none' AND container_workspace='none'").fetchone()[0])
PY
)" = 1

# Exercise the exact command and arguments stored for the seeded server. The
# production executor wraps these arguments in the pinned, networkless image;
# this test runs the same stdio program directly to verify MCP negotiation,
# discovery, and a real tool result.
python3 - "$MCP_DB" <<'PY'
import json
import sqlite3
import subprocess
import sys

connection = sqlite3.connect(sys.argv[1])
raw_args = connection.execute(
    "SELECT args FROM workspace_mcp_servers WHERE workspace_id=? AND id=?",
    ("ws_test123", "demo-decision-lab"),
).fetchone()
assert raw_args, "seeded MCP server was not found"
args = json.loads(raw_args[0])
process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

def request(request_id, method, params=None):
    payload = {"jsonrpc": "2.0", "id": request_id, "method": method}
    if params is not None:
        payload["params"] = params
    process.stdin.write(json.dumps(payload) + "\n")
    process.stdin.flush()
    line = process.stdout.readline()
    assert line, "seeded MCP server exited without a response: " + process.stderr.read()
    response = json.loads(line)
    assert response["id"] == request_id and "error" not in response, response
    return response["result"]

initialize = request(1, "initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "test", "version": "1"}})
assert initialize["serverInfo"]["name"] == "Soulacy Demo Decision Lab"
tools = request(2, "tools/list")
assert [tool["name"] for tool in tools["tools"]] == ["weighted_decision_matrix"]
result = request(3, "tools/call", {"name": "weighted_decision_matrix", "arguments": {
    "criteria": [{"name": "quality", "weight": 2}, {"name": "cost", "weight": 1}],
    "options": [{"name": "Option A", "scores": {"quality": 9, "cost": 4}}, {"name": "Option B", "scores": {"quality": 6, "cost": 8}}],
}})
content = json.loads(result["content"][0]["text"])
assert content["ranking"][0]["name"] == "Option A", content
assert content["normalized_weights"] == {"quality": 0.6667, "cost": 0.3333}, content
process.terminate()
process.wait(timeout=5)
PY

PATH="$TMP_DIR/bin:$PATH" SOULACY_PUBLIC_DEMO_ROOT="$TMP_DIR" \
  "$AWS_DIR/remote-public-demo.sh" disable >/dev/null
grep -A1 '^public_demo:$' "$CONFIG" | grep -q 'enabled: false'
test ! -e "$AGENT_ROOT/demo-research-explorer/SOUL.yaml"
test ! -e "$AGENT_ROOT/demo-data-storyteller/SOUL.yaml"
test ! -e "$AGENT_ROOT/demo-workflow-guide/SOUL.yaml"
test ! -e "$AGENT_ROOT/demo-decision-analyst/SOUL.yaml"
test ! -e "$SKILL_ROOT/evidence-brief/SKILL.md"
test ! -e "$SKILL_ROOT/decision-matrix/SKILL.md"
test ! -e "$SKILL_ROOT/chart-storytelling/SKILL.md"
test "$(python3 - "$MCP_DB" <<'PY'
import sqlite3, sys
connection = sqlite3.connect(sys.argv[1])
print(connection.execute("SELECT count(*) FROM workspace_mcp_servers WHERE workspace_id='ws_test123' AND id='demo-decision-lab'").fetchone()[0])
PY
)" = 0
grep -q '  daily_budget_usd: 12' "$CONFIG"
printf 'remote-public-demo tests passed\n'
