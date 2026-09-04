#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE_DIR="${RELEASE_DIR:-$ROOT/bin/release}"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)}"
GENERATED_AT="${GENERATED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

cd "$ROOT"
mkdir -p "$RELEASE_DIR"

targets=""
while IFS= read -r target; do
  targets="${targets}${target}"$'\n'
done <<EOF_TARGETS
$(find "$RELEASE_DIR" -maxdepth 1 -type f -perm -111 -name 'soulacy-*' -print \
  | sed -E 's#^.*/soulacy-##' \
  | sort)
EOF_TARGETS

if [[ -z "$(printf '%s' "$targets" | tr -d '[:space:]')" ]]; then
  echo "package-release: no release binaries found in $RELEASE_DIR" >&2
  echo "run make release-linux, make release-darwin, or copy soulacy-<os>-<arch> and sy-<os>-<arch> there first" >&2
  exit 2
fi

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/soulacy-release-package-XXXXXXXX")"
trap 'rm -rf "$tmp_root"' EXIT

: > "$RELEASE_DIR/SHA256SUMS"

printf '%s' "$targets" | while IFS= read -r target; do
  [[ -n "$target" ]] || continue
  soulacy_bin="$RELEASE_DIR/soulacy-$target"
  sy_bin="$RELEASE_DIR/sy-$target"
  if [[ ! -x "$sy_bin" ]]; then
    echo "package-release: missing executable $sy_bin for $target" >&2
    exit 2
  fi

  platform="${target%-*}"
  arch="${target##*-}"
  archive_target="${platform}_${arch}"
  stage="$tmp_root/soulacy_${VERSION}_${archive_target}"
  mkdir -p "$stage"
  cp "$soulacy_bin" "$stage/soulacy"
  cp "$sy_bin" "$stage/sy"
  chmod 0755 "$stage/soulacy" "$stage/sy"
  cat > "$stage/README.txt" <<EOF
Soulacy ${VERSION} for ${target}

Install:
  install -m 755 soulacy sy /usr/local/bin

Verify:
  soulacy --version
  sy --version
  sy doctor
EOF

  archive="soulacy_${VERSION}_${archive_target}.tar.gz"
  tar -C "$stage" -czf "$RELEASE_DIR/$archive" .
  if command -v shasum >/dev/null 2>&1; then
    (cd "$RELEASE_DIR" && shasum -a 256 "$archive") >> "$RELEASE_DIR/SHA256SUMS"
  elif command -v sha256sum >/dev/null 2>&1; then
    (cd "$RELEASE_DIR" && sha256sum "$archive") >> "$RELEASE_DIR/SHA256SUMS"
  else
    echo "package-release: need shasum or sha256sum on PATH" >&2
    exit 2
  fi
  echo "✓ $RELEASE_DIR/$archive"
done

python3 - "$RELEASE_DIR" "$VERSION" "$COMMIT" "$GENERATED_AT" <<'PY'
import hashlib, json, pathlib, sys
release_dir = pathlib.Path(sys.argv[1])
version = sys.argv[2]
commit = sys.argv[3]
generated_at = sys.argv[4]
artifacts = []
for archive in sorted(release_dir.glob(f"soulacy_{version}_*.tar.gz")):
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    suffix = archive.name.removeprefix(f"soulacy_{version}_").removesuffix(".tar.gz")
    parts = suffix.rsplit("_", 1)
    os_name, arch = (parts + [""])[:2] if len(parts) == 2 else (suffix, "")
    artifacts.append({
        "name": archive.name,
        "os": os_name,
        "arch": arch,
        "sha256": digest,
        "bytes": archive.stat().st_size,
    })
manifest = {
    "product": "soulacy",
    "version": version,
    "commit": commit,
    "generated_at": generated_at,
    "artifacts": artifacts,
}
(release_dir / "release-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
PY

echo "✓ $RELEASE_DIR/SHA256SUMS"
echo "✓ $RELEASE_DIR/release-manifest.json"
