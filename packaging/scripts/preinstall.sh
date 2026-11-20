#!/bin/sh
# Package pre-install (deb and rpm): the runner's system user and group. Idempotent.
set -e

user=launchway-runner
home=/var/lib/launchway-runner

nologin=/usr/sbin/nologin
[ -x "$nologin" ] || nologin=/sbin/nologin

if ! getent group "$user" >/dev/null 2>&1; then
	groupadd --system "$user"
fi
if ! getent passwd "$user" >/dev/null 2>&1; then
	useradd --system --gid "$user" --home-dir "$home" --no-create-home \
		--shell "$nologin" --comment "Launchway runner" "$user"
fi
exit 0
