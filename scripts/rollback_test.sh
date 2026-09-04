#!/usr/bin/env bash
# rollback_test.sh — functional test for install.sh's backup/rollback logic.
#
# It extracts the real ROLLBACK BLOCK from install.sh (so we test the shipping
# code, not a copy), stubs the tiny output helpers + env, then exercises a full
# upgrade→rollback cycle in a throwaway HOME:
#   1. "install" v1 binaries + config
#   2. backup_current_install (snapshots v1)
#   3. "upgrade" to v2 binaries + config
#   4. rollback_install  → must restore v1 binaries AND v1 config
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
INSTALL_SH="$HERE/../install.sh"
[ -f "$INSTALL_SH" ] || { echo "install.sh not found"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Extract the block between the markers.
BLOCK="$WORK/block.sh"
sed -n '/# >>> ROLLBACK BLOCK START/,/# <<< ROLLBACK BLOCK END/p' "$INSTALL_SH" > "$BLOCK"
[ -s "$BLOCK" ] || { echo "FAIL: could not extract ROLLBACK BLOCK"; exit 1; }

export HOME="$WORK/home"
BIN_DIR="$WORK/home/.local/bin"
NEEDS_SUDO=0
CONFIG_WORKSPACE="$WORK/home/.soulacy/soulspace/config.yaml"
CONFIG_LEGACY="$WORK/home/.soulacy/config.yaml"
mkdir -p "$BIN_DIR" "$(dirname "$CONFIG_WORKSPACE")"

# Stub the output helpers used by the block.
ok()   { printf 'ok: %s\n' "$*"; }
warn() { printf 'warn: %s\n' "$*"; }
err()  { printf 'err: %s\n' "$*" >&2; exit 1; }
hdr()  { printf '== %s\n' "$*"; }

# shellcheck disable=SC1090
source "$BLOCK"

fail() { echo "FAIL: $*"; exit 1; }

# 1. Install v1.
printf '#!/bin/sh\necho v1\n' > "$BIN_DIR/soulacy"; chmod +x "$BIN_DIR/soulacy"
printf '#!/bin/sh\necho v1\n' > "$BIN_DIR/sy";      chmod +x "$BIN_DIR/sy"
echo "server: {api_key: V1KEY}" > "$CONFIG_WORKSPACE"

# 2. Snapshot v1.
backup_current_install
[ -f "$BACKUP_ROOT/latest" ] || fail "no latest pointer after backup"
STAMP="$(cat "$BACKUP_ROOT/latest")"
[ -f "$BACKUP_ROOT/$STAMP/soulacy" ] || fail "v1 soulacy not in backup"
[ -f "$BACKUP_ROOT/$STAMP/config/config.yaml" ] || fail "v1 config not in backup"

# 3. Upgrade to v2 (overwrite binaries + config, as a real upgrade would).
printf '#!/bin/sh\necho v2\n' > "$BIN_DIR/soulacy"
printf '#!/bin/sh\necho v2\n' > "$BIN_DIR/sy"
echo "server: {api_key: V2KEY}" > "$CONFIG_WORKSPACE"
[ "$("$BIN_DIR/soulacy")" = "v2" ] || fail "precondition: soulacy should be v2"

# 4. Roll back.
rollback_install

[ "$("$BIN_DIR/soulacy")" = "v1" ] || fail "soulacy not rolled back to v1"
[ "$("$BIN_DIR/sy")" = "v1" ]      || fail "sy not rolled back to v1"
grep -q V1KEY "$CONFIG_WORKSPACE"  || fail "config not rolled back to v1"
# The current (v2) config must be preserved, not destroyed.
ls "$CONFIG_WORKSPACE".pre-rollback.* >/dev/null 2>&1 || fail "v2 config not preserved as .pre-rollback"
grep -q V2KEY "$CONFIG_WORKSPACE".pre-rollback.* || fail "preserved config is not the v2 one"

# 5. Pruning keeps only BACKUP_KEEP snapshots.
export BACKUP_KEEP=3
for i in 1 2 3 4 5; do mkdir -p "$BACKUP_ROOT/old-$i"; done
_prune_backups
kept="$(ls -1d "$BACKUP_ROOT"/*/ 2>/dev/null | wc -l | tr -d ' ')"
[ "$kept" -le 3 ] || fail "prune kept $kept dirs, expected <= 3"

echo "PASS: backup, rollback, config-preservation, and pruning all work"
