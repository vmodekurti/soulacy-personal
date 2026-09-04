#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

manifest="dependencies/editions.json"
base_file=".personal-base"

fail() {
  echo "edition dependencies: $*" >&2
  exit 1
}

command -v jq >/dev/null 2>&1 || fail "jq is required"
[[ -f "$manifest" ]] || fail "$manifest is missing"
[[ -f "$base_file" ]] || fail "$base_file is missing"
jq -e . "$manifest" >/dev/null || fail "$manifest is not valid JSON"

base="$(tr -d '[:space:]' < "$base_file")"
revision="$(jq -r '.personal.revision // empty' "$manifest")"
[[ "$revision" == "$base" ]] || fail "manifest Personal revision does not match .personal-base"
[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || fail "Personal revision must be a full commit SHA"

jq -e '
  .schema_version == 1 and
  .personal.repository == "https://github.com/vmodekurti/soulacy-personal.git" and
  .personal.branch == "main" and
  .personal.update_policy == "reviewed_pull_request" and
  .personal.automatic_merge == false and
  .editions.personal.depends_on == [] and
  .editions.teams.depends_on == ["personal"] and
  .editions.scale.depends_on == ["personal", "teams"] and
  (.editions.teams.compatibility_commands | index("make edition-boundary")) != null and
  (.editions.teams.compatibility_commands | index("make deployment-modes-test")) != null and
  (.editions.scale.compatibility_commands | index("make edition-boundary")) != null and
  (.editions.scale.compatibility_commands | index("make deployment-modes-test")) != null
' "$manifest" >/dev/null || fail "edition graph or update policy is invalid"

while IFS= read -r contract_path; do
  [[ -e "$contract_path" ]] || fail "declared contract path is missing: $contract_path"
done < <(jq -r '.contracts[].path' "$manifest")

echo "edition dependencies: Personal $revision -> Teams -> Scale"
