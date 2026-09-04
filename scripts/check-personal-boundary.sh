#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

for forbidden_path in \
  internal/entitlements \
  internal/tenancy \
  gui/src/pages/Members.svelte \
  gui/src/pages/PlatformAdmin.svelte \
  gui/src/pages/WorkspaceAdmin.svelte \
  gui/src/pages/WorkspaceProviders.svelte
do
  if [[ -e "$forbidden_path" ]]; then
    echo "Personal boundary: commercial path present: $forbidden_path" >&2
    exit 1
  fi
done

if command -v rg >/dev/null 2>&1; then
  matches=$(rg -n 'DeploymentMode(Team|Scale)|deployment\.mode[[:space:]]*=[[:space:]]*"(team|scale)"' \
    --glob '*.go' --glob '*.js' --glob '*.svelte' . || true)
else
  matches=$(grep -RInE 'DeploymentMode(Team|Scale)|deployment\.mode[[:space:]]*=[[:space:]]*"(team|scale)"' \
    --include='*.go' --include='*.js' --include='*.svelte' . || true)
fi
if [[ -n "$matches" ]]; then
  printf '%s\n' "$matches" >&2
  echo "Personal boundary: commercial deployment logic present" >&2
  exit 1
fi

echo "Personal boundary: ok"
