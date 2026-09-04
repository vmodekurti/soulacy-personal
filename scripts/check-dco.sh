#!/usr/bin/env bash
set -euo pipefail

base="${1:-}"
head="${2:-HEAD}"
if [[ -z "$base" ]]; then
  echo "usage: scripts/check-dco.sh <base-sha> [head-sha]" >&2
  exit 2
fi

unsigned=()
while IFS= read -r commit; do
  [[ -z "$commit" ]] && continue
  if ! git show -s --format=%B "$commit" | grep -Eiq '^Signed-off-by: .+ <[^>]+>$'; then
    unsigned+=("$commit")
  fi
done < <(git rev-list --no-merges "$base..$head")

if (( ${#unsigned[@]} )); then
  echo "DCO sign-off missing from:" >&2
  for commit in "${unsigned[@]}"; do
    git show -s --format='  %h %s' "$commit" >&2
  done
  echo "Sign commits with: git commit -s" >&2
  exit 1
fi

echo "DCO sign-off: ok"
