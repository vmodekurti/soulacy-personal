#!/usr/bin/env bash
# DEPRECATED — merged into the canonical installer at the repo root: ./install.sh
#
# There is now ONE installer story. The root install.sh:
#   1. Downloads this platform's prebuilt release tarball
#      (soulacy_<version>_<os>_<arch>.tar.gz), then
#   2. Falls back to building from source via `make all`.
#
# Use it instead:
#   curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/install.sh | bash
#   # or, from a local checkout:
#   ./install.sh
#
# This shim forwards to the root installer so any old references keep working.
# (The owner may delete this file once no docs/CI reference scripts/install.sh.)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_INSTALLER="$SCRIPT_DIR/../install.sh"

if [ -f "$ROOT_INSTALLER" ]; then
    echo "scripts/install.sh is deprecated — forwarding to the root install.sh" >&2
    exec bash "$ROOT_INSTALLER" "$@"
fi

echo "Canonical installer not found at $ROOT_INSTALLER." >&2
echo "Run: curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/install.sh | bash" >&2
exit 1
