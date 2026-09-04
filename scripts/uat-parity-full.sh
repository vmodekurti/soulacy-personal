#!/usr/bin/env bash
# uat-parity-full.sh — the "everything on" production-parity harness.
#
# Wraps `make production-parity` with all six opt-in checks enabled:
#
#   - SOULACY_PARITY_LIVE_CHANNELS   → live Telegram/Slack/Discord delivery
#   - SOULACY_PARITY_BROWSER_MCP     → Playwright MCP sidecar smoke
#   - SOULACY_PARITY_BROWSER_RENDER  → Playwright screenshots + GUI check
#   - SOULACY_PARITY_DOCS_SCREENSHOTS → refresh docs/assets/screenshots
#   - SOULACY_PARITY_STUDIO_LIVE     → Studio build-and-live workflow check
#   - plus the E2 credential-backed smoke via scripts/uat-credential-smoke.sh
#
# Sources scripts/.env.uat (see scripts/.env.uat.example) so operator secrets
# stay out of the repo. NEVER wired into CI — this is the pre-tag operator
# harness described in docs/RELEASE_CHECKLIST.md step 3.
#
# Usage:
#   bash scripts/uat-parity-full.sh
#   ENV_UAT=/path/to/.env.uat bash scripts/uat-parity-full.sh
#   SKIP_CREDENTIAL_SMOKE=1 bash scripts/uat-parity-full.sh    # parity only, skip E2

set -u -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# ── Locate and source .env.uat ──────────────────────────────────────────────
ENV_UAT_PATH="${ENV_UAT:-}"
if [[ -z "$ENV_UAT_PATH" ]]; then
  if [[ -f "$ROOT/.env.uat" ]]; then
    ENV_UAT_PATH="$ROOT/.env.uat"
  elif [[ -f "$ROOT/scripts/.env.uat" ]]; then
    ENV_UAT_PATH="$ROOT/scripts/.env.uat"
  fi
fi

if [[ -n "$ENV_UAT_PATH" && -f "$ENV_UAT_PATH" ]]; then
  echo "== sourcing $ENV_UAT_PATH =="
  set -a
  # shellcheck disable=SC1090
  source "$ENV_UAT_PATH"
  set +a
else
  echo "== no .env.uat found — using process env only =="
  echo "   (expected \$ROOT/.env.uat, \$ROOT/scripts/.env.uat, or \$ENV_UAT)"
fi

# ── Turn on every opt-in parity flag ────────────────────────────────────────
export SOULACY_PARITY_LIVE_CHANNELS="${SOULACY_PARITY_LIVE_CHANNELS:-1}"
export SOULACY_PARITY_BROWSER_MCP="${SOULACY_PARITY_BROWSER_MCP:-1}"
export SOULACY_PARITY_BROWSER_RENDER="${SOULACY_PARITY_BROWSER_RENDER:-1}"
export SOULACY_PARITY_DOCS_SCREENSHOTS="${SOULACY_PARITY_DOCS_SCREENSHOTS:-1}"
export SOULACY_PARITY_STUDIO_LIVE="${SOULACY_PARITY_STUDIO_LIVE:-1}"

# ── Preflight report on what will actually run ──────────────────────────────
echo ""
echo "== opt-in flag state =="
printf "  %-40s %s\n" "SOULACY_PARITY_LIVE_CHANNELS"    "$SOULACY_PARITY_LIVE_CHANNELS"
printf "  %-40s %s\n" "SOULACY_PARITY_BROWSER_MCP"      "$SOULACY_PARITY_BROWSER_MCP"
printf "  %-40s %s\n" "SOULACY_PARITY_BROWSER_RENDER"   "$SOULACY_PARITY_BROWSER_RENDER"
printf "  %-40s %s\n" "SOULACY_PARITY_DOCS_SCREENSHOTS" "$SOULACY_PARITY_DOCS_SCREENSHOTS"
printf "  %-40s %s\n" "SOULACY_PARITY_STUDIO_LIVE"      "$SOULACY_PARITY_STUDIO_LIVE"
echo ""
echo "== golden channel destinations (blanks will skip that channel) =="
printf "  %-40s %s\n" "SOULACY_GOLDEN_TELEGRAM_TO"      "${SOULACY_GOLDEN_TELEGRAM_TO:-<unset>}"
printf "  %-40s %s\n" "SOULACY_GOLDEN_SLACK_CHANNEL"    "${SOULACY_GOLDEN_SLACK_CHANNEL:-<unset>}"
printf "  %-40s %s\n" "SOULACY_GOLDEN_DISCORD_CHANNEL"  "${SOULACY_GOLDEN_DISCORD_CHANNEL:-<unset>}"
echo ""
echo "== provider keys (masked; blank means the credential-smoke will skip it) =="
mask() { local v="${1:-}"; if [[ -z "$v" ]]; then echo "<unset>"; else echo "${v:0:6}…${v: -4} (${#v} chars)"; fi; }
printf "  %-40s %s\n" "OPENAI_API_KEY"                 "$(mask "${OPENAI_API_KEY:-}")"
printf "  %-40s %s\n" "ANTHROPIC_API_KEY"              "$(mask "${ANTHROPIC_API_KEY:-}")"
printf "  %-40s %s\n" "GOOGLE_API_KEY"                 "$(mask "${GOOGLE_API_KEY:-}")"
printf "  %-40s %s\n" "GROQ_API_KEY"                   "$(mask "${GROQ_API_KEY:-}")"
echo ""

# ── Playwright preflight ────────────────────────────────────────────────────
if [[ "$SOULACY_PARITY_BROWSER_RENDER" == "1" ]] || [[ "$SOULACY_PARITY_DOCS_SCREENSHOTS" == "1" ]]; then
  if ! command -v npx >/dev/null 2>&1; then
    echo "!! npx not found — install Node so Playwright can run" >&2
    echo "   (unset SOULACY_PARITY_BROWSER_RENDER and SOULACY_PARITY_DOCS_SCREENSHOTS to skip)" >&2
    exit 1
  fi
  if ! npx --yes -q playwright --version >/dev/null 2>&1; then
    echo "!! Playwright not installed — run: npx playwright install" >&2
    exit 1
  fi
fi

# ── Run the required-13 + opt-in-6 parity harness ───────────────────────────
echo "== bash scripts/production-parity.sh (all opt-ins on) =="
if ! bash scripts/production-parity.sh; then
  echo ""
  echo "!! production-parity failed — see report above." >&2
  echo "   Do NOT tag until this is green." >&2
  exit 1
fi

# ── Run the E2 credential-backed smoke on top ───────────────────────────────
if [[ "${SKIP_CREDENTIAL_SMOKE:-0}" == "1" ]]; then
  echo ""
  echo "== SKIP_CREDENTIAL_SMOKE=1 — skipping E2 credential smoke =="
else
  echo ""
  echo "== bash scripts/uat-credential-smoke.sh =="
  if ! ENV_UAT="$ENV_UAT_PATH" bash scripts/uat-credential-smoke.sh; then
    echo ""
    echo "!! credential smoke failed — see report at .cache/uat-reports/" >&2
    echo "   A live-endpoint failure is a legitimate 'not ready to tag' signal." >&2
    exit 1
  fi
fi

echo ""
echo "== full parity + credential smoke complete =="
echo ""
echo "Review the reports before tagging:"
echo "  - production-parity: tmp/production-parity/<STAMP>/report.md"
echo "  - credential smoke:  .cache/uat-reports/CRED_SMOKE_<UTC>.md"
echo ""
echo "If both are clean, proceed with docs/RELEASE_CHECKLIST.md step 4."
