#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "== Go: product smoke packages =="
go test \
  ./internal/agentvalidate \
  ./internal/introspect \
  ./internal/pkgregistry \
  ./internal/reasoning \
  ./internal/runtime \
  ./internal/templates \
  ./internal/studio \
  ./internal/gateway \
  ./internal/regression \
  ./internal/channels \
  ./internal/scheduler \
  ./internal/learning

echo "== Go: binaries =="
go build ./cmd/soulacy ./cmd/sy

if command -v npm >/dev/null 2>&1; then
  echo "== GUI: focused tests =="
  npm --prefix gui test -- --run \
    src/lib/api.auth.test.js \
    src/lib/channelguides.test.js \
    src/lib/studio/blockmeta.test.js \
    src/lib/studio/configfields.test.js

  echo "== GUI: production build =="
  npm --prefix gui run build
else
  echo "npm not found; skipped GUI regression checks"
fi
