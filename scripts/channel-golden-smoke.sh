#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${SOULACY_GOLDEN_BASE_URL:-${SOULACY_BASE_URL:-http://127.0.0.1:18789/api/v1}}"
API_KEY="${SOULACY_GOLDEN_API_KEY:-${SOULACY_API_KEY:-}}"

WANT_ANY=0
for ch in telegram slack discord; do
  upper="$(printf '%s' "$ch" | tr '[:lower:]' '[:upper:]')"
  enabled="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}:-}\"")"
  adapter="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}_ADAPTER:-}\"")"
  to="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}_TO:-}\"")"
  if [[ -n "$enabled" || -n "$adapter" || -n "$to" ]]; then
    WANT_ANY=1
    break
  fi
done

if [[ "$WANT_ANY" == "0" ]]; then
  for ch in telegram slack discord; do
    upper="$(printf '%s' "$ch" | tr '[:lower:]' '[:upper:]')"
    echo "skip $ch: set SOULACY_GOLDEN_${upper}=1 or SOULACY_GOLDEN_${upper}_TO/ADAPTER to run"
  done
  echo "No channel golden smoke targets selected."
  echo "Example: SOULACY_API_KEY=... SOULACY_GOLDEN_TELEGRAM_TO=123456 make channel-golden-smoke"
  exit 0
fi

if [[ -z "$API_KEY" ]]; then
  echo "SOULACY_API_KEY or SOULACY_GOLDEN_API_KEY is required for channel golden smoke." >&2
  exit 2
fi

AUTH=(-H "Authorization: Bearer ${API_KEY}" -H "Content-Type: application/json")
RUN_ANY=0
FAILURES=0

json_body() {
  CHANNEL="$1" ADAPTER="$2" TO="$3" TEXT="$4" DRY="$5" python3 - <<'PY'
import json, os

body = {
    "adapter_id": os.environ["ADAPTER"],
    "text": os.environ["TEXT"],
    "dry": os.environ["DRY"].lower() == "true",
}
if os.environ["TO"]:
    body["to"] = os.environ["TO"]
print(json.dumps(body))
PY
}

post_json() {
  local path="$1"
  local body="$2"
  curl -fsS "${AUTH[@]}" -X POST "${BASE_URL%/}${path}" --data "$body"
}

diagnosis_ok() {
  python3 - <<'PY'
import json, sys

data = json.load(sys.stdin)
diag = data.get("diagnosis") or {}
if diag.get("ok") is True or diag.get("category") in ("ok", "ready"):
    sys.exit(0)
print(json.dumps(data, indent=2))
sys.exit(1)
PY
}

run_channel() {
  local channel="$1"
  local upper
  upper="$(printf '%s' "$channel" | tr '[:lower:]' '[:upper:]')"

  local enabled adapter to text
  enabled="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}:-}\"")"
  adapter="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}_ADAPTER:-$channel}\"")"
  to="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}_TO:-}\"")"
  text="$(eval "printf '%s' \"\${SOULACY_GOLDEN_${upper}_TEXT:-Soulacy golden channel smoke for $channel}\"")"

  if [[ -z "$enabled" && -z "$to" && "$adapter" == "$channel" ]]; then
    echo "skip $channel: set SOULACY_GOLDEN_${upper}=1 or SOULACY_GOLDEN_${upper}_TO/ADAPTER to run"
    return 0
  fi

  RUN_ANY=1
  echo "== $channel dry diagnosis ($adapter) =="
  local body
  body="$(json_body "$channel" "$adapter" "$to" "$text" true)"
  if ! post_json "/channels/$channel/diagnose" "$body" | diagnosis_ok; then
    echo "FAIL $channel dry diagnosis" >&2
    FAILURES=$((FAILURES + 1))
    return 0
  fi

  if [[ "${SOULACY_GOLDEN_LIVE_SEND:-1}" == "0" ]]; then
    echo "skip $channel live send: SOULACY_GOLDEN_LIVE_SEND=0"
    return 0
  fi

  echo "== $channel live diagnosis/send ($adapter) =="
  body="$(json_body "$channel" "$adapter" "$to" "$text" false)"
  if ! post_json "/channels/$channel/diagnose" "$body" | diagnosis_ok; then
    echo "FAIL $channel live send" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

for ch in telegram slack discord; do
  run_channel "$ch"
done

if [[ "$FAILURES" -gt 0 ]]; then
  echo "$FAILURES channel golden smoke check(s) failed." >&2
  exit 1
fi

echo "All selected channel golden smoke checks passed."
