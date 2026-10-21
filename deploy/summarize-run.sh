#!/bin/sh
# Root scheduler enters an unprivileged, separate account. No sudo for the web user.
set -eu
[ "$#" -eq 0 ] || { echo 'No arguments accepted' >&2; exit 2; }
base_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
agent_home=/var/lib/infogap-agent
if [ "$(id -u)" -eq 0 ]; then
  exec /sbin/runuser -u infogap-agent -- /usr/bin/env -i \
    HOME="$agent_home" PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 \
    /usr/bin/python3 "$base_dir/scripts/summarize.py"
fi
[ "$(id -un)" = infogap-agent ] || { echo 'Only the summarizer account may run this command' >&2; exit 1; }
exec /usr/bin/env -i HOME="$agent_home" PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 \
  /usr/bin/python3 "$base_dir/scripts/summarize.py"
