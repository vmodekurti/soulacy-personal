#!/usr/bin/env bash
set -Eeuo pipefail

AWS_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/soulacy-remote-release-test.XXXXXX")
cleanup() { rm -rf -- "$TMP_DIR"; }
trap cleanup EXIT
mkdir -p "$TMP_DIR/bin" "$TMP_DIR/root/opt/soulacy/bin" "$TMP_DIR/root/etc/soulacy" "$TMP_DIR/root/var/lib/soulacy"

cat >"$TMP_DIR/bin/aws" <<'MOCK'
#!/usr/bin/env bash
printf 'test-password\n'
MOCK
cat >"$TMP_DIR/bin/docker" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  login) cat >/dev/null ;;
  pull|rm) ;;
  create) printf 'test-container\n' ;;
  cp)
    destination=$3
    printf 'new-%s\n' "$(basename "$destination")" >"$destination"
    ;;
  *) echo "unexpected docker command: $*" >&2; exit 1 ;;
esac
MOCK
for command in cosign systemctl curl; do
  cat >"$TMP_DIR/bin/$command" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
done
chmod 0755 "$TMP_DIR/bin/"*

for binary in soulacy sy soulacy-worker; do
  printf 'old-%s\n' "$binary" >"$TMP_DIR/root/opt/soulacy/bin/$binary"
  chmod 0755 "$TMP_DIR/root/opt/soulacy/bin/$binary"
done
touch "$TMP_DIR/root/etc/soulacy/execution-image.pub"
cat >"$TMP_DIR/root/var/lib/soulacy/config.yaml" <<'CONFIG'
executor:
  docker_image: old-execution
runtime:
  sandbox:
    image: old-execution
CONFIG
cat >"$TMP_DIR/root/etc/soulacy/worker.env" <<'ENV'
SOULACY_WORKER_IMAGE=old-execution
SOULACY_WORKER_SANDBOX_IMAGE=old-execution
ENV

gateway_image="example.invalid/gateway@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
execution_image="example.invalid/execution@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

PATH="$TMP_DIR/bin:$PATH" SOULACY_REMOTE_RELEASE_ROOT="$TMP_DIR/root" \
  "$AWS_DIR/remote-release.sh" apply gateway release-a "$gateway_image" "$execution_image" us-east-1
grep -q '^new-soulacy$' "$TMP_DIR/root/opt/soulacy/bin/soulacy"
grep -q "docker_image: $execution_image" "$TMP_DIR/root/var/lib/soulacy/config.yaml"
grep -q "image: $execution_image" "$TMP_DIR/root/var/lib/soulacy/config.yaml"

PATH="$TMP_DIR/bin:$PATH" SOULACY_REMOTE_RELEASE_ROOT="$TMP_DIR/root" \
  "$AWS_DIR/remote-release.sh" rollback gateway release-a
grep -q '^old-soulacy$' "$TMP_DIR/root/opt/soulacy/bin/soulacy"
grep -q 'docker_image: old-execution' "$TMP_DIR/root/var/lib/soulacy/config.yaml"

PATH="$TMP_DIR/bin:$PATH" SOULACY_REMOTE_RELEASE_ROOT="$TMP_DIR/root" \
  "$AWS_DIR/remote-release.sh" apply worker release-b "$gateway_image" "$execution_image" us-east-1
grep -q '^new-soulacy-worker$' "$TMP_DIR/root/opt/soulacy/bin/soulacy-worker"
grep -q "SOULACY_WORKER_IMAGE=$execution_image" "$TMP_DIR/root/etc/soulacy/worker.env"

PATH="$TMP_DIR/bin:$PATH" SOULACY_REMOTE_RELEASE_ROOT="$TMP_DIR/root" \
  "$AWS_DIR/remote-release.sh" rollback worker release-b
grep -q '^old-soulacy-worker$' "$TMP_DIR/root/opt/soulacy/bin/soulacy-worker"
grep -q 'SOULACY_WORKER_IMAGE=old-execution' "$TMP_DIR/root/etc/soulacy/worker.env"

printf 'PASS: remote release applies digest-pinned images and restores gateway/worker state\n'
