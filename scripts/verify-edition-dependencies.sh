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

while IFS= read -r commercial_path; do
  [[ -e "$commercial_path" ]] || fail "declared commercial module is missing: $commercial_path"
  if git cat-file -e "${base}:${commercial_path}" 2>/dev/null; then
    fail "commercial module is present in the pinned Personal source: $commercial_path"
  fi
done < <(jq -r '.commercial_modules[][]' "$manifest")

[[ -f LICENSE-COMMERCIAL ]] || fail "commercial license notice is missing"
[[ -x scripts/apply-commercial-overlay.sh ]] || fail "commercial overlay applicator is missing or not executable"
[[ -f dependencies/commercial-overlay.txt ]] || fail "commercial overlay policy is missing"
while read -r action overlay_path extra; do
  [[ -z "${action:-}" || "$action" == \#* ]] && continue
  [[ "$action" == "preserve" || "$action" == "delete" ]] || fail "invalid overlay action: $action"
  [[ -z "${extra:-}" ]] || fail "invalid overlay entry for $overlay_path"
  if [[ "$action" == "preserve" ]]; then
    [[ -f "$overlay_path" ]] || fail "preserved Commercial path is missing: $overlay_path"
  fi
done < dependencies/commercial-overlay.txt
grep -Fq '"edition": "commercial"' .github/workflows/release.yml || \
  fail "release manifest does not declare the commercial edition"
grep -Fq 'soulacy-commercial/.github/workflows/release.yml' .github/workflows/release.yml || \
  fail "release signatures are not bound to the private repository"
grep -Fq '"dependencies"' .github/workflows/release.yml || \
  fail "release manifest does not record dependency revisions"

echo "edition dependencies: Personal $revision -> Teams -> Scale"
