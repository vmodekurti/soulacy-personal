#!/bin/sh
set -eu

# Railway assigns each service a runtime port through PORT. Translate that
# platform convention to Soulacy's configuration variable while preserving the
# normal 18789 default for Docker, Compose, and other hosting providers.
if [ -n "${PORT:-}" ]; then
  export SOULACY_SERVER_PORT="$PORT"
fi

data_root=/home/soulacy/.soulacy

if [ "$(id -u)" = "0" ]; then
  mkdir -p "$data_root"
  chown -R soulacy:soulacy "$data_root"
  export HOME=/home/soulacy
  exec gosu soulacy /usr/local/bin/soulacy "$@"
fi

exec /usr/local/bin/soulacy "$@"
