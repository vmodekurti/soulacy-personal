#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="${SOULACY_RELEASE_SMOKE_PREFIX:-$(mktemp -d "${TMPDIR:-/tmp}/soulacy-release-smoke-XXXXXXXX")}"
BIN_DIR="$PREFIX/bin"

mkdir -p "$BIN_DIR"

if [[ ! -x "$ROOT/bin/soulacy" || ! -x "$ROOT/bin/sy" ]]; then
  echo "release smoke requires built binaries; run make build first" >&2
  exit 2
fi

cp "$ROOT/bin/soulacy" "$BIN_DIR/soulacy"
cp "$ROOT/bin/sy" "$BIN_DIR/sy"
chmod +x "$BIN_DIR/soulacy" "$BIN_DIR/sy"

export PATH="$BIN_DIR:$PATH"

if [[ "$(command -v soulacy)" != "$BIN_DIR/soulacy" ]]; then
  echo "release smoke failed: soulacy does not resolve from $BIN_DIR" >&2
  command -v soulacy >&2 || true
  exit 1
fi
if [[ "$(command -v sy)" != "$BIN_DIR/sy" ]]; then
  echo "release smoke failed: sy does not resolve from $BIN_DIR" >&2
  command -v sy >&2 || true
  exit 1
fi

SOULACY_UAT_BIN="$BIN_DIR/soulacy" \
SOULACY_UAT_CLI="$BIN_DIR/sy" \
SOULACY_UAT_PORT="${SOULACY_RELEASE_SMOKE_PORT:-18892}" \
  bash "$ROOT/scripts/uat-clean-runtime.sh"

echo "PASS release smoke using prefix $PREFIX"
