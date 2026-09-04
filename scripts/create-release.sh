#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  make release-create VERSION=v0.1.0
  scripts/create-release.sh v0.1.0

Environment:
  VERSION=vX.Y.Z       Release version. Required unless passed as argv[1].
  RELEASE_REF=origin/main
                       Commit-ish to tag. Defaults to origin/main.
  REMOTE=origin        Git remote to fetch and push. Defaults to origin.
  DRY_RUN=1            Validate and print actions without creating/pushing tag.

This command creates an annotated release tag and pushes it. The existing
.github/workflows/release.yml workflow is triggered by tags matching v* and
will build artifacts, publish the GitHub Release, and push the Docker image.
EOF
}

version="${1:-${VERSION:-}}"
remote="${REMOTE:-origin}"
release_ref="${RELEASE_REF:-${remote}/main}"
dry_run="${DRY_RUN:-0}"

if [[ "${version}" == "-h" || "${version}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ -z "${version}" ]]; then
  echo "error: VERSION is required, for example: make release-create VERSION=v0.1.0" >&2
  exit 2
fi

if [[ ! "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "error: VERSION must look like v1.2.3 or v1.2.3-rc.1; got '${version}'" >&2
  exit 2
fi

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "error: not inside a git repository" >&2
  exit 2
fi

if ! git remote get-url "${remote}" >/dev/null 2>&1; then
  echo "error: git remote '${remote}' does not exist" >&2
  exit 2
fi

echo "→ Fetching ${remote}/main and tags..."
git fetch --tags "${remote}" main

commit="$(git rev-parse "${release_ref}^{commit}")"
short_commit="$(git rev-parse --short "${commit}")"

if git rev-parse -q --verify "refs/tags/${version}" >/dev/null; then
  echo "error: local tag '${version}' already exists" >&2
  exit 1
fi

if git ls-remote --exit-code --tags "${remote}" "refs/tags/${version}" >/dev/null 2>&1; then
  echo "error: remote tag '${version}' already exists on ${remote}" >&2
  exit 1
fi

if [[ "${dry_run}" == "1" || "${dry_run}" == "true" ]]; then
  echo "✓ Dry run only. Would create tag '${version}' at ${release_ref} (${short_commit}) and push it to ${remote}."
  exit 0
fi

echo "→ Creating annotated tag ${version} at ${release_ref} (${short_commit})..."
git tag -a "${version}" "${commit}" -m "Soulacy ${version}"

echo "→ Pushing ${version} to ${remote}..."
git push "${remote}" "refs/tags/${version}"

echo "✓ Release workflow started for ${version}."
if command -v gh >/dev/null 2>&1; then
  repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner 2>/dev/null || true)"
  if [[ -n "${repo}" ]]; then
    echo "  Watch it at: https://github.com/${repo}/actions/workflows/release.yml"
  fi
else
  echo "  Watch it in GitHub Actions: .github/workflows/release.yml"
fi
