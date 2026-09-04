#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

personal_url="${PERSONAL_REPO_URL:-https://github.com/vmodekurti/soulacy-personal.git}"
base_file="${PERSONAL_BASE_FILE:-.personal-base}"

fail() {
  echo "personal upstream: $*" >&2
  exit 1
}

[[ -f "$base_file" ]] || fail "$base_file is missing"
./scripts/verify-edition-dependencies.sh
base="$(tr -d '[:space:]' < "$base_file")"
[[ "$base" =~ ^[0-9a-f]{40}$ ]] || fail "$base_file must contain one full Git commit SHA"

# This probe deliberately removes ambient GitHub credentials. Personal must
# remain usable even when the private Commercial repository is unavailable.
env -u GITHUB_TOKEN -u GH_TOKEN git -c credential.helper= \
  ls-remote "$personal_url" HEAD >/dev/null || fail "Personal is not anonymously readable"

git fetch --quiet --no-tags "$personal_url" main
personal_head="$(git rev-parse FETCH_HEAD)"
git cat-file -e "${base}^{commit}" 2>/dev/null || fail "recorded Personal commit is unavailable"
while IFS=$'\t' read -r policy contract_path; do
  if [[ "$policy" == "personal_first" ]]; then
    git cat-file -e "${base}:${contract_path}" 2>/dev/null || \
      fail "Personal base does not contain declared public contract: $contract_path"
  fi
done < <(jq -r '.contracts[] | [.change_policy, .path] | @tsv' dependencies/editions.json)
git merge-base --is-ancestor "$base" HEAD || fail "Commercial history does not contain the recorded Personal base"
git merge-base --is-ancestor "$base" "$personal_head" || fail "recorded base is not on Personal main"

if [[ "$base" == "$personal_head" ]]; then
  echo "personal upstream: current at $base"
else
  echo "personal upstream: update available ($base -> $personal_head)"
fi
