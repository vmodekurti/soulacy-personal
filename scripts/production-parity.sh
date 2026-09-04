#!/usr/bin/env bash
set -u -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

STAMP="$(date -u +"%Y%m%dT%H%M%SZ")"
DEFAULT_REPORT_BASE="$ROOT/.cache/production-parity"
if [[ -e "$ROOT/.cache" && ! -d "$ROOT/.cache" ]]; then
  DEFAULT_REPORT_BASE="$ROOT/tmp/production-parity"
fi
REPORT_DIR="${SOULACY_PARITY_REPORT_DIR:-$DEFAULT_REPORT_BASE/$STAMP}"
JSON_REPORT="$REPORT_DIR/report.json"
MD_REPORT="$REPORT_DIR/report.md"
mkdir -p "$REPORT_DIR"

PASS=0
FAIL=0
SKIP=0

json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.stdin.read())[1:-1])'
}

append_json_check() {
  local name="$1" status="$2" required="$3" elapsed="$4" log="$5" detail="$6"
  local comma=""
  if [[ -s "$REPORT_DIR/checks.jsonl" ]]; then
    comma=","
  fi
  {
    printf '%s\n' "$comma"
    printf '{"name":"%s","status":"%s","required":%s,"elapsed_seconds":%s,"log":"%s","detail":"%s"}' \
      "$(printf '%s' "$name" | json_escape)" \
      "$status" \
      "$required" \
      "$elapsed" \
      "$(printf '%s' "$log" | json_escape)" \
      "$(printf '%s' "$detail" | json_escape)"
  } >> "$REPORT_DIR/checks.jsonl"
}

write_reports() {
  local overall="pass"
  if [[ "$FAIL" -gt 0 ]]; then
    overall="fail"
  fi
  {
    printf '{"generated_at":"%s","overall":"%s","summary":{"pass":%d,"fail":%d,"skip":%d},"checks":[\n' "$STAMP" "$overall" "$PASS" "$FAIL" "$SKIP"
    cat "$REPORT_DIR/checks.jsonl" 2>/dev/null || true
    printf '\n]}\n'
  } > "$JSON_REPORT"

  {
    echo "# Soulacy Production Parity Report"
    echo
    echo "- Generated: $STAMP"
    echo "- Overall: $overall"
    echo "- Passed: $PASS"
    echo "- Failed: $FAIL"
    echo "- Skipped: $SKIP"
    echo
    echo "| Check | Status | Required | Seconds | Log | Detail |"
    echo "|---|---:|---:|---:|---|---|"
    python3 - "$JSON_REPORT" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
for c in data.get("checks", []):
    detail = (c.get("detail") or "").replace("|", "\\|").replace("\n", " ")[:220]
    print(f"| {c['name']} | {c['status']} | {str(c['required']).lower()} | {c['elapsed_seconds']} | `{c['log']}` | {detail} |")
PY
  } > "$MD_REPORT"
}

run_check() {
  local name="$1" required="$2" cmd="$3"
  local slug log start end rc elapsed detail status
  slug="$(printf '%s' "$name" | tr '[:upper:] /:' '[:lower:]---' | tr -cd 'a-z0-9._-')"
  log="$REPORT_DIR/${slug}.log"
  start="$(date +%s)"
  echo "== $name =="
  bash -lc "$cmd" >"$log" 2>&1
  rc=$?
  end="$(date +%s)"
  elapsed="$((end - start))"
  if [[ "$rc" -eq 0 ]]; then
    status="pass"
    detail="ok"
    PASS=$((PASS + 1))
  else
    detail="$(tail -40 "$log" | python3 -c 'import sys; print("  ".join(line.rstrip() for line in sys.stdin))')"
    if [[ "$required" == "true" ]]; then
      status="fail"
      FAIL=$((FAIL + 1))
    else
      status="skip"
      SKIP=$((SKIP + 1))
    fi
  fi
  append_json_check "$name" "$status" "$required" "$elapsed" "$log" "$detail"
  echo "$status ($elapsed s) — log: $log"
  return 0
}

skip_check() {
  local name="$1" detail="$2"
  echo "== $name =="
  echo "skip — $detail"
  SKIP=$((SKIP + 1))
  append_json_check "$name" "skip" "false" "0" "" "$detail"
}

python_for_sdk() {
  if [[ -n "${SOULACY_PARITY_PYTHON:-}" ]]; then
    printf '%s\n' "$SOULACY_PARITY_PYTHON"
    return 0
  fi
  for py in python3.13 python3.12 python3.11 python3.10 python3; do
    if command -v "$py" >/dev/null 2>&1 && "$py" - <<'PY' >/dev/null 2>&1
import sys
raise SystemExit(0 if sys.version_info >= (3, 10) else 1)
PY
    then
      command -v "$py"
      return 0
    fi
  done
  return 1
}

run_required_suite() {
  if [[ "${SOULACY_PARITY_QUICK:-0}" == "1" ]]; then
    run_check "go vet" true "go vet ./..."
    run_check "targeted go tests" true "go test ./internal/runtime ./internal/gateway ./internal/channels ./cmd/sy -timeout 120s"
    run_check "production regression smoke" true "make regression"
    run_check "clean runtime UAT" true "make uat"
    run_check "release smoke" true "make release-smoke"
    return
  fi
  run_check "go vet" true "go vet ./..."
  run_check "golangci-lint" true "which golangci-lint >/dev/null 2>&1 || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$(go env GOPATH)/bin; golangci-lint run"
  run_check "full go tests" true "go test ./... -timeout 120s"
  run_check "full GUI tests" true "npm --prefix gui test -- --run"
  run_check "production regression smoke" true "make regression"
  run_check "clean runtime UAT" true "make uat"
  run_check "release smoke" true "make release-smoke"
  run_check "race detector" true "CGO_ENABLED=1 go test -race -timeout 120s ./..."
}

run_toolchain_security() {
  local toolchain="${SOULACY_PARITY_GOTOOLCHAIN:-go1.26.6}"
  run_check "govulncheck ($toolchain)" true "command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest; GOTOOLCHAIN=$toolchain \$(go env GOPATH)/bin/govulncheck ./..."
}

run_sdk_docs() {
  local py
  if py="$(python_for_sdk)"; then
    local venv="$REPORT_DIR/python-sdk-venv"
    run_check "python SDK import" true "\"$py\" -m venv \"$venv\" && \"$venv/bin/python\" -m pip install -e sdk/python --quiet && \"$venv/bin/python\" -c 'import soulacy; print(\"SDK ok\")'"
    run_check "python SDK tests" false "cd sdk/python && \"$venv/bin/python\" -m pip install pytest --quiet && \"$venv/bin/python\" -m pytest"
  else
    skip_check "python SDK" "No Python >= 3.10 found. Set SOULACY_PARITY_PYTHON to run this check."
  fi
  run_check "docs strict build" true "command -v mkdocs >/dev/null 2>&1 || python3 -m pip install mkdocs-material --quiet; mkdocs build --strict"
}

run_live_optional() {
  if [[ "${SOULACY_PARITY_LIVE_CHANNELS:-0}" == "1" ]]; then
    run_check "live channel golden smoke" true "make channel-golden-smoke"
  else
    skip_check "live channel golden smoke" "Set SOULACY_PARITY_LIVE_CHANNELS=1 plus SOULACY_GOLDEN_* destinations to certify Telegram/Slack/Discord delivery."
  fi

  if [[ "${SOULACY_PARITY_BROWSER_MCP:-0}" == "1" ]]; then
    run_check "browser MCP sidecar smoke" true "SOULACY_BROWSER_MCP_SMOKE=1 make browser-mcp-smoke"
  else
    skip_check "browser MCP sidecar smoke" "Set SOULACY_PARITY_BROWSER_MCP=1 to run the browser sidecar smoke."
  fi

  if [[ "${SOULACY_PARITY_BROWSER_RENDER:-0}" == "1" ]]; then
    run_check "browser render smoke" true "node scripts/browser-render-smoke.mjs"
  else
    skip_check "browser render smoke" "Set SOULACY_PARITY_BROWSER_RENDER=1 and install Playwright to screenshot/check every main GUI route."
  fi

  if [[ "${SOULACY_PARITY_DOCS_SCREENSHOTS:-0}" == "1" ]]; then
    run_check "docs screenshot refresh" true "make docs-screenshots"
  else
    skip_check "docs screenshot refresh" "Set SOULACY_PARITY_DOCS_SCREENSHOTS=1 to refresh docs/assets/screenshots from the production GUI bundle."
  fi

  if [[ "${SOULACY_PARITY_STUDIO_LIVE:-0}" == "1" ]]; then
    run_check "Studio build/live UAT" true "SOULACY_UAT_PORT=\${SOULACY_UAT_PORT:-18893} SOULACY_UAT_STUDIO_BUILD=1 SOULACY_UAT_STUDIO_LIVE=1 bash scripts/uat-clean-runtime.sh"
  else
    skip_check "Studio build/live UAT" "Set SOULACY_PARITY_STUDIO_LIVE=1 to run the optional Studio build-and-live workflow check."
  fi
}

run_install_verify() {
  run_check "install fresh build" true "make install"
  run_check "installed CLI version" true "command -v soulacy && soulacy --version"
}

main() {
  echo "Soulacy production parity automation"
  echo "report dir: $REPORT_DIR"
  : > "$REPORT_DIR/checks.jsonl"

  run_required_suite
  run_toolchain_security
  run_sdk_docs
  run_live_optional
  run_install_verify

  write_reports
  echo
  echo "Report: $MD_REPORT"
  echo "JSON:   $JSON_REPORT"

  if [[ "$FAIL" -gt 0 ]]; then
    echo "Production parity failed: $FAIL failing required check(s)." >&2
    exit 1
  fi
  echo "Production parity passed with $SKIP optional check(s) skipped."
}

main "$@"
