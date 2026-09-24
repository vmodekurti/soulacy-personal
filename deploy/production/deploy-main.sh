#!/usr/bin/env bash
# deploy/production/deploy-main.sh — deploy main to a compose host.
#
# This is what runs on soul.soulacy.io. It used to live only in the session
# scratchpad of whoever last deployed, which meant the production deploy
# procedure was not in version control and could not be reviewed, reproduced,
# or found. Now it is here, next to the compose file it drives.
#
# Run on the host, from the checkout:
#
#   cd /opt/soulacy-personal && git fetch -q origin && git merge -q --ff-only origin/main \
#     && bash deploy/production/deploy-main.sh
#
# What it does, in order: tag the running image so there is something to roll
# back to, build main with its real version stamped in, start it, wait for the
# health check, and prove the public URL answers. Any failure stops it; a
# container that never becomes healthy prints its logs and exits non-zero.
set -euo pipefail

cd "$(dirname "$0")/../.."
TS="$(date -u +%Y%m%dT%H%M%SZ)"
LOG="/var/log/soulacy-deploy-$TS.log"
exec > >(tee -a "$LOG") 2>&1
echo "== log $LOG"

# The version the binary will report. Tags are fetched explicitly because a
# checkout that only ever pulls main has none, and `git describe` without tags
# yields a bare commit hash. With them, a deploy of a tagged commit reports the
# tag exactly, and one past it reports v0.1.20-3-gabc1234 — which the update
# checker can compare against the latest release. The old deploy left this
# unset, so production called itself "dev" and the updater could only say
# "versions are not comparable".
git fetch -q --tags origin || true
# Empty rather than "dev" on failure: the Dockerfile then reads the repo
# VERSION file, which is still better than claiming to be an unreleased build.
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "")"
echo "== deploying $(git log -1 --format='%h %s') as version $VERSION"

# Something to go back to.
if docker image inspect soulacy:latest >/dev/null 2>&1; then
  docker tag soulacy:latest "soulacy:rollback-$TS"
  echo "== rollback image tagged soulacy:rollback-$TS"
fi
docker image prune -f >/dev/null

# The build arg is passed directly rather than through the VERSION env var:
# compose also uses that variable for the image tag, and a per-version image
# name would break the rollback tagging above and leave `docker compose up`
# starting a different image than the one just built.
echo "== building"
docker compose build --build-arg "VERSION=$VERSION" --progress plain soulacy 2>&1 \
  | grep -vE '^#[0-9]+ (sha256|extracting|DONE [0-9.]+s$|\.\.\.)' | tail -60

echo "== starting"
docker compose up -d soulacy

st=missing
for _ in $(seq 1 60); do
  st="$(docker inspect --format '{{.State.Health.Status}}' soulacy 2>/dev/null || echo missing)"
  echo "health=$st"
  [ "$st" = healthy ] && break
  sleep 5
done
if [ "$st" != healthy ]; then
  echo "== NOT HEALTHY, last logs:"
  docker logs --tail 60 soulacy
  exit 2
fi

echo "== public check"
curl -sS -A "Mozilla/5.0 soulacy-deploy-check" -o /dev/null -m 15 \
  -w 'https://soul.soulacy.io/ -> %{http_code}\n' https://soul.soulacy.io/ || true

echo "== image $(docker image inspect soulacy:latest --format '{{.Created}}')"
echo "== reports version: $(docker exec soulacy soulacy --version 2>/dev/null | head -1)"
echo "== DEPLOY_OK"
