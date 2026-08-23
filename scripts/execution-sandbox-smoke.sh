#!/usr/bin/env bash
set -euo pipefail

# Destructive/adversarial checks belong on a disposable worker node. The
# script creates only a temporary directory and disposable containers.
: "${SOULACY_EXECUTION_IMAGE:?set SOULACY_EXECUTION_IMAGE to the production digest-pinned image}"
SOULACY_SANDBOX_RUNTIME="${SOULACY_SANDBOX_RUNTIME:-runsc}"
SOULACY_COSIGN_KEY="${SOULACY_COSIGN_KEY:-}"

case "$SOULACY_EXECUTION_IMAGE" in
  *@sha256:*) ;;
  *) echo "execution image is not digest-pinned" >&2; exit 1 ;;
esac

command -v docker >/dev/null
command -v cosign >/dev/null
docker info --format '{{json .Runtimes}}' | grep -q "\"${SOULACY_SANDBOX_RUNTIME}\""
if [[ -n "$SOULACY_COSIGN_KEY" ]]; then
  cosign verify --key "$SOULACY_COSIGN_KEY" "$SOULACY_EXECUTION_IMAGE" >/dev/null
else
  cosign verify "$SOULACY_EXECUTION_IMAGE" >/dev/null
fi

SOULACY_ESCAPE_TMP="$(mktemp -d)"
trap 'rm -rf -- "$SOULACY_ESCAPE_TMP"' EXIT
mkdir -p "$SOULACY_ESCAPE_TMP/workspace-a" "$SOULACY_ESCAPE_TMP/workspace-b"
printf 'sibling-secret' > "$SOULACY_ESCAPE_TMP/workspace-b/secret"
printf 'host-secret' > "$SOULACY_ESCAPE_TMP/host-secret"

docker run --rm --runtime "$SOULACY_SANDBOX_RUNTIME" \
  --network none --read-only --user 65532:65532 --cap-drop ALL \
  --security-opt no-new-privileges --pids-limit 32 --memory 128m --cpus 1 \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  -v "$SOULACY_ESCAPE_TMP/workspace-a:/workspace:rw" -w /workspace \
  -e SOULACY_SIBLING_HOST_PATH="$SOULACY_ESCAPE_TMP/workspace-b/secret" \
  -e SOULACY_HOST_MARKER="$SOULACY_ESCAPE_TMP/host-secret" \
  "$SOULACY_EXECUTION_IMAGE" python3 - <<'PY'
import os, socket

for path in (os.environ["SOULACY_SIBLING_HOST_PATH"], os.environ["SOULACY_HOST_MARKER"], "/var/run/docker.sock"):
    if os.path.exists(path):
        raise SystemExit(f"escape: host path is visible: {path}")

try:
    open("/rootfs-write", "w").write("bad")
    raise SystemExit("escape: container root is writable")
except OSError:
    pass

status = open("/proc/self/status", encoding="utf-8").read()
if "NoNewPrivs:\t1" not in status:
    raise SystemExit("escape: no-new-privileges is not active")
cap = next(line.split()[1] for line in status.splitlines() if line.startswith("CapEff:"))
if int(cap, 16) != 0:
    raise SystemExit(f"escape: effective capabilities remain: {cap}")

for host in ("169.254.169.254", "127.0.0.1", "10.0.0.1", "1.1.1.1"):
    sock = socket.socket()
    sock.settimeout(0.35)
    try:
        sock.connect((host, 80))
    except OSError:
        pass
    else:
        raise SystemExit(f"escape: direct network route reached {host}")
    finally:
        sock.close()

print("execution sandbox smoke passed")
PY
