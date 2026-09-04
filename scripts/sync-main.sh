#!/usr/bin/env zsh
# Fast-forward the local `main` branch to match origin/main.
# Safe: aborts if `main` has diverged (never force-updates or merges).
#
# Usage:  ./scripts/sync-main.sh        (run from anywhere inside the repo)
#         syncmain                      (if installed as a shell function, see README note)

set -e

# Move to the repo root regardless of where the script is called from.
repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

current="$(git rev-parse --abbrev-ref HEAD)"

echo "Fetching from origin..."
git fetch --all --prune

if [[ "$current" != "main" ]]; then
  echo "You're on '$current', not 'main'. Updating origin refs only."
  echo "Local 'main' will fast-forward next time you check it out and run this."
  exit 0
fi

echo "Fast-forwarding main..."
git pull --ff-only

echo "Done. main is now at: $(git rev-parse --short HEAD)"
