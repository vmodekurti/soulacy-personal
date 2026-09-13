#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${SOULACY_UAT_BIN:-$ROOT/bin/soulacy}"
CLI="${SOULACY_UAT_CLI:-$ROOT/bin/sy}"
WORKSPACE="${SOULACY_UAT_WORKSPACE:-$(mktemp -d "${TMPDIR:-/tmp}/soulacy-uat-XXXXXXXX")}"
HOST="${SOULACY_UAT_HOST:-127.0.0.1}"
PORT="${SOULACY_UAT_PORT:-18891}"
API_KEY="${SOULACY_UAT_API_KEY:-sy_uat_clean_runtime}"
MODEL="${SOULACY_UAT_MODEL:-}"
CHAT="${SOULACY_UAT_CHAT:-0}"
STUDIO_BUILD="${SOULACY_UAT_STUDIO_BUILD:-0}"
STUDIO_LIVE="${SOULACY_UAT_STUDIO_LIVE:-0}"
BROWSER_MCP="${SOULACY_UAT_BROWSER_MCP:-0}"
URL="http://${HOST}:${PORT}"

# ── Report mode & auto-generated timestamped report ──────────────────────────
# SOULACY_UAT_MODE is `public` (default; skips every credential-gated block) or
# `full` (allows the live-channel blocks that need real bot tokens). The mode
# is advisory — the live blocks still skip cleanly on their own when the env
# pair is missing, so `make uat-public` on a machine with tokens set still
# skips gracefully.
UAT_MODE="${SOULACY_UAT_MODE:-public}"
if [[ "$UAT_MODE" != "public" && "$UAT_MODE" != "full" ]]; then
  echo "SOULACY_UAT_MODE must be 'public' or 'full', got '$UAT_MODE'" >&2
  exit 2
fi

# Report path: default to .cache/uat-reports/<timestamp>.md so a checkout
# accumulates a review-friendly trail without polluting docs/. Override with
# SOULACY_UAT_REPORT=/absolute/path.md (empty string disables report writing).
UAT_TS="$(date -u +%Y%m%dT%H%M%SZ)"
if [[ -n "${SOULACY_UAT_REPORT+x}" ]]; then
  REPORT_PATH="${SOULACY_UAT_REPORT}"
else
  REPORT_PATH="${ROOT}/.cache/uat-reports/UAT_REPORT_${UAT_TS}.md"
fi
# H2 — heal any corrupted ancestor before mkdir. Some repo states leave a
# stray FILE where a directory should be (touch fallout, a botched install,
# etc.); the actual audit-observed case is $ROOT/.cache existing as a file,
# which makes `mkdir -p .cache/uat-reports` fail with "Not a directory" and
# red-lines the whole clean UAT lane.
#
# The healer walks the target path bottom-up, removing any ancestor that
# exists as a non-directory. Bounded by $ROOT — never touches anything
# outside the repo checkout, so a mistyped path can't nuke a system file.
#
# Portability note: this script runs under macOS's shipped /bin/bash 3.2
# under `set -euo pipefail`. Bash 3.2 treats `${arr[@]}` on an empty array
# as "unbound variable" under nounset, which broke an earlier array-based
# implementation. The current version uses a simple linear walker with a
# single scalar variable — no arrays — so it works identically on 3.2 and
# 4+/5+.
ensure_dir() {
  local d="$1"
  if [[ -z "$d" ]]; then return 0; fi
  local p="$d"
  while :; do
    if [[ -e "$p" && ! -d "$p" ]]; then
      if [[ "$p" == "$ROOT"* ]]; then
        echo "warn: $p exists as a non-directory; removing." >&2
        rm -f "$p"
      else
        echo "error: refusing to heal $p — outside \$ROOT ($ROOT)" >&2
        return 1
      fi
    fi
    local parent
    parent=$(dirname "$p")
    if [[ "$parent" == "$p" || "$parent" == "/" || "$parent" == "." ]]; then
      break
    fi
    p="$parent"
  done
  mkdir -p "$d"
}
if [[ -n "$REPORT_PATH" ]]; then
  ensure_dir "$(dirname "$REPORT_PATH")"
fi

# Tee everything into a live log so the report can quote the failure context
# (last few lines of stdout) even after `set -e` unwinds the script.
UAT_LOG="${WORKSPACE:-/tmp}/uat.log"
ensure_dir "$(dirname "$UAT_LOG")"
exec > >(tee -a "$UAT_LOG") 2>&1

# ── Per-step timing (E1 hardening) ───────────────────────────────────────────
# `step "<name>"` closes the previous section, starts a new one with a fresh
# start timestamp, and appends to a jsonl the report will render as a table.
# `skip_step "<name>" "<reason>"` records a skip without timing. `step_finish`
# closes the last open step at the end. The ERR trap also closes the current
# step as failed with its elapsed time so a fail row lands in the table even
# when `set -e` unwinds mid-section.
STEP_LOG="${WORKSPACE:-/tmp}/steps.jsonl"
: > "$STEP_LOG"
CURR_STEP=""
CURR_STEP_START=""
_step_json_str() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }
_step_write() {
  local name="$1" status="$2" dur="$3" reason="${4:-}"
  local name_esc reason_esc
  name_esc="$(_step_json_str "$name")"
  reason_esc="$(_step_json_str "$reason")"
  printf '{"name":"%s","duration_s":%d,"status":"%s","reason":"%s"}\n' \
    "$name_esc" "$dur" "$status" "$reason_esc" >> "$STEP_LOG"
}
step() {
  if [[ -n "$CURR_STEP" ]]; then
    local now dur; now="$(date +%s)"; dur=$((now - CURR_STEP_START))
    _step_write "$CURR_STEP" "pass" "$dur" ""
  fi
  CURR_STEP="$1"
  CURR_STEP_START="$(date +%s)"
  echo "▶ $CURR_STEP"
}
skip_step() {
  # Close any open step first — a skip mid-section still leaves the previous
  # section's timing intact.
  if [[ -n "$CURR_STEP" ]]; then
    local now dur; now="$(date +%s)"; dur=$((now - CURR_STEP_START))
    _step_write "$CURR_STEP" "pass" "$dur" ""
    CURR_STEP=""; CURR_STEP_START=""
  fi
  _step_write "$1" "skip" "0" "${2:-}"
  echo "SKIP $1: ${2:-}"
}
step_finish() {
  if [[ -n "$CURR_STEP" ]]; then
    local now dur; now="$(date +%s)"; dur=$((now - CURR_STEP_START))
    _step_write "$CURR_STEP" "pass" "$dur" ""
    CURR_STEP=""; CURR_STEP_START=""
  fi
}
UAT_FAIL_LINE=""
UAT_FAIL_CMD=""
# Chain the ERR trap: mark the current step as fail (with its elapsed time)
# before we unwind, so the report table shows the exact step that broke.
trap 'UAT_FAIL_LINE=$LINENO; UAT_FAIL_CMD=$BASH_COMMAND;
      if [[ -n "$CURR_STEP" ]]; then
        _now=$(date +%s); _dur=$((_now - CURR_STEP_START));
        _step_write "$CURR_STEP" "fail" "$_dur" "line $LINENO: $BASH_COMMAND";
        CURR_STEP="";
      fi' ERR

if [[ ! -x "$BIN" ]]; then
  echo "soulacy binary not found at $BIN; run make build first or set SOULACY_UAT_BIN" >&2
  exit 2
fi

ensure_dir "$WORKSPACE"
if [[ -z "$MODEL" ]]; then
  MODEL="$(curl -fsS http://localhost:11434/api/tags 2>/dev/null \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); ms=d.get("models") or []; print((ms[0].get("name") if ms else "") or "llama3.2")' 2>/dev/null \
    || printf 'llama3.2')"
fi
cat > "$WORKSPACE/config.yaml" <<YAML
server:
  host: "${HOST}"
  port: ${PORT}
  api_key: "${API_KEY}"
  gui_enabled: true
llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "${MODEL}"
agent_dirs:
  - "${WORKSPACE}/agents"
log:
  file: "${WORKSPACE}/logs/soulacy.log"
YAML

write_uat_report() {
  local exit_code="$1"
  if [[ -z "$REPORT_PATH" ]]; then
    return 0
  fi
  # Close any still-open step so the last row lands in the table on failure.
  if [[ -n "$CURR_STEP" ]]; then
    local now dur; now="$(date +%s)"; dur=$((now - CURR_STEP_START))
    if [[ "$exit_code" == "0" ]]; then
      _step_write "$CURR_STEP" "pass" "$dur" ""
    else
      _step_write "$CURR_STEP" "fail" "$dur" "line ${UAT_FAIL_LINE:-?}: ${UAT_FAIL_CMD:-unknown}"
    fi
    CURR_STEP=""
  fi
  local status="pass"
  if [[ "$exit_code" != "0" ]]; then
    status="fail"
  fi
  local tail_lines=""
  if [[ -f "$UAT_LOG" ]]; then
    tail_lines="$(tail -n 60 "$UAT_LOG" 2>/dev/null || true)"
  fi
  # Per-step counts + rendered table sourced from STEP_LOG (jsonl).
  local pass_count fail_count skip_count total_secs steps_table
  local counts_raw
  counts_raw="$(python3 - "$STEP_LOG" <<'PY'
import json, sys
p, f, s, total = 0, 0, 0, 0
with open(sys.argv[1]) as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        st = r.get("status", "")
        total += int(r.get("duration_s", 0))
        if st == "pass": p += 1
        elif st == "fail": f += 1
        elif st == "skip": s += 1
print(f"{p}\t{f}\t{s}\t{total}")
PY
  )"
  pass_count="$(echo "$counts_raw" | awk '{print $1}')"
  fail_count="$(echo "$counts_raw" | awk '{print $2}')"
  skip_count="$(echo "$counts_raw" | awk '{print $3}')"
  total_secs="$(echo "$counts_raw" | awk '{print $4}')"
  steps_table="$(python3 - "$STEP_LOG" <<'PY'
import json, sys
rows = []
with open(sys.argv[1]) as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            rows.append(json.loads(line))
        except Exception:
            continue
if not rows:
    print("_(no per-step data captured)_")
else:
    print("| # | Step | Status | Duration | Notes |")
    print("|---|------|--------|----------|-------|")
    icons = {"pass": "✓ pass", "fail": "✗ fail", "skip": "○ skip"}
    for i, r in enumerate(rows, 1):
        name = r.get("name", "").replace("|", "\\|")
        st = icons.get(r.get("status", ""), r.get("status", ""))
        dur = r.get("duration_s", 0)
        dur_txt = f"{dur}s" if dur else "—"
        reason = r.get("reason", "").replace("|", "\\|")
        print(f"| {i} | {name} | {st} | {dur_txt} | {reason} |")
PY
  )"
  # Screenshot gallery: link to whatever `make docs-screenshots` produced.
  # The manifest.json lists every route/name/screenshot so the report can
  # inline the images. Missing manifest → gallery section is omitted.
  local screenshot_section=""
  local screenshot_manifest="$ROOT/docs/assets/screenshots/manifest.json"
  if [[ -f "$screenshot_manifest" ]]; then
    screenshot_section="$(python3 - "$screenshot_manifest" "$ROOT" <<'PY'
import json, os, sys
mf, root = sys.argv[1], sys.argv[2]
try:
    d = json.load(open(mf))
except Exception as e:
    print(f"_(screenshot manifest could not be parsed: {e})_")
    sys.exit(0)
routes = d.get("routes") or []
if not routes:
    print("_(screenshot manifest has no routes)_")
    sys.exit(0)
gen = d.get("generated_at", "unknown time")
print(f"Screenshots captured by `make docs-screenshots` at `{gen}`.")
print("")
print("| Route | Bytes | Text length | Image |")
print("|-------|------:|-----------:|-------|")
for r in routes:
    name = r.get("name", "")
    path = r.get("path", "")
    img  = r.get("screenshot", "")
    rel  = os.path.relpath(os.path.join(root, "docs", "assets", "screenshots", img), start=os.path.dirname(mf))
    # Absolute-from-repo path is easier to eyeball in the .md.
    abs_repo_path = f"docs/assets/screenshots/{img}"
    print(f"| `{name}` (`{path}`) | {r.get('bytes','')} | {r.get('text_length','')} | ![]({abs_repo_path}) |")
PY
    )"
  fi
  {
    echo "# Soulacy Clean-Runtime UAT Report"
    echo
    echo "- Generated: \`$UAT_TS\`"
    echo "- Mode: \`$UAT_MODE\` ($([ "$UAT_MODE" = "full" ] && echo "credential-backed subset enabled" || echo "no credentials required"))"
    echo "- Result: \`$status\`"
    echo "- Workspace: \`$WORKSPACE\`"
    echo "- Gateway: \`$URL\`"
    echo "- Model: \`$MODEL\`"
    echo "- Full log: \`$UAT_LOG\`"
    echo "- Step log: \`$STEP_LOG\`"
    echo "- Pass / Fail / Skip: \`${pass_count:-0}\` / \`${fail_count:-0}\` / \`${skip_count:-0}\` (total ~\`${total_secs:-0}s\`)"
    if [[ "$status" = "fail" ]]; then
      echo
      echo "## Failure"
      echo
      echo "- Line: \`${UAT_FAIL_LINE:-unknown}\`"
      echo "- Command: \`${UAT_FAIL_CMD:-unknown}\`"
      echo "- Remediation hints: consult the last log tail below. Channel-delivery"
      echo "  failures include a delivery-doctor \`category\`+\`reason\`+\`fix\` in the"
      echo "  server response; provider failures include a provider-doctor"
      echo "  \`diagnosis\` block with \`category\`/\`reason\`/\`fix\`."
    fi
    echo
    echo "## Per-step timing"
    echo
    echo "$steps_table"
    if [[ -n "$screenshot_section" ]]; then
      echo
      echo "## Screenshots"
      echo
      echo "$screenshot_section"
    fi
    echo
    echo "## Log tail"
    echo
    echo '```text'
    echo "$tail_lines"
    echo '```'
  } > "$REPORT_PATH"
  echo "UAT report written: $REPORT_PATH"
}

cleanup() {
  local rc=$?
  if [[ -n "${PID:-}" ]]; then
    kill "$PID" >/dev/null 2>&1 || true
    wait "$PID" >/dev/null 2>&1 || true
  fi
  write_uat_report "$rc"
}
trap cleanup EXIT

SOULACY_WORKSPACE="$WORKSPACE" SOULACY_CONFIG_PATH="$WORKSPACE/config.yaml" "$BIN" serve >"$WORKSPACE/server.out" 2>"$WORKSPACE/server.err" &
PID=$!

api() {
  local method="$1"; shift
  local path="$1"; shift
  local data="${1:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" "$URL/api/v1$path" \
      -H "Authorization: Bearer $API_KEY" \
      -H "Content-Type: application/json" \
      --data "$data"
  else
    curl -fsS -X "$method" "$URL/api/v1$path" \
      -H "Authorization: Bearer $API_KEY"
  fi
}

wait_for_gateway() {
  for _ in $(seq 1 60); do
    if curl -fsS -H "Authorization: Bearer $API_KEY" "$URL/api/v1/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  echo "gateway did not start; stdout/stderr follow" >&2
  sed -n '1,120p' "$WORKSPACE/server.out" >&2 || true
  sed -n '1,160p' "$WORKSPACE/server.err" >&2 || true
  return 1
}

json_assert() {
  python3 -c '
import json, sys
expr = sys.argv[1]
doc = json.load(sys.stdin)
scope = {"doc": doc, "len": len, "any": any, "all": all, "isinstance": isinstance, "list": list, "dict": dict, "str": str}
if not eval(expr, {"__builtins__": {}, **scope}, scope):
    raise SystemExit(f"assertion failed: {expr}\n{json.dumps(doc, indent=2)[:2000]}")
' "$@"
}

echo "== clean runtime UAT =="
echo "workspace: $WORKSPACE"
echo "gateway:   $URL"

wait_for_gateway

step "health"
api GET /health | json_assert "doc.get('status') == 'ok'"

step "onboarding status"
api GET /onboarding/status | json_assert "'steps' in doc and len(doc['steps']) >= 3"

step "launch readiness"
api GET /readiness | json_assert "'summary' in doc and 'journey' in doc and 'release' in doc and 'deployment' in doc and len(doc['journey']) >= 6 and all(k in doc['summary'] for k in ['status', 'score', 'providers_ready', 'agents', 'enabled_agents', 'updates_ready', 'deployment_profile']) and all(k in [item.get('key') for item in doc['journey']] for k in ['providers', 'studio', 'agents', 'channels', 'monitor', 'learning', 'deployment'])"

step "gui shell"
INDEX_HTML="$(curl -fsS "$URL/")"
ASSET_PATH="$(printf '%s' "$INDEX_HTML" | python3 -c '
import re, sys
html = sys.stdin.read()
m = re.search(r"/assets/index-[^\"'"'"']+\.js", html)
print(m.group(0) if m else "")
')"
if [[ -z "$ASSET_PATH" ]]; then
  echo "could not locate GUI asset in index.html" >&2
  exit 1
fi
GUI_JS="$(curl -fsS "$URL$ASSET_PATH")"
GUI_BYTES="$(printf '%s' "$GUI_JS" | wc -c | tr -d ' ')"
if (( GUI_BYTES > 250000 )); then
  echo "GUI entry bundle is too large: ${GUI_BYTES} bytes" >&2
  exit 1
fi
for label in "Deployed" "Learning" "Delivery" "Automations" "Runs" "Browser"; do
  if ! grep -Fq "$label" <<<"$GUI_JS"; then
    echo "GUI entry bundle does not contain expected label: $label" >&2
    exit 1
  fi
done
for needle in "Failed to fetch dynamically imported module" "__soulacy_reload" "soulacy:stale-asset-reload"; do
  if ! grep -Fq "$needle" <<<"$GUI_JS"; then
    echo "GUI entry bundle does not contain stale-asset recovery marker: $needle" >&2
    exit 1
  fi
done
DASHBOARD_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/Dashboard-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$DASHBOARD_PATH" ]]; then
  echo "could not locate lazy Dashboard chunk in GUI entry bundle" >&2
  exit 1
fi
DASHBOARD_JS="$(curl -fsS "$URL$DASHBOARD_PATH")"
if ! grep -Fq "Launch Readiness" <<<"$DASHBOARD_JS"; then
  echo "Dashboard chunk does not contain Launch Readiness" >&2
  exit 1
fi
MOBILE_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/Mobile-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$MOBILE_PATH" ]]; then
  echo "could not locate lazy Mobile chunk in GUI entry bundle" >&2
  exit 1
fi
MOBILE_JS="$(curl -fsS "$URL$MOBILE_PATH")"
if ! grep -Fq "Launch Readiness" <<<"$MOBILE_JS"; then
  echo "Mobile chunk does not contain Launch Readiness" >&2
  exit 1
fi
for label in "Recent runs" "Delivery health"; do
  if ! grep -Fq "$label" <<<"$MOBILE_JS"; then
    echo "Mobile chunk does not contain expected operations label: $label" >&2
    exit 1
  fi
done
BROWSER_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/BrowserTrace-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$BROWSER_PATH" ]]; then
  echo "could not locate lazy BrowserTrace chunk in GUI entry bundle" >&2
  exit 1
fi
BROWSER_JS="$(curl -fsS "$URL$BROWSER_PATH")"
if ! grep -Fq "Browser Trace" <<<"$BROWSER_JS"; then
  echo "BrowserTrace chunk does not contain Browser Trace" >&2
  exit 1
fi
for label in "Screenshot Gallery" "Export JSON" "Copy link"; do
  if ! grep -Fq "$label" <<<"$BROWSER_JS"; then
    echo "BrowserTrace chunk does not contain expected trace tool: $label" >&2
    exit 1
  fi
done
SKILLS_PATH="$(printf '%s' "$GUI_JS" | python3 -c '
import re, sys
js = sys.stdin.read()
m = re.search(r"assets/Skills-[^\"'"'"']+\.js", js)
print("/" + m.group(0) if m else "")
')"
if [[ -z "$SKILLS_PATH" ]]; then
  echo "could not locate lazy Skills chunk in GUI entry bundle" >&2
  exit 1
fi
SKILLS_JS="$(curl -fsS "$URL$SKILLS_PATH")"
for label in "Skill sources" "Find skills" "Try direct install" "skills.sh"; do
  if ! grep -Fq "$label" <<<"$SKILLS_JS"; then
    echo "Skills chunk does not contain expected registry guidance: $label" >&2
    exit 1
  fi
done

step "pwa manifest"
MANIFEST_JSON="$(curl -fsS "$URL/manifest.webmanifest")"
printf '%s' "$MANIFEST_JSON" | json_assert "doc.get('start_url') == '/#mobile' and doc.get('display') == 'standalone' and doc.get('id') == '/#mobile' and all(url in [s.get('url') for s in doc.get('shortcuts', [])] for url in ['/#chat', '/#activity', '/#studio', '/#channels'])"
SW_JS="$(curl -fsS "$URL/sw.js")"
for needle in "notificationclick" "/#mobile" "/brand/living-core-blue-v1-192.png" "soulacy-shell-v4-living-core"; do
  if ! grep -Fq "$needle" <<<"$SW_JS"; then
    echo "service worker does not contain expected PWA behavior: $needle" >&2
    exit 1
  fi
done

python3 - "$URL" "$ROOT/gui/public" "$MANIFEST_JSON" <<'PY'
import json, pathlib, struct, sys, urllib.request

base, source, raw_manifest = sys.argv[1:]
manifest = json.loads(raw_manifest)
expected = {f"/brand/living-core-blue-v1-{size}.png": size for size in (192, 512)}
icons = manifest.get("icons", [])
assert {icon.get("src") for icon in icons} == set(expected), "unexpected PWA icon paths"
for icon in icons:
    size = expected[icon["src"]]
    assert icon.get("sizes") == f"{size}x{size}" and icon.get("type") == "image/png", "incorrect icon metadata"
for size in (32, 128, 180, 192, 512):
    path = f"/brand/living-core-blue-v1-{size}.png"
    with urllib.request.urlopen(base + path, timeout=10) as response:
        assert response.headers.get_content_type() == "image/png", f"not a PNG response: {path}"
        data = response.read(2 * 1024 * 1024)
    assert data[:8] == b"\x89PNG\r\n\x1a\n", f"invalid PNG: {path}"
    assert struct.unpack(">II", data[16:24]) == (size, size), f"incorrect PNG dimensions: {path}"
    assert data == (pathlib.Path(source) / path.lstrip("/")).read_bytes(), f"embedded icon differs from approved asset: {path}"
print("approved Living Core assets served correctly")
PY

step "template catalog"
TEMPLATE_JSON="$(api GET /templates)"
printf '%s' "$TEMPLATE_JSON" | json_assert "doc.get('count', 0) > 0"
printf '%s' "$TEMPLATE_JSON" | json_assert "all(name in [t.get('name') for t in doc.get('templates', [])] for name in ['stock-screener', 'research-brief', 'rag-over-docs', 'scheduled-briefing'])"
printf '%s' "$TEMPLATE_JSON" | json_assert "all(t.get('display_name') or t.get('definition', {}).get('name') for t in doc.get('templates', []))"
printf '%s' "$TEMPLATE_JSON" | json_assert "any(t.get('name') == 'stock-screener' and any(item.get('key') == 'search' for item in t.get('setup', [])) for t in doc.get('templates', []))"
TEMPLATE="$(printf '%s' "$TEMPLATE_JSON" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["templates"][0]["name"])')"

step "instantiate template: $TEMPLATE"
api POST "/templates/${TEMPLATE}/instantiate" '{"id":"uat-template-agent"}' >/dev/null
api GET /agents | json_assert "any(a.get('id') == 'uat-template-agent' for a in doc.get('agents', []))"

step "studio run history"
api GET '/studio/run-history?agentId=uat-template-agent' | json_assert "doc.get('agentId') == 'uat-template-agent' and isinstance(doc.get('runs'), list)"

step "browser trace"
api GET '/browser/trace?agent_id=uat-template-agent' | json_assert "doc.get('enabled') is True and doc.get('trace', {}).get('agent_id') == 'uat-template-agent' and isinstance(doc.get('trace', {}).get('steps'), list)"

if [[ "$BROWSER_MCP" == "1" ]]; then
  step "optional browser MCP sidecar"
  SOULACY_BROWSER_MCP_SMOKE=1 python3 "$ROOT/scripts/browser-mcp-smoke.py"
else
  skip_step "optional browser MCP sidecar" "SOULACY_UAT_BROWSER_MCP=0"
fi

if [[ "$STUDIO_BUILD" == "1" || "$STUDIO_LIVE" == "1" ]]; then
  step "optional studio build loop"
  VERIFY=false
  if [[ "$STUDIO_LIVE" == "1" ]]; then
    VERIFY=true
  fi
  STUDIO_BUILD_BODY="$(python3 - "$VERIFY" <<'PY'
import json, sys
verify = sys.argv[1].lower() == "true"
workflow = {
    "name": "UAT Echo Workflow",
    "intent": "When manually triggered, echo the user's message in a short friendly sentence.",
    "trigger": {"type": "manual"},
    "channels": ["http"],
    "flow": {
        "entry": "normalize",
        "output": "normalize",
        "nodes": [
            {
                "id": "normalize",
                "kind": "python",
                "description": "Normalize the incoming manual input and produce a short reply.",
                "input": "{\"message\":\"{{ .trigger.text }}\"}",
                "code": "def run(inputs):\n    msg = inputs.get('message') or ''\n    return {'reply': 'UAT echo: ' + str(msg).strip()}\n",
                "output": "result",
                "x": 0,
                "y": 0,
            }
        ],
        "edges": [{"from": "normalize", "to": "end"}],
    },
}
print(json.dumps({
    "workflow": workflow,
    "intent": workflow["intent"],
    "verify": verify,
}))
PY
)"
  STUDIO_BUILD_JSON="$(api POST /studio/build "$STUDIO_BUILD_BODY")"
  printf '%s' "$STUDIO_BUILD_JSON" | json_assert "doc.get('report', {}).get('ok') is True and isinstance(doc.get('report', {}).get('attempts'), list) and isinstance(doc.get('traceId'), str) and len(doc.get('traceId')) > 0"
  if [[ "$STUDIO_LIVE" == "1" ]]; then
    printf '%s' "$STUDIO_BUILD_JSON" | json_assert "doc.get('report', {}).get('verified') is True"
  fi
  TRACE_ID="$(printf '%s' "$STUDIO_BUILD_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["traceId"])')"
  api GET "/studio/build-trace?id=${TRACE_ID}" | json_assert "doc.get('id') == '${TRACE_ID}' and isinstance(doc.get('events'), list) and len(doc.get('events')) > 0"
fi

step "golden template instantiation"
api POST "/templates/stock-screener/instantiate" '{"id":"uat-stock-screener","cron":"0 7 * * 1-5","output":{"channel":"http","to":"uat-outbox","template":"{reply}"}}' >/dev/null
api POST "/templates/research-brief/instantiate" '{"id":"uat-research-brief"}' >/dev/null
api POST "/templates/rag-over-docs/instantiate" '{"id":"uat-rag-over-docs"}' >/dev/null
api GET /agents | json_assert "all(agent_id in [a.get('id') for a in doc.get('agents', [])] for agent_id in ['uat-stock-screener', 'uat-research-brief', 'uat-rag-over-docs'])"
api POST /agents/validate "$(api GET /agents | python3 -c 'import json,sys; agents=json.load(sys.stdin)["agents"]; print(json.dumps(next(a for a in agents if a.get("id")=="uat-stock-screener")))')" \
  | json_assert "doc.get('valid') is True and doc.get('errors') == 0"

step "queues"
api POST /queues '{"queue":"uat_resources"}' >/dev/null
api POST /queues/items '{"queue":"uat_resources","item":{"kind":"url","url":"https://example.com/uat"}}' >/dev/null
api GET '/queues/items?queue=uat_resources' | json_assert "doc.get('count') == 1"
api POST '/queues/take?queue=uat_resources' | json_assert "doc.get('ok') is True and doc.get('item') is not None"

step "schedule"
api GET /schedule | json_assert "'schedule' in doc"

step "doctor"
if [[ -x "$CLI" ]]; then
  # Point resolveSoulacyBinary() at the UAT-built gateway binary so the
  # install check passes in CI runners where soulacy isn't on PATH /
  # ~/.local/bin / /usr/local/bin / /opt/homebrew/bin. Locally installed
  # setups (Mac Studio, dev laptops) have SOULACY_BIN unset and the resolver's
  # fallback chain finds their real install — this only affects the CI case.
  # Capture the doctor report; on failure, print the failing check names + reasons
  # so CI logs are actionable instead of just showing "doctor found N failing check(s)".
  # Keep stdout (JSON) and stderr (cobra's "Error:" line + Usage) in separate files
  # so the JSON stays parseable, and use raw_decode as a belt-and-braces fallback
  # in case anything else stray lands on stdout.
  _doctor_out="$WORKSPACE/doctor.json"
  _doctor_err="$WORKSPACE/doctor.err"
  if ! SOULACY_WORKSPACE="$WORKSPACE" SOULACY_BIN="$BIN" "$CLI" --gateway "$URL" --api-key "$API_KEY" --json doctor check >"$_doctor_out" 2>"$_doctor_err"; then
    echo "  doctor failed — surfacing check-level detail:"
    python3 - "$_doctor_out" <<'PY' || { echo "  (raw JSON dump follows)"; cat "$_doctor_out"; }
import json, sys
try:
    with open(sys.argv[1]) as f:
        text = f.read()
    rep, _ = json.JSONDecoder().raw_decode(text.lstrip())
except Exception as e:
    print(f"  (could not parse doctor JSON: {e})")
    sys.exit(1)
for c in rep.get("checks", []):
    if c.get("status") in ("fail", "warn"):
        print(f"  [{c.get('status','?')}] {c.get('name','?')}: {c.get('detail','')}")
        if c.get("remedy"):
            print(f"      remedy: {c['remedy']}")
PY
    if [[ -s "$_doctor_err" ]]; then
      echo "  --- doctor stderr ---"
      sed 's/^/  /' "$_doctor_err"
    fi
    exit 1
  fi
  SOULACY_WORKSPACE="$WORKSPACE" SOULACY_BIN="$BIN" "$CLI" --gateway "$URL" --api-key "$API_KEY" launch check >/dev/null
  cat > "$WORKSPACE/release-manifest.json" <<JSON
{"product":"soulacy","version":"99.0.0","artifacts":[{"name":"soulacy-test.tar.gz","os":"uat","arch":"uat","sha256":"abc123","bytes":123}]}
JSON
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" --json update check --manifest "$WORKSPACE/release-manifest.json" --current 1.0.0 \
    | json_assert "doc.get('update_available') is True and doc.get('latest_version') == '99.0.0'"
  RELDIR="$WORKSPACE/update-dry-run"
  mkdir -p "$RELDIR/stage"
  printf '#!/usr/bin/env sh\necho soulacy uat update\n' > "$RELDIR/stage/soulacy"
  printf '#!/usr/bin/env sh\necho sy uat update\n' > "$RELDIR/stage/sy"
  chmod 0755 "$RELDIR/stage/soulacy" "$RELDIR/stage/sy"
  (cd "$RELDIR/stage" && tar -czf "$RELDIR/soulacy-uat.tar.gz" soulacy sy)
  if command -v shasum >/dev/null 2>&1; then
    UAT_SHA="$(shasum -a 256 "$RELDIR/soulacy-uat.tar.gz" | awk '{print $1}')"
  else
    UAT_SHA="$(sha256sum "$RELDIR/soulacy-uat.tar.gz" | awk '{print $1}')"
  fi
  UAT_BYTES="$(wc -c < "$RELDIR/soulacy-uat.tar.gz" | tr -d ' ')"
  UAT_GOOS="$(go env GOOS 2>/dev/null || uname | tr '[:upper:]' '[:lower:]')"
  UAT_GOARCH="$(go env GOARCH 2>/dev/null || uname -m)"
  UAT_ARTIFACT="soulacy_99.0.0_${UAT_GOOS}_${UAT_GOARCH}.tar.gz"
  mv "$RELDIR/soulacy-uat.tar.gz" "$RELDIR/$UAT_ARTIFACT"
  printf '{"verificationMaterial":{}}\n' > "$WORKSPACE/release-manifest-real.json.cosign.bundle"
  printf '{"verificationMaterial":{}}\n' > "$RELDIR/$UAT_ARTIFACT.cosign.bundle"
  # Exercise the updater's Sigstore wiring without requiring network trust roots in UAT.
  mkdir -p "$RELDIR/test-bin"
  cat > "$RELDIR/test-bin/cosign" <<'SH'
#!/usr/bin/env sh
set -eu
[ "$1" = "verify-blob" ]
bundle=""
identity=""
artifact=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --bundle) bundle="$2"; shift 2 ;;
    --certificate-identity) identity="$2"; shift 2 ;;
    --certificate-oidc-issuer) [ "$2" = "https://token.actions.githubusercontent.com" ]; shift 2 ;;
    verify-blob) shift ;;
    *) artifact="$1"; shift ;;
  esac
done
[ "$identity" = "https://github.com/vmodekurti/soulacy-personal/.github/workflows/release.yml@refs/tags/v99.0.0" ]
[ -s "$bundle" ]
[ -s "$artifact" ]
SH
  chmod 0755 "$RELDIR/test-bin/cosign"
  cat > "$WORKSPACE/release-manifest-real.json" <<JSON
{"product":"soulacy","version":"99.0.0","artifacts":[{"name":"$UAT_ARTIFACT","os":"$UAT_GOOS","arch":"$UAT_GOARCH","sha256":"$UAT_SHA","bytes":$UAT_BYTES,"url":"$RELDIR/$UAT_ARTIFACT"}]}
JSON
  PATH="$RELDIR/test-bin:$PATH" SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" --json update install --manifest "$WORKSPACE/release-manifest-real.json" --current 1.0.0 --install-dir "$RELDIR/install" --dry-run \
    | json_assert "doc.get('dry_run') is True and doc.get('installed') is False and doc.get('artifact', {}).get('name') == '$UAT_ARTIFACT'"
else
  api GET /doctor | json_assert "'providers' in doc and 'channels' in doc"
fi


# ── Knowledge base + ASYNC ingestion ────────────────────────────────────────
# Ingestion runs OUT of the request: POST returns 202 + a durable job, and a
# background worker does extract → chunk → embed → store. We always assert the
# async CONTRACT (202 + a queued job + a retrievable job row), which needs no
# embedder. The full round-trip (job reaches `done`, the doc is searchable) is
# additionally asserted when an embedder is actually reachable.
# KB creation itself probes the embedding model, so the whole block needs a
# reachable embedder. Without one this is an ENVIRONMENT gap, not a regression —
# skip loudly instead of failing the run.
KB_CODE="$(curl -sS -o "$WORKSPACE/kb.json" -w '%{http_code}' \
  -X POST "$URL/api/v1/knowledge" -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" --data '{"name":"uat_kb","description":"clean runtime UAT"}' || true)"
if [[ "$KB_CODE" != "200" && "$KB_CODE" != "201" ]]; then
  skip_step "knowledge: kb create" "no reachable embedder — KB creation returned HTTP $KB_CODE (pull an embedding model like nomic-embed-text)"
  KB_AVAILABLE=0
else
  KB_AVAILABLE=1
fi

if [[ "$KB_AVAILABLE" == "1" ]]; then
step "knowledge: kb created"
api GET /knowledge | json_assert "any(k.get('name') == 'uat_kb' for k in doc.get('knowledge_bases', doc.get('kbs', [])))"

step "knowledge: ingest returns 202 + job (async, non-blocking)"
INGEST_CODE="$(curl -sS -o "$WORKSPACE/ingest.json" -w '%{http_code}' \
  -X POST "$URL/api/v1/knowledge/uat_kb/documents" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" \
  --data '{"title":"UAT Doc","mime_type":"text/plain","content":"Soulacy clean runtime UAT document. The refund window is 30 days."}')"
if [[ "$INGEST_CODE" != "202" ]]; then
  echo "FAIL: ingest must be async (202 Accepted), got HTTP $INGEST_CODE" >&2
  cat "$WORKSPACE/ingest.json" >&2 || true
  exit 1
fi
json_assert "doc.get('id') and doc.get('status') in ('queued','running') and doc.get('kb_name') == 'uat_kb'" < "$WORKSPACE/ingest.json"
JOB_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$WORKSPACE/ingest.json")"

step "knowledge: job durable + listable ($JOB_ID)"
api GET "/ingest-jobs/${JOB_ID}" | json_assert "doc.get('id') == '${JOB_ID}'"
api GET '/knowledge/uat_kb/jobs' | json_assert "any(j.get('id') == '${JOB_ID}' for j in doc.get('jobs', []))"

# Full ingestion needs a live embedder. Poll to a terminal state, then decide.
step "knowledge: awaiting worker"
KB_STATUS="queued"
for _ in $(seq 1 60); do
  KB_STATUS="$(api GET "/ingest-jobs/${JOB_ID}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')"
  [[ "$KB_STATUS" == "done" || "$KB_STATUS" == "failed" ]] && break
  sleep 1
done

if [[ "$KB_STATUS" == "done" ]]; then
  step "knowledge: ingested — asserting document + search"
  api GET "/ingest-jobs/${JOB_ID}" | json_assert "doc.get('progress') == 100 and doc.get('doc_id')"
  api GET /knowledge/uat_kb/documents | json_assert "any(d.get('title') == 'UAT Doc' for d in doc.get('documents', []))"
  api POST /knowledge/uat_kb/search '{"query":"refund window","top_k":3}' \
    | json_assert "isinstance(doc.get('hits', doc.get('results', [])), list)"
else
  # A failed job here means no reachable embedder — that's an environment gap,
  # not a product regression. The async contract above already passed, and the
  # failure must be RECORDED with a reason (that itself is the guarantee).
  api GET "/ingest-jobs/${JOB_ID}" | json_assert "doc.get('status') == 'failed' and doc.get('error')"
  skip_step "knowledge: round-trip" "no reachable embedder (job recorded a failure reason, as designed)"
fi

fi   # end KB_AVAILABLE

# ── Channel delivery doctor (no secrets required) ────────────────────────────
# Diagnose must return a STRUCTURED, plain-language verdict rather than a raw
# error, even for a channel that was never configured.
step "channels: diagnose an unconfigured channel"
DIAG="$(curl -sS -X POST "$URL/api/v1/channels/telegram/diagnose" \
  -H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json" --data '{"dry":true}' || true)"
printf '%s' "$DIAG" | json_assert "'diagnosis' in doc and doc['diagnosis'].get('category') and doc['diagnosis'].get('reason')"

# ── Live channel delivery (secret-gated; skips cleanly) ─────────────────────
# In `public` mode we skip this whole block unconditionally so CI never needs
# any channel tokens. In `full` mode we run each block when its env pair is
# present (each still skips cleanly if you only have some tokens).
if [[ "$UAT_MODE" != "full" ]]; then
  skip_step "channels: live telegram delivery" "SOULACY_UAT_MODE=$UAT_MODE (set to full and provide TELEGRAM_BOT_TOKEN+TELEGRAM_TEST_CHAT_ID)"
elif [[ -n "${TELEGRAM_BOT_TOKEN:-}" && -n "${TELEGRAM_TEST_CHAT_ID:-}" ]]; then
  step "channels: live telegram delivery"
  api PATCH /channels/telegram "$(python3 -c '
import json, os
print(json.dumps({"settings": {
  "token": os.environ["TELEGRAM_BOT_TOKEN"],
  "default_output_to": os.environ["TELEGRAM_TEST_CHAT_ID"],
  "outbound_only": True,
}}))')" >/dev/null
  api POST /channels/telegram/enable >/dev/null || true
  api POST /channels/telegram/test '{"text":"clean runtime UAT"}' \
    | json_assert "doc.get('ok') is True"
  api POST /channels/telegram/diagnose '{}' \
    | json_assert "doc['diagnosis'].get('ok') is True"
else
  skip_step "channels: live telegram delivery" "TELEGRAM_BOT_TOKEN or TELEGRAM_TEST_CHAT_ID unset"
fi

if [[ "$UAT_MODE" = "full" && -n "${SLACK_BOT_TOKEN:-}" && -n "${SLACK_TEST_CHANNEL_ID:-}" ]]; then
  step "channels: live slack delivery"
  api POST /channels/slack/test "$(python3 -c '
import json, os
print(json.dumps({"to": os.environ["SLACK_TEST_CHANNEL_ID"], "text": "clean runtime UAT"}))')" \
    | json_assert "doc.get('ok') is True"
else
  skip_step "channels: live slack delivery" "SOULACY_UAT_MODE=$UAT_MODE and/or SLACK_BOT_TOKEN + SLACK_TEST_CHANNEL_ID unset"
fi

if [[ "$UAT_MODE" = "full" && -n "${DISCORD_BOT_TOKEN:-}" && -n "${DISCORD_TEST_CHANNEL_ID:-}" ]]; then
  step "channels: live discord delivery"
  api POST /channels/discord/test "$(python3 -c '
import json, os
print(json.dumps({"to": os.environ["DISCORD_TEST_CHANNEL_ID"], "text": "clean runtime UAT"}))')" \
    | json_assert "doc.get('ok') is True"
else
  skip_step "channels: live discord delivery" "SOULACY_UAT_MODE=$UAT_MODE and/or DISCORD_BOT_TOKEN + DISCORD_TEST_CHANNEL_ID unset"
fi

step "support bundle"
BUNDLE="$WORKSPACE/support-bundle.zip"
curl -fsS "$URL/api/v1/support/bundle" -H "Authorization: Bearer $API_KEY" -o "$BUNDLE"
python3 - "$BUNDLE" <<'PY'
import sys, zipfile
path = sys.argv[1]
with zipfile.ZipFile(path) as zf:
    names = set(zf.namelist())
    required = {"manifest.json", "doctor.json", "readiness.json"}
    missing = sorted(required - names)
    if missing:
        raise SystemExit(f"support bundle missing {missing}; got {sorted(names)[:40]}")
    for name in required:
        if not zf.read(name).strip():
            raise SystemExit(f"support bundle entry {name} is empty")
PY

if [[ "$CHAT" == "1" ]]; then
  step "optional chat"
  SOULACY_WORKSPACE="$WORKSPACE" "$CLI" --gateway "$URL" --api-key "$API_KEY" chat --agent uat-template-agent "Reply with exactly: clean runtime ok" \
    | tee "$WORKSPACE/chat.out"
  grep -qi "clean runtime ok" "$WORKSPACE/chat.out"
fi


# ── First-run bootstrap (virgin install) ────────────────────────────────────
# The main run above pre-writes config.yaml, so it never exercises the "dumb
# install" path. Here we start the gateway against a COMPLETELY EMPTY workspace
# and assert EnsureBootstrap does its job: writes a default config.yaml, mints an
# API key, and comes up authenticated on that key. This is the very first thing a
# new user hits, so it must never regress.
step "first-run bootstrap (virgin workspace)"
FR_WS="$(mktemp -d "${TMPDIR:-/tmp}/soulacy-uat-firstrun-XXXXXXXX")"
FR_PORT=$((PORT + 1))
FR_URL="http://${HOST}:${FR_PORT}"

SOULACY_WORKSPACE="$FR_WS" SOULACY_SERVER_HOST="$HOST" SOULACY_SERVER_PORT="$FR_PORT" \
  "$BIN" serve >"$FR_WS/server.out" 2>"$FR_WS/server.err" &
FR_PID=$!

fr_cleanup() {
  if [[ -n "${FR_PID:-}" ]]; then
    kill "$FR_PID" >/dev/null 2>&1 || true
    wait "$FR_PID" >/dev/null 2>&1 || true
  fi
}

# The config it generates is the source of the key we must authenticate with.
FR_CONFIG=""
for _ in $(seq 1 60); do
  FR_CONFIG="$(find "$FR_WS" -name config.yaml -maxdepth 3 2>/dev/null | head -1)"
  [[ -n "$FR_CONFIG" ]] && break
  sleep 0.5
done
if [[ -z "$FR_CONFIG" ]]; then
  echo "FAIL first-run: no config.yaml was bootstrapped under $FR_WS" >&2
  sed -n '1,60p' "$FR_WS/server.err" >&2 || true
  fr_cleanup; exit 1
fi
echo "  bootstrapped config: $FR_CONFIG"

# A key must have been generated and persisted (it gates every API call).
FR_KEY="$(python3 -c '
import re, sys
text = open(sys.argv[1]).read()
m = re.search(r"^\s*api_key:\s*\"?([^\"\n]+)\"?", text, re.M)
print((m.group(1).strip() if m else ""))' "$FR_CONFIG")"
if [[ -z "$FR_KEY" ]]; then
  echo "FAIL first-run: bootstrapped config has no generated api_key" >&2
  fr_cleanup; exit 1
fi

# And the gateway must actually be up and accept exactly that key.
FR_OK=0
for _ in $(seq 1 60); do
  if curl -fsS -H "Authorization: Bearer $FR_KEY" "$FR_URL/api/v1/health" >/dev/null 2>&1; then
    FR_OK=1; break
  fi
  sleep 0.5
done
if [[ "$FR_OK" != "1" ]]; then
  echo "FAIL first-run: gateway did not come up authenticated on the generated key" >&2
  sed -n '1,60p' "$FR_WS/server.err" >&2 || true
  fr_cleanup; exit 1
fi

# An unauthenticated call must still be rejected (the key isn't decorative).
FR_CODE="$(curl -sS -o /dev/null -w '%{http_code}' "$FR_URL/api/v1/agents" || true)"
if [[ "$FR_CODE" != "401" && "$FR_CODE" != "403" ]]; then
  echo "FAIL first-run: unauthenticated request returned $FR_CODE (expected 401/403)" >&2
  fr_cleanup; exit 1
fi

fr_cleanup
rm -rf "$FR_WS"
echo "  first-run OK: config + generated key + authenticated gateway"

step_finish
echo "PASS clean runtime UAT"
