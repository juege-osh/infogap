#!/usr/bin/env bash
set -euo pipefail
# Run on the host after placing the release directory under /opt/infogap-osh/releases/.
release_path="${1:?Pass an absolute release directory}"
case "$release_path" in /opt/infogap-osh/releases/*) ;; *) echo 'Unexpected release path' >&2; exit 1;; esac
[ -x "$release_path/app" ]
[ -f "$release_path/web/index.html" ]
[ -f "$release_path/data/seed.json" ]
install -d -m 0755 /opt/infogap-osh
install -d -m 0700 -o nobody -g nobody /var/lib/infogap-osh
install -d -m 0700 /var/backups/infogap-osh
install -d -m 0750 -o root -g nobody /etc/infogap-osh
if [ -f /var/lib/infogap-osh/infogap.sqlite ]; then
  /usr/bin/python3 "$release_path/scripts/backup.py" --db /var/lib/infogap-osh/infogap.sqlite --directory /var/backups/infogap-osh
fi
previous_path="$(readlink /opt/infogap-osh/current || true)"
printf '%s\n' "$previous_path" > "$release_path/previous-release.txt"
ln -sfn "$release_path" /opt/infogap-osh/current.next
mv -Tf /opt/infogap-osh/current.next /opt/infogap-osh/current
install -m 0644 "$release_path"/deploy/infogap*.service "$release_path"/deploy/infogap*.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable infogap-osh.service
systemctl restart infogap-osh.service
for attempt in 1 2 3 4 5; do
  if curl --fail --silent http://127.0.0.1:18869/healthz >/dev/null; then break; fi
  sleep 1
done
curl --fail --silent http://127.0.0.1:18869/healthz
# The collector imports the curated launch archive idempotently. New scraped items stay pending.
runuser -u nobody -- /usr/bin/python3 "$release_path/scripts/collect.py" --db /var/lib/infogap-osh/infogap.sqlite --sources "$release_path/data/sources.json" --seed "$release_path/data/seed.json" --publish-seed
install -m 0644 "$release_path/deploy/infogap.tls.conf" /www/server/panel/vhost/nginx/infogap.osh.lol.conf
/www/server/nginx/sbin/nginx -t
/www/server/nginx/sbin/nginx -s reload
install -m 0644 "$release_path/deploy/infogap.http.conf" /opt/osh/003-config/osh/osh-nginx/conf.d/infogap.osh.lol.conf
docker exec osh-nginx nginx -t
docker exec osh-nginx nginx -s reload
systemctl enable --now infogap-collect.timer infogap-backup.timer
if [ -f "$release_path/deploy/codex-run.sh" ] && [ -x /usr/local/bin/codex ]; then
  install -m 0755 "$release_path/deploy/codex-run.sh" /usr/local/bin/infogap-codex
  if [ -f "$release_path/deploy/summarize-run.sh" ]; then
    systemctl enable --now infogap-summary.timer
  fi
fi
if [ -f "$release_path/deploy/infogap-browser.timer" ] && docker image inspect infogap-browser:1.64.0 >/dev/null 2>&1; then
  systemctl enable --now infogap-browser.timer
fi
systemctl start infogap-backup.service
printf '\nRelease activated: %s\nPrevious release: %s\n' "$release_path" "$previous_path"
