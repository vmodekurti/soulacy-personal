#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
INSTALL_DIR=${SOULACY_AWS_COMMAND_DIR:-"${HOME:?}/.local/bin"}
mkdir -p "$INSTALL_DIR"
ln -sfn "$SCRIPT_DIR/soulacy-aws" "$INSTALL_DIR/soulacy-aws"
printf 'Installed %s/soulacy-aws\n' "$INSTALL_DIR"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) printf 'Add this to your shell once:  export PATH="%s:$PATH"\n' "$INSTALL_DIR" ;;
esac
printf 'Try: soulacy-aws status\n'
