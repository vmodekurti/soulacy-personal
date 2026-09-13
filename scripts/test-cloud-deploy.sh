#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

bash -n deploy/common/bootstrap.sh
sh -n deploy/common/docker-entrypoint.sh
jq empty deploy/azure/azuredeploy.json
jq empty railway.json
grep -q '^FROM golang:1.26.6-bookworm AS gobuild$' Dockerfile

# Railway's Metal builder requires a service-specific cacheKey prefix, which a
# reusable repository cannot know in advance. Rely on normal Docker layer
# caching and reject non-portable BuildKit cache mounts before deployment.
if grep -Eq -- '--mount=type=cache' Dockerfile; then
  echo "Dockerfile must not use service-specific Railway cache mounts" >&2
  exit 1
fi
if grep -Eq '^[[:space:]]*VOLUME[[:space:]]' Dockerfile; then
  echo "Dockerfile must not declare VOLUME; configure it in Railway instead" >&2
  exit 1
fi
grep -q 'SOULACY_SERVER_HOST=0.0.0.0' Dockerfile
grep -q 'SOULACY_SERVER_PORT="$PORT"' deploy/common/docker-entrypoint.sh
grep -q 'chown -R soulacy:soulacy "$data_root"' deploy/common/docker-entrypoint.sh
grep -q 'exec gosu soulacy /usr/local/bin/soulacy "$@"' deploy/common/docker-entrypoint.sh
grep -Fq 'CMD curl -fs "http://localhost:${PORT:-${SOULACY_SERVER_PORT:-18789}}/"' Dockerfile
grep -q '^ENTRYPOINT \["soulacy-entrypoint"\]$' Dockerfile

SOULACY_API_KEY=test-login-key \
SOULACY_JWT_SECRET=test-jwt-secret \
POSTGRES_PASSWORD=test-postgres-secret \
SOULACY_DOMAIN=soulacy.example.com \
docker compose -f deploy/common/docker-compose.cloud.yml config --quiet

grep -q 'soulacy-public-deploy-633654243571.s3.us-east-1.amazonaws.com' website/index.html
grep -q 'deploy%2Fazure%2Fazuredeploy.json' website/index.html

echo "cloud deployment assets: ok"
