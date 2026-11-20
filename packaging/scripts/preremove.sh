#!/bin/sh
# Package pre-remove (deb: "remove" or "upgrade"; rpm: $1 is 0 on removal, 1 on upgrade). On
# removal the service is stopped and disabled; on an upgrade it keeps running until post-install
# restarts it.
set -e

case "${1:-}" in
remove | 0)
	if [ -d /run/systemd/system ]; then
		systemctl disable --now launchway-runner.service >/dev/null 2>&1 || :
	fi
	;;
esac
exit 0
