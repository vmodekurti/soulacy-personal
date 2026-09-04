#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() {
  echo "edition boundary: $*" >&2
  exit 1
}

search_tree() {
  local pattern=$1
  shift
  if command -v rg >/dev/null 2>&1; then
    rg -n "$pattern" "$@"
  else
    grep -RInE "$pattern" "$@"
  fi
}

list_matching_files() {
  local pattern=$1
  shift
  if command -v rg >/dev/null 2>&1; then
    rg -l "$pattern" "$@"
  else
    grep -RlE "$pattern" "$@"
  fi
}

# The reusable Go contract must remain importable by an external commercial
# module. Any dependency on Soulacy internals would make that impossible.
if search_tree 'github.com/soulacy/soulacy/(internal|commercial)/' pkg/edition; then
  fail "pkg/edition imports implementation code"
fi

# Browser contracts are equally dependency-inverted: only the distribution
# composition root may select a commercial implementation.
if search_tree "from ['\"]\.\./(pages|editions|distribution)/" gui/src/lib/edition.js; then
  fail "the GUI edition contract imports an implementation"
fi

commercial_importers="$(list_matching_files "editions/commercial\.js" gui/src || true)"
if [[ "$commercial_importers" != "gui/src/distribution/edition.js" ]]; then
  printf '%s\n' "$commercial_importers" >&2
  fail "commercial GUI editions must be imported only by the distribution composition root"
fi

# Capability consumers must not grow a second, string-based edition system.
if find gui/src -type f \( -name '*.js' -o -name '*.svelte' \) \
  ! -name '*.test.js' ! -path '*/editions/commercial.js' -print0 \
  | xargs -0 grep -nE "\[['\"]team['\"], ['\"]scale['\"]\]\.includes|deploymentMode.*(===|==).*['\"](team|scale)['\"]"; then
  fail "GUI code branches on edition names; use editionHas(capability)"
fi

echo "edition boundary: ok"
