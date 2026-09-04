#!/usr/bin/env bash
set -euo pipefail

gitleaks_bin="${1:-gitleaks}"
base_sha="${2:-}"
head_sha="${3:-HEAD}"

if [[ ! -x "$gitleaks_bin" ]] && ! command -v "$gitleaks_bin" >/dev/null 2>&1; then
  echo "gitleaks executable not found: $gitleaks_bin" >&2
  exit 2
fi

if ! git cat-file -e "${head_sha}^{commit}" 2>/dev/null; then
  echo "gitleaks head is not a commit: $head_sha" >&2
  exit 2
fi

if [[ -n "$base_sha" ]] &&
   [[ ! "$base_sha" =~ ^0+$ ]] &&
   git cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
  log_opts="${base_sha}..${head_sha}"
else
  # A new branch has no usable before SHA. Scan its tip rather than every
  # historical commit reachable from it.
  log_opts="-1 ${head_sha}"
fi

echo "Scanning commits selected by: git log $log_opts"
exec "$gitleaks_bin" git . \
  --redact \
  --verbose \
  --log-opts="$log_opts"
