#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

bash -n deploy/common/bootstrap.sh
jq empty deploy/azure/azuredeploy.json
jq empty railway.json

# Railway's Metal builder requires named BuildKit caches and manages attached
# volumes itself. Catch Dockerfile constructs that its validator rejects before
# a user discovers them during a one-click deployment.
if grep -Eq -- '--mount=type=cache,target=' Dockerfile; then
  echo "Dockerfile cache mounts must include an explicit id for Railway" >&2
  exit 1
fi
if grep -Eq '^[[:space:]]*VOLUME[[:space:]]' Dockerfile; then
  echo "Dockerfile must not declare VOLUME; configure it in Railway instead" >&2
  exit 1
fi
grep -q 'SOULACY_SERVER_HOST=0.0.0.0' Dockerfile
grep -q 'CMD curl -fs http://localhost:18789/' Dockerfile

SOULACY_API_KEY=test-login-key \
SOULACY_JWT_SECRET=test-jwt-secret \
POSTGRES_PASSWORD=test-postgres-secret \
SOULACY_DOMAIN=soulacy.example.com \
docker compose -f deploy/common/docker-compose.cloud.yml config --quiet

grep -q 'soulacy-public-deploy-633654243571.s3.us-east-1.amazonaws.com' website/index.html
grep -q 'deploy%2Fazure%2Fazuredeploy.json' website/index.html

echo "cloud deployment assets: ok"
