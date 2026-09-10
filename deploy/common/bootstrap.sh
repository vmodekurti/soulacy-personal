#!/usr/bin/env bash
set -euo pipefail

: "${SOULACY_DOMAIN:?SOULACY_DOMAIN is required}"
: "${SOULACY_API_KEY:?SOULACY_API_KEY is required}"

SOULACY_VERSION="${SOULACY_VERSION:-latest}"
SOULACY_RELEASE_REF="${SOULACY_RELEASE_REF:-main}"
SOULACY_INSTALL_DIR="${SOULACY_INSTALL_DIR:-/opt/soulacy}"
SOURCE_BASE="https://raw.githubusercontent.com/vmodekurti/soulacy-personal/${SOULACY_RELEASE_REF}/deploy/common"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl docker.io docker-compose-v2 openssl
systemctl enable --now docker

install -d -m 0750 "${SOULACY_INSTALL_DIR}"
curl --fail --silent --show-error --location "${SOURCE_BASE}/docker-compose.cloud.yml" -o "${SOULACY_INSTALL_DIR}/docker-compose.yml"
curl --fail --silent --show-error --location "${SOURCE_BASE}/Caddyfile" -o "${SOULACY_INSTALL_DIR}/Caddyfile"

if [[ ! -f "${SOULACY_INSTALL_DIR}/.env" ]]; then
  umask 077
  cat > "${SOULACY_INSTALL_DIR}/.env" <<EOF
SOULACY_DOMAIN=${SOULACY_DOMAIN}
SOULACY_VERSION=${SOULACY_VERSION}
SOULACY_API_KEY=${SOULACY_API_KEY}
SOULACY_JWT_SECRET=$(openssl rand -hex 48)
POSTGRES_PASSWORD=$(openssl rand -hex 32)
EOF
fi

# Seed the persistent workspace before the first gateway boot. This makes the
# deployment compatible with older published images whose first-run config did
# not discover every nested storage environment variable.
umask 077
cat > /tmp/soulacy-cloud-config.yaml <<EOF
server:
  host: 0.0.0.0
  port: 18789
  gui_enabled: true
  api_key: "${SOULACY_API_KEY}"
auth:
  mode: jwt
  jwt_secret: "$(sed -n 's/^SOULACY_JWT_SECRET=//p' "${SOULACY_INSTALL_DIR}/.env")"
  jwt_refresh_ttl: 720h
storage:
  backend: postgres
  postgres_dsn: "postgres://soulacy:$(sed -n 's/^POSTGRES_PASSWORD=//p' "${SOULACY_INSTALL_DIR}/.env")@postgres:5432/soulacy?sslmode=disable"
  postgres_log_dir: /home/soulacy/.soulacy/logs
vector:
  backend: qdrant
  url: http://qdrant:6333
  collection: soulacy_memory
  dims: 768
executor:
  backend: pool
  workers: 4
deployment:
  profile: production
security:
  intent_gate: deny
runtime:
  sandbox:
    enabled: true
EOF

docker volume create soulacy_soulacy_data >/dev/null
docker run --rm \
  -v soulacy_soulacy_data:/data \
  -v /tmp/soulacy-cloud-config.yaml:/seed/config.yaml:ro \
  alpine:3.20 \
  sh -c 'mkdir -p /data/soulspace && if [ ! -f /data/soulspace/config.yaml ]; then cp /seed/config.yaml /data/soulspace/config.yaml; fi && chown -R 1000:1000 /data'
rm -f /tmp/soulacy-cloud-config.yaml

cd "${SOULACY_INSTALL_DIR}"
docker compose pull
docker compose up -d

for _ in $(seq 1 90); do
  if docker compose exec -T soulacy /usr/local/bin/soulacy --version >/dev/null 2>&1; then
    touch "${SOULACY_INSTALL_DIR}/READY"
    exit 0
  fi
  sleep 2
done

docker compose ps >&2
docker compose logs --tail=100 soulacy >&2
exit 1
