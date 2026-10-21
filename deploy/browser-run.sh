#!/bin/sh
set -eu
base_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mode=${1:---probe}
target=${2:-home}
case "$mode:$target" in --probe:home|--probe:topic-3011457|--source:linuxdo-browser|--source:x-*) ;; *) echo 'Use --probe home|topic-3011457 or --source linuxdo-browser|x-account-id' >&2; exit 2 ;; esac
case "$target" in *[!A-Za-z0-9_-]*) exit 2 ;; esac
if [ "$#" -gt 2 ]; then
  if [ "$#" -ne 4 ] || [ "$mode" != --source ] || [ "${3:-}" != --status ]; then echo 'Unexpected arguments' >&2; exit 2; fi
  case "$target:${4:-}" in x-*:[0-9]*) ;; *) exit 2 ;; esac
  case "${4:-}" in *[!0-9]*) exit 2 ;; esac
fi
sh "$base_dir/browser-network.sh" --check >/dev/null
# No host network, shared IPC, bind mount, credentials, Docker socket or published port.
container_name="infogap-browser-probe-$$"
trap 'docker rm -f "$container_name" >/dev/null 2>&1 || true' EXIT
trap 'exit 130' INT TERM
status=0
timeout --signal=TERM --kill-after=5s 75s docker run --rm --init \
  --name "$container_name" --network infogap-browser-net --dns 1.1.1.1 --dns 8.8.8.8 \
  --user pwuser --read-only --cap-drop=ALL \
  --security-opt=no-new-privileges \
  --security-opt="seccomp=$base_dir/browser-seccomp.json" \
  --cpus=1 --memory=1g --memory-swap=1g --pids-limit=256 \
  --ipc=private --shm-size=256m \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m,mode=1777 \
  --tmpfs /home/pwuser:rw,nosuid,nodev,size=64m,uid=1001,gid=1001 \
  --env HOME=/home/pwuser \
  infogap-browser:1.64.0 "$@" || status=$?
exit "$status"
