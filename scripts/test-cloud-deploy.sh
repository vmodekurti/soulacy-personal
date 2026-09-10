#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

bash -n deploy/common/bootstrap.sh
jq empty deploy/azure/azuredeploy.json

SOULACY_API_KEY=test-login-key \
SOULACY_JWT_SECRET=test-jwt-secret \
POSTGRES_PASSWORD=test-postgres-secret \
SOULACY_DOMAIN=soulacy.example.com \
docker compose -f deploy/common/docker-compose.cloud.yml config --quiet

grep -q 'soulacy-public-deploy-633654243571.s3.us-east-1.amazonaws.com' website/index.html
grep -q 'deploy%2Fazure%2Fazuredeploy.json' website/index.html

echo "cloud deployment assets: ok"

