#!/bin/sh
set -eu
# Keep interactive agent work separate from the website database and credentials.
if [ "$(id -u)" -eq 0 ]; then
  cd /var/lib/infogap-agent/work
  exec runuser -u infogap-agent -- /usr/local/bin/codex "$@"
fi
if [ "$(id -un)" != infogap-agent ]; then
  echo 'Run as root through this wrapper or as infogap-agent.' >&2
  exit 1
fi
cd /var/lib/infogap-agent/work
exec /usr/local/bin/codex "$@"
