#!/bin/sh
# Package post-remove (deb: "remove", "purge" or "upgrade"; rpm: $1 is 0 on removal, 1 on
# upgrade). A purge (deb) also deletes the settings and the workspaces; the user is kept, as files
# elsewhere may still belong to it.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || :
fi
if [ "${1:-}" = purge ]; then
	rm -rf /etc/launchway/runner.yaml /etc/launchway/runner.env /var/lib/launchway-runner
	rmdir /etc/launchway 2>/dev/null || :
fi
exit 0
