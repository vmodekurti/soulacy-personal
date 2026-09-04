#!/usr/bin/env bash
set -Eeuo pipefail

ACTION=${1:-}
ROLE=${2:-}
VERSION=${3:-}
GATEWAY_IMAGE=${4:-}
EXECUTION_IMAGE=${5:-}
AWS_REGION=${6:-}

[[ "$ACTION" =~ ^(apply|rollback)$ ]] || { echo "action must be apply or rollback" >&2; exit 2; }
[[ "$ROLE" =~ ^(gateway|worker)$ ]] || { echo "role must be gateway or worker" >&2; exit 2; }
[[ "$VERSION" =~ ^[a-zA-Z0-9._-]+$ ]] || { echo "invalid release version" >&2; exit 2; }

ROOT=${SOULACY_REMOTE_RELEASE_ROOT:-}
OPT_DIR="$ROOT/opt/soulacy"
ETC_DIR="$ROOT/etc/soulacy"
VAR_DIR="$ROOT/var/lib/soulacy"
RELEASE_DIR="$OPT_DIR/releases/$VERSION"
ROLLBACK_DIR="$RELEASE_DIR/rollback"
SERVICE=soulacy.service
[[ "$ROLE" == "worker" ]] && SERVICE=soulacy-worker.service

restore_release() {
  local binary
  [[ -d "$ROLLBACK_DIR" ]] || { echo "no rollback exists for $VERSION" >&2; return 1; }
  for binary in soulacy sy soulacy-worker; do
    [[ -f "$ROLLBACK_DIR/$binary" ]] || continue
    install -m 0755 "$ROLLBACK_DIR/$binary" "$OPT_DIR/bin/$binary.rollback"
    mv -f "$OPT_DIR/bin/$binary.rollback" "$OPT_DIR/bin/$binary"
  done
  if [[ "$ROLE" == "gateway" && -f "$ROLLBACK_DIR/config.yaml" ]]; then
    install -m 0600 "$ROLLBACK_DIR/config.yaml" "$VAR_DIR/config.yaml"
    [[ -n "$ROOT" ]] || chown soulacy:soulacy "$VAR_DIR/config.yaml"
  elif [[ "$ROLE" == "worker" && -f "$ROLLBACK_DIR/worker.env" ]]; then
    install -m 0600 "$ROLLBACK_DIR/worker.env" "$ETC_DIR/worker.env"
  fi
  systemctl restart "$SERVICE"
}

if [[ "$ACTION" == "rollback" ]]; then
  restore_release
  echo "Rolled back $ROLE from release $VERSION"
  exit 0
fi

[[ "$GATEWAY_IMAGE" =~ @sha256:[a-f0-9]{64}$ ]] || { echo "gateway image is not digest pinned" >&2; exit 2; }
[[ "$EXECUTION_IMAGE" =~ @sha256:[a-f0-9]{64}$ ]] || { echo "execution image is not digest pinned" >&2; exit 2; }
[[ -n "$AWS_REGION" ]] || { echo "AWS region is required" >&2; exit 2; }

registry=${GATEWAY_IMAGE%%/*}
install -d -m 0755 "$RELEASE_DIR/stage" "$ROLLBACK_DIR"
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$registry" >/dev/null
docker pull "$GATEWAY_IMAGE" >/dev/null
docker pull "$EXECUTION_IMAGE" >/dev/null
cosign verify --key "$ETC_DIR/execution-image.pub" "$EXECUTION_IMAGE" >/dev/null

container_id=$(docker create "$GATEWAY_IMAGE")
cleanup() { docker rm -f "$container_id" >/dev/null 2>&1 || true; }
trap cleanup EXIT
for binary in soulacy sy soulacy-worker; do
  docker cp "$container_id:/usr/local/bin/$binary" "$RELEASE_DIR/stage/$binary"
  chmod 0755 "$RELEASE_DIR/stage/$binary"
done
cleanup
trap - EXIT

if [[ ! -f "$ROLLBACK_DIR/.captured" ]]; then
  for binary in soulacy sy soulacy-worker; do
    [[ -f "$OPT_DIR/bin/$binary" ]] && cp -a "$OPT_DIR/bin/$binary" "$ROLLBACK_DIR/$binary"
  done
  if [[ "$ROLE" == "gateway" ]]; then
    cp -a "$VAR_DIR/config.yaml" "$ROLLBACK_DIR/config.yaml"
  else
    cp -a "$ETC_DIR/worker.env" "$ROLLBACK_DIR/worker.env"
  fi
  touch "$ROLLBACK_DIR/.captured"
fi

release_failed() {
  trap - ERR
  echo "Release failed on $ROLE; restoring the previous version" >&2
  restore_release || true
  exit 1
}
trap release_failed ERR

if [[ "$ROLE" == "gateway" ]]; then
  install -m 0755 "$RELEASE_DIR/stage/soulacy" "$OPT_DIR/bin/soulacy.next"
  install -m 0755 "$RELEASE_DIR/stage/sy" "$OPT_DIR/bin/sy.next"
  mv -f "$OPT_DIR/bin/soulacy.next" "$OPT_DIR/bin/soulacy"
  mv -f "$OPT_DIR/bin/sy.next" "$OPT_DIR/bin/sy"
  sed -E \
    -e "s|^([[:space:]]*docker_image:).*|\\1 $EXECUTION_IMAGE|" \
    -e "s|^([[:space:]]*image:).*|\\1 $EXECUTION_IMAGE|" \
    "$VAR_DIR/config.yaml" >"$RELEASE_DIR/config.yaml.next"
  if [[ -n "$ROOT" ]]; then
    install -m 0600 "$RELEASE_DIR/config.yaml.next" "$VAR_DIR/config.yaml"
  else
    install -o soulacy -g soulacy -m 0600 "$RELEASE_DIR/config.yaml.next" "$VAR_DIR/config.yaml"
  fi
  systemctl restart "$SERVICE"
  for _ in {1..60}; do
    curl --fail --silent --show-error --max-time 2 http://127.0.0.1:1947/ready >/dev/null && break
    sleep 2
  done
  curl --fail --silent --show-error --max-time 5 http://127.0.0.1:1947/ready >/dev/null
else
  install -m 0755 "$RELEASE_DIR/stage/soulacy-worker" "$OPT_DIR/bin/soulacy-worker.next"
  mv -f "$OPT_DIR/bin/soulacy-worker.next" "$OPT_DIR/bin/soulacy-worker"
  sed -E \
    -e "s|^SOULACY_WORKER_IMAGE=.*|SOULACY_WORKER_IMAGE=$EXECUTION_IMAGE|" \
    -e "s|^SOULACY_WORKER_SANDBOX_IMAGE=.*|SOULACY_WORKER_SANDBOX_IMAGE=$EXECUTION_IMAGE|" \
    "$ETC_DIR/worker.env" >"$RELEASE_DIR/worker.env.next"
  install -m 0600 "$RELEASE_DIR/worker.env.next" "$ETC_DIR/worker.env"
  systemctl restart "$SERVICE"
  sleep 3
  systemctl is-active --quiet "$SERVICE"
fi

trap - ERR
printf '%s\n' "$VERSION" >"$RELEASE_DIR/active"

# Release bundles are rebuildable from the digest-pinned ECR images. Bound the
# local rollback cache so a long-lived Team Lite host cannot fill its root disk
# and strand SSM before the next deployment can clean it up.
mapfile -t obsolete_releases < <(
  find "$OPT_DIR/releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' |
    sort -nr |
    awk 'NR > 5 { sub(/^[^ ]+ /, ""); print }'
)
for obsolete_release in "${obsolete_releases[@]}"; do
  rm -rf -- "$obsolete_release"
done

echo "Applied Soulacy release $VERSION to $ROLE"
