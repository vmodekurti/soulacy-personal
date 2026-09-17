#!/usr/bin/env bash
# build-browser-libs.sh — build the downloadable Chromium library bundle.
#
# Soulacy's image ships without Chromium's shared libraries: they are ~30MB
# that most installs never use, and they are system packages, so a deployment
# that wants them cannot simply install them — apt needs root and the gateway
# runs unprivileged.
#
# This produces the bundle such a deployment downloads instead. The libraries
# do not have to be installed as packages, only found: unpacked into a
# directory on LD_LIBRARY_PATH they load exactly as well from a mounted volume
# as from /usr/lib.
#
# The contents are the ACTUAL closure the browser binaries link against,
# computed with ldd, not a package list copied from a docs page. A hand-written
# list is how the first attempt at this shipped without libexpat and libXi —
# dependencies of the listed packages rather than their own files — and failed
# at launch with a linker error naming neither.
#
# Base-image libraries (libc and friends) are deliberately excluded: those must
# come from the host the browser actually runs on.
#
# Usage:
#   scripts/build-browser-libs.sh [--platform linux/amd64] [--out dist]
#
# Output: dist/browser-libs-<arch>.tar.gz and its sha256, which the install
# requires — these files are loaded into the browser process, so an unverified
# bundle is code execution rather than a corrupt download.
set -euo pipefail

PLATFORM="linux/amd64"
OUT="dist"
PLAYWRIGHT_VERSION="${PLAYWRIGHT_VERSION:-1.56.0}"

while [ $# -gt 0 ]; do
  case "$1" in
    --platform) PLATFORM="$2"; shift 2 ;;
    --out)      OUT="$2";      shift 2 ;;
    -h|--help)  sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

ARCH="${PLATFORM##*/}"
mkdir -p "$OUT"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

cat > "$BUILD_DIR/Dockerfile" <<'EOF'
FROM node:20-bookworm-slim AS node20
FROM python:3.12-slim-bookworm
ARG PLAYWRIGHT_VERSION
COPY --from=node20 /usr/local/bin/node /usr/local/bin/node
COPY --from=node20 /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# The same base as the runtime image, so the closure below is the closure that
# deployment will need.
RUN apt-get update && apt-get install -y --no-install-recommends \
        libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 \
        libcairo2 libcups2 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0 \
        libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 \
        libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 libxi6 libexpat1 \
        fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

ENV PLAYWRIGHT_BROWSERS_PATH=/browsers
RUN npm install -g "playwright@${PLAYWRIGHT_VERSION}" --no-audit --no-fund --silent \
    && npx playwright install chromium

# Take what the browser actually resolves, minus what the base image provides.
RUN mkdir -p /bundle && \
    for bin in $(find /browsers -type f \( -name chrome -o -name chrome-headless-shell \)); do \
      ldd "$bin" 2>/dev/null | awk '/=> \//{print $3}'; \
    done | sort -u \
    | grep -vE '/(libc|libm|libdl|libpthread|librt|libstdc\+\+|libgcc_s|ld-linux)[.-]' \
    | while read -r so; do cp -aL "$so" /bundle/ 2>/dev/null || true; done
EOF

echo "== building bundle for $PLATFORM (playwright $PLAYWRIGHT_VERSION)"
IMAGE="soulacy-browser-libs-$ARCH"
docker build --platform "$PLATFORM" --build-arg "PLAYWRIGHT_VERSION=$PLAYWRIGHT_VERSION" \
    -t "$IMAGE" "$BUILD_DIR" >/dev/null

TARBALL="$OUT/browser-libs-$ARCH.tar.gz"
CID="$(docker create --platform "$PLATFORM" "$IMAGE" /bin/true)"
trap 'docker rm -f "$CID" >/dev/null 2>&1 || true; rm -rf "$BUILD_DIR"' EXIT
docker cp "$CID:/bundle" "$BUILD_DIR/bundle" >/dev/null
tar -czf "$TARBALL" -C "$BUILD_DIR/bundle" .

echo "== verifying: launching Chromium in an image with no browser libraries"
VERIFY_DIR="$BUILD_DIR/verify"
mkdir -p "$VERIFY_DIR"
cp "$TARBALL" "$VERIFY_DIR/bundle.tar.gz"
cat > "$VERIFY_DIR/Dockerfile" <<'EOF'
FROM node:20-bookworm-slim AS node20
FROM python:3.12-slim-bookworm
ARG PLAYWRIGHT_VERSION
COPY --from=node20 /usr/local/bin/node /usr/local/bin/node
COPY --from=node20 /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && useradd --create-home soulacy && mkdir -p /data && chown soulacy /data
COPY --chown=soulacy bundle.tar.gz /data/bundle.tar.gz
USER soulacy
RUN mkdir -p /data/browser-libs && tar -xzf /data/bundle.tar.gz -C /data/browser-libs
ENV PLAYWRIGHT_BROWSERS_PATH=/data/browsers
ENV LD_LIBRARY_PATH=/data/browser-libs
RUN npm install -g --prefix /home/soulacy/.npm-global "playwright@${PLAYWRIGHT_VERSION}" --no-audit --no-fund --silent \
    && /home/soulacy/.npm-global/bin/playwright install chromium
RUN node -e "const {chromium}=require('/home/soulacy/.npm-global/lib/node_modules/playwright'); \
  (async () => { const b = await chromium.launch({headless:true, args:['--no-sandbox']}); \
  const p = await b.newPage(); await p.setContent('<title>bundle-verified</title>'); \
  if ((await p.title()) !== 'bundle-verified') process.exit(1); \
  console.log('VERIFIED: Chromium launched using only the bundle'); \
  await b.close(); })().catch(e => { console.error('BUNDLE IS NOT USABLE:', e.message.split('\n')[0]); process.exit(1); });"
EOF
if ! docker build --platform "$PLATFORM" --build-arg "PLAYWRIGHT_VERSION=$PLAYWRIGHT_VERSION" \
      -t "$IMAGE-verify" "$VERIFY_DIR" 2>&1 | grep -E "VERIFIED|BUNDLE IS NOT USABLE|ERROR"; then
  echo "verification failed — the bundle was not written" >&2
  rm -f "$TARBALL"
  exit 1
fi

# sha256sum on Linux, shasum on macOS. The script builds release artifacts,
# so it runs in CI as often as on a laptop, and a tool that exists on only one
# of them fails after the slow part has already succeeded.
if command -v sha256sum >/dev/null 2>&1; then
  SUM="$(sha256sum "$TARBALL" | cut -d' ' -f1)"
else
  SUM="$(shasum -a 256 "$TARBALL" | cut -d' ' -f1)"
fi
SIZE="$(du -h "$TARBALL" | cut -f1)"
COUNT="$(tar -tzf "$TARBALL" | grep -c '\.so' || true)"

echo
echo "bundle:   $TARBALL"
echo "size:     $SIZE ($COUNT shared objects)"
echo "sha256:   $SUM"
echo
echo "Install it with the gateway's browser-support endpoint, passing both the"
echo "URL it is published at and this checksum."
