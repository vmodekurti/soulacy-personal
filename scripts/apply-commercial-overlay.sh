#!/usr/bin/env bash
set -euo pipefail

base="${1:-}"
policy="${2:-dependencies/commercial-overlay.txt}"
if [[ -z "$base" ]]; then
  echo "usage: scripts/apply-commercial-overlay.sh <commercial-base-ref> [policy]" >&2
  exit 2
fi
git rev-parse --verify "${base}^{commit}" >/dev/null
[[ -f "$policy" ]] || { echo "commercial overlay policy is missing: $policy" >&2; exit 1; }

while read -r action path extra; do
  [[ -z "${action:-}" || "$action" == \#* ]] && continue
  [[ -z "${extra:-}" ]] || { echo "invalid commercial overlay entry: $action $path $extra" >&2; exit 1; }
  case "$action" in
    preserve)
      git cat-file -e "${base}:${path}" 2>/dev/null || {
        echo "commercial overlay path is absent from $base: $path" >&2
        exit 1
      }
      git checkout "$base" -- "$path"
      git add "$path"
      ;;
    delete)
      git rm -f --ignore-unmatch "$path"
      ;;
    *)
      echo "unknown commercial overlay action '$action' for $path" >&2
      exit 1
      ;;
  esac
done < "$policy"

unmerged="$(git diff --name-only --diff-filter=U)"
if [[ -n "$unmerged" ]]; then
  printf '%s\n' "$unmerged" >&2
  echo "Personal merge has conflicts outside the declared Commercial overlay" >&2
  exit 1
fi

echo "commercial overlay: applied from $base"
