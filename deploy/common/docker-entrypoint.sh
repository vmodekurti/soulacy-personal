#!/bin/sh
set -eu

# Railway assigns each service a runtime port through PORT. Translate that
# platform convention to Soulacy's configuration variable while preserving the
# normal 18789 default for Docker, Compose, and other hosting providers.
if [ -n "${PORT:-}" ]; then
  export SOULACY_SERVER_PORT="$PORT"
fi

exec /usr/local/bin/soulacy "$@"
