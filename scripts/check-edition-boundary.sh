#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  echo "edition boundary: $*" >&2
  exit 1
}

# The reusable Go contract must remain importable by an external commercial
# module. Any dependency on Soulacy internals would make that impossible.
if rg -n 'github.com/soulacy/soulacy/(internal|commercial)/' pkg/edition; then
  fail "pkg/edition imports implementation code"
fi

# Browser contracts are equally dependency-inverted: only the distribution
# composition root may select a commercial implementation.
if rg -n "from ['\"]\.\./(pages|editions|distribution)/" gui/src/lib/edition.js; then
  fail "the GUI edition contract imports an implementation"
fi

commercial_importers="$(rg -l "editions/commercial\.js" gui/src || true)"
if [[ "$commercial_importers" != "gui/src/distribution/edition.js" ]]; then
  printf '%s\n' "$commercial_importers" >&2
  fail "commercial GUI editions must be imported only by the distribution composition root"
fi

# Capability consumers must not grow a second, string-based edition system.
if rg -n "\[['\"]team['\"], ['\"]scale['\"]\]\.includes|deploymentMode[^\n]*(===|==)[^\n]*['\"](?:team|scale)['\"]" \
  gui/src --glob '!**/*.test.js' --glob '!editions/commercial.js'; then
  fail "GUI code branches on edition names; use editionHas(capability)"
fi

echo "edition boundary: ok"
