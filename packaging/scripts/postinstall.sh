#!/bin/sh
# Package post-install (deb: "configure [old-version]"; rpm: $1 is 1 on install, 2 on upgrade).
# The service is not enabled on a first install: it needs a server and a token first. On an
# upgrade a running service is restarted with the new binary.
set -e

user=launchway-runner

upgrade=no
case "${1:-}" in
configure) [ -n "${2:-}" ] && upgrade=yes ;;
2) upgrade=yes ;;
esac

# The settings are readable by the runner's group; the environment file (the token) by root only.
chown root:"$user" /etc/launchway /etc/launchway/runner.yaml
chmod 0750 /etc/launchway
chmod 0640 /etc/launchway/runner.yaml
chown root:root /etc/launchway/runner.env
chmod 0600 /etc/launchway/runner.env

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || :
	if [ "$upgrade" = yes ]; then
		systemctl try-restart launchway-runner.service >/dev/null 2>&1 || :
	fi
fi

if [ "$upgrade" = no ]; then
	cat <<'EOF'
Launchway runner installed. To connect it to a server:
  1. set server: in /etc/launchway/runner.yaml
  2. set LAUNCHWAY_RUNNER_TOKEN in /etc/launchway/runner.env
  3. systemctl enable --now launchway-runner
EOF
fi
exit 0
