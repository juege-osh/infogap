#!/bin/sh
# Install root:root 0755 as /usr/local/sbin/infogap-submission-review-bridge.
# The SSH key may execute only this fixed no-argument command through sudo.
set -eu
[ "$#" -eq 0 ] || exit 64
umask 077
set -a
[ ! -f /etc/prompt-osh/smtp.env ] || . /etc/prompt-osh/smtp.env
set +a
exec /usr/sbin/runuser -u nobody -- /opt/infogap-osh/current/app \
 -db /var/lib/infogap-osh/infogap.sqlite \
 -submission-review-bridge
