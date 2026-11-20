#!/bin/sh
# make test-install: installs the Launchway runner on real systemd machines (containers) of
# several distributions, the two supported ways, and checks each end to end:
#
#   install.sh  install (signature and checksum verified), settings and permissions, the unit
#               running as launchway-runner, a pipeline step run through a real server, an
#               idempotent re-run, a re-run with new options, refusal of a tampered binary and
#               of a tampered SHA256SUMS, upgrade to the next release, uninstall --purge;
#   packages    the deb or rpm from the generated (signed) apt or yum repository: install the
#               older release, configure, start, run a step, upgrade with apt or dnf, run a step
#               again, remove.
#
# Two releases are built first (linux/amd64 only), v0.0.1-test.1 and v0.0.2-test.1, from the
# commit checked out, signed with this machine's key (.release-key/). The server is the newer
# release, in a container of its own, with git daemon serving the test repository.
#
# Environment:
#   DISTROS     the distributions to test (default: debian ubuntu rocky)
#   SKIP_BUILD  1 to reuse the releases already in dist/install-test
#   KEEP        1 to leave the containers running afterwards
# Needs podman and jq. Containers and the network are named lw-it-*.
set -eu

root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"
here=deploy/test/install
work=$root/dist/install-test
OLD=v0.0.1-test.1
NEW=v0.0.2-test.1
DISTROS=${DISTROS:-debian ubuntu rocky}
NET=lw-it-net
SERVER=lw-it-server

DEBIAN_BASE=docker.io/library/debian:12@sha256:268fb24c39bca92d326dd83ea3abebe4a3f7751c804ffef23bb6d857dd343ff8
UBUNTU_BASE=docker.io/library/ubuntu:24.04@sha256:73b999393d07f2bf65a40373778a44dc9a6d54ad7718d29247d8ff9413c4d6a8
ROCKY_BASE=quay.io/rockylinux/rockylinux:9@sha256:bb5755f96bec05ff26165e779ec843d7167544be9e50770508f307019a6d05c4

command -v podman >/dev/null || { echo "test-install: needs podman" >&2; exit 1; }
command -v jq >/dev/null || { echo "test-install: needs jq" >&2; exit 1; }

log() { printf '\n==== %s\n' "$*"; }
fail() {
	printf 'test-install: FAIL: %s\n' "$*" >&2
	exit 1
}
pass() { printf '  ok: %s\n' "$*"; }

containers=
cleanup() {
	[ "${KEEP:-}" = 1 ] && return
	for c in $containers; do podman rm -f "$c" >/dev/null 2>&1 || :; done
	podman network rm "$NET" >/dev/null 2>&1 || :
}
trap cleanup EXIT

# ---- the releases ----
if [ "${SKIP_BUILD:-}" != 1 ]; then
	log "building releases $OLD and $NEW (linux/amd64)"
	mkdir -p "$work"
	for v in $OLD $NEW; do
		RELEASE_PLATFORMS=linux/amd64 ALLOW_DIRTY=1 packaging/release/release.sh "$v" "$work/$v" >"$work/build-$v.log" 2>&1 ||
			{ tail -30 "$work/build-$v.log"; fail "building $v"; }
		WEB_BUILD=skip
		export WEB_BUILD
	done
	# One repository holding both releases' packages, so that apt and dnf can upgrade.
	rm -rf "$work/pkgs"
	mkdir -p "$work/pkgs"
	cp "$work/$OLD"/*.deb "$work/$OLD"/*.rpm "$work/$NEW"/*.deb "$work/$NEW"/*.rpm "$work/pkgs/"
	packaging/release/repo.sh "$work/pkgs" >"$work/repo.log" 2>&1 || { tail -30 "$work/repo.log"; fail "repo.sh"; }
fi
for v in $OLD $NEW; do [ -f "$work/$v/SHA256SUMS" ] || fail "no release in $work/$v"; done
ls -l "$work/$NEW"

# ---- images ----
log "building the test machines"
podman build -q -t localhost/lw-it/server --build-arg BASE="$DEBIAN_BASE" -f $here/server.Containerfile $here >/dev/null
for d in $DISTROS; do
	case $d in
	debian) podman build -q -t localhost/lw-it/debian --build-arg BASE="$DEBIAN_BASE" -f $here/debian.Containerfile $here >/dev/null ;;
	ubuntu) podman build -q -t localhost/lw-it/ubuntu --build-arg BASE="$UBUNTU_BASE" -f $here/debian.Containerfile $here >/dev/null ;;
	rocky) podman build -q -t localhost/lw-it/rocky --build-arg BASE="$ROCKY_BASE" -f $here/rhel.Containerfile $here >/dev/null ;;
	alpine) fail "Alpine (OpenRC) is not supported by install.sh or the packages" ;;
	*) fail "unknown distribution $d" ;;
	esac
done

# ---- the server ----
log "starting a Launchway $NEW server"
podman rm -f $SERVER >/dev/null 2>&1 || :
podman network rm $NET >/dev/null 2>&1 || :
podman network create $NET >/dev/null
srv=$work/server
rm -rf "$srv"
mkdir -p "$srv/git" "$srv/data"
token=$(od -An -tx1 -N24 /dev/urandom | tr -d ' \n')
printf '%s\n' "$token" >"$work/token"
(
	tmp=$(mktemp -d)
	cd "$tmp"
	git init -q -b main .
	mkdir .launchway
	cat >.launchway/pipeline.yaml <<'EOF'
pipeline: install-test
steps:
  hello:
    # The host executor ignores the image and runs the script on the runner's machine.
    image: docker.io/library/alpine:3.22
    run: |
      echo "launchway-install-test user=$(id -un) home=$HOME pwd=$(pwd)"
      test "$(id -un)" = launchway-runner
      # The unit's sandbox applies to steps: no new privileges, and / mounted read-only.
      grep -E '^NoNewPrivs:[[:space:]]+1' /proc/self/status
      awk '$5 == "/" { print "root mount: " $6 }' /proc/self/mountinfo | tail -n 1 | grep 'root mount: ro'
EOF
	git add .
	git -c user.name=test -c user.email=test@example.invalid commit -q -m "install test"
	git clone -q --bare . "$srv/git/demo.git"
	rm -rf "$tmp"
)
cat >"$srv/server.yaml" <<'EOF'
listen: 0.0.0.0:8080
data: /srv/data
auth: { allow_anonymous: true }
runner: { enabled: false }
projects:
  - { name: demo, repo: "git://lw-server/demo.git", branch: main, poll: 1h }
EOF
podman run -d --name $SERVER --network $NET --network-alias lw-server \
	-v "$work/$NEW/launchway-${NEW#v}-linux-amd64:/usr/local/bin/launchway:ro,z" -v "$srv:/srv:z" \
	-e LAUNCHWAY_RUNNER_TOKEN="$token" localhost/lw-it/server \
	sh -c 'git daemon --base-path=/srv/git --export-all --reuseaddr --detach && exec launchway server --config /srv/server.yaml' >/dev/null
containers="$containers $SERVER"
api() { podman exec $SERVER curl -fsS -H 'X-Launchway-CSRF: 1' -H 'Content-Type: application/json' "$@"; }
i=0
until api http://127.0.0.1:8080/readyz >/dev/null 2>&1; do
	i=$((i + 1))
	[ $i -lt 60 ] || { podman logs $SERVER | tail -20; fail "the server did not start"; }
	sleep 1
done
pass "server $NEW ready"

# run_step RUNNER: starts a run, waits for it, checks that RUNNER ran the step as launchway-runner.
run_step() {
	id=$(api -X POST -d '{}' http://127.0.0.1:8080/api/v1/projects/demo/runs | jq -r .id)
	i=0
	while :; do
		status=$(api "http://127.0.0.1:8080/api/v1/runs/$id" | jq -r .status)
		case $status in succeeded | failed | canceled) break ;; esac
		i=$((i + 1))
		[ $i -lt 120 ] || fail "run $id is still $status after 2 minutes"
		sleep 1
	done
	logs=$(api "http://127.0.0.1:8080/api/v1/runs/$id/steps/hello/logs" | jq -r '.lines[].text')
	[ "$status" = succeeded ] || { printf '%s\n' "$logs"; fail "run $id $status"; }
	printf '%s\n' "$logs" | grep -q "^Runner $1, executor host" || { printf '%s\n' "$logs"; fail "run $id was not run by $1"; }
	printf '%s\n' "$logs" | grep -q 'user=launchway-runner' || fail "the step did not run as launchway-runner"
	printf '%s\n' "$logs" | grep -q 'root mount: ro' || fail "the step's root file system was writable"
	pass "run $id: step run by $1 as launchway-runner, NoNewPrivs, / read-only"
}

runner_version() { # NAME: the version the server saw last for that runner
	api http://127.0.0.1:8080/api/v1/runners | jq -r --arg n "$1" '.[] | select(.name == $n) | .version'
}

# ---- one machine ----
start_machine() { # NAME IMAGE
	podman rm -f "$1" >/dev/null 2>&1 || :
	extra=
	# Ubuntu checks the signature with cosign; the others with openssl.
	[ "$2" != localhost/lw-it/ubuntu ] || extra="-v $root/bin/tools/cosign:/usr/local/sbin/cosign:ro,z"
	# shellcheck disable=SC2086
	# --privileged (rootless: all capabilities, but only inside this user namespace) lets systemd
	# set up the unit's sandbox for real; without it ProtectSystem= silently does nothing here.
	podman run -d --name "$1" --hostname "$1" --systemd=always --privileged --network $NET \
		-v "$work:/release:ro,z" $extra "$2" >/dev/null
	containers="$containers $1"
	i=0
	until state=$(podman exec "$1" systemctl is-system-running 2>/dev/null) || [ "$state" = degraded ]; do
		i=$((i + 1))
		[ $i -lt 60 ] || fail "$1: systemd did not start ($state)"
		sleep 1
	done
	podman exec -i "$1" sh -c 'cat >/root/token' <"$work/token"
	podman exec "$1" chmod 0600 /root/token
}

check_installed() { # MACHINE BIN
	m=$1
	podman exec "$m" systemctl is-active --quiet launchway-runner || fail "$m: the service is not active"
	podman exec "$m" getent passwd launchway-runner | grep -Eq ':/var/lib/launchway-runner:/(usr/)?s?bin/nologin$' ||
		fail "$m: launchway-runner is not a nologin system user: $(podman exec "$m" getent passwd launchway-runner)"
	uid=$(podman exec "$m" id -u launchway-runner)
	[ "$uid" -lt 1000 ] || fail "$m: launchway-runner has uid $uid, not a system uid"
	perms=$(podman exec "$m" stat -c '%a %U %G' /etc/launchway/runner.env)
	[ "$perms" = "600 root root" ] || fail "$m: runner.env is $perms"
	perms=$(podman exec "$m" stat -c '%a %U %G' /etc/launchway/runner.yaml)
	[ "$perms" = "640 root launchway-runner" ] || fail "$m: runner.yaml is $perms"
	# shellcheck disable=SC2016 # expanded in the machine
	user=$(podman exec "$m" sh -c 'stat -c %U /proc/$(systemctl show -p MainPID --value launchway-runner)')
	[ "$user" = launchway-runner ] || fail "$m: the runner runs as $user"
	podman exec "$m" "$2" version >/dev/null
	pass "$m: active, as system user launchway-runner (uid $uid), runner.env 600 root, runner.yaml 640 root:launchway-runner"
	score=$(podman exec "$m" systemd-analyze security launchway-runner 2>/dev/null | tail -1 || :)
	[ -z "$score" ] || pass "$m: systemd-analyze security: $score"
}

# lwinstall MACHINE RELEASE [MODE] ARGS...: the release's install.sh, verifying the signature with
# this machine's release key (the releases are signed with it).
lwinstall() {
	lm=$1 lr=$2
	shift 2
	podman exec "$lm" sh "/release/$lr/install.sh" "$@" --public-key "/release/$lr/cosign.pub" --require-signature
}

test_script() { # DISTRO
	m=lw-it-$1-script
	name=$m
	log "$1: install.sh"
	start_machine "$m" "localhost/lw-it/$1"
	lwinstall "$m" "$OLD" --from-dir /release/$OLD \
		--server http://lw-server:8080 --token-file /root/token --name "$name" || fail "$m: install.sh"
	check_installed "$m" /usr/local/bin/launchway
	run_step "$name"
	[ "$(runner_version "$name")" = "$OLD" ] || fail "$m: the server sees version $(runner_version "$name")"
	pass "$m: the server sees runner version $OLD"

	# Again with the same options: nothing changes, the service is not restarted.
	pid=$(podman exec "$m" systemctl show -p MainPID --value launchway-runner)
	out=$(lwinstall "$m" "$OLD" --from-dir /release/$OLD \
		--server http://lw-server:8080 --token-file /root/token --name "$name") || fail "$m: re-run"
	[ "$(podman exec "$m" systemctl show -p MainPID --value launchway-runner)" = "$pid" ] ||
		{ printf '%s\n' "$out"; fail "$m: a re-run with the same options restarted the service"; }
	pass "$m: re-run with the same options changed nothing (same process $pid)"

	# With a proxy (bypassed for the server) and an extra CA: settings updated, runner restarted.
	podman exec "$m" sh -c 'openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj /CN=test-ca \
		-keyout /tmp/ca.key -out /root/test-ca.pem -days 1 2>/dev/null'
	lwinstall "$m" "$OLD" --from-dir /release/$OLD \
		--server http://lw-server:8080 --name "$name" --proxy http://proxy.invalid:3128 --no-proxy lw-server \
		--ca-file /root/test-ca.pem >/dev/null || fail "$m: re-run with --proxy and --ca-file"
	env=$(podman exec "$m" cat /etc/launchway/runner.env)
	for want in 'HTTPS_PROXY=http://proxy.invalid:3128' 'NO_PROXY=lw-server' 'SSL_CERT_FILE=/etc/launchway/ca-bundle.pem' 'LAUNCHWAY_RUNNER_TOKEN='; do
		printf '%s\n' "$env" | grep -q "^$want" || fail "$m: runner.env lacks $want"
	done
	podman exec "$m" grep -q 'CN=test-ca\|BEGIN CERTIFICATE' /etc/launchway/ca-bundle.pem || fail "$m: no CA bundle"
	[ "$(podman exec "$m" systemctl show -p MainPID --value launchway-runner)" != "$pid" ] || fail "$m: new settings did not restart the runner"
	run_step "$name"
	pass "$m: --proxy, --no-proxy and --ca-file written (token kept) and in force"

	# Tampering: a changed binary, then a changed SHA256SUMS. Nothing may be installed.
	before=$(podman exec "$m" sha256sum /usr/local/bin/launchway)
	podman exec "$m" sh -c "rm -rf /tmp/bad && cp -r /release/$NEW /tmp/bad && printf x >>/tmp/bad/launchway-${NEW#v}-linux-amd64"
	if out=$(lwinstall "$m" "$NEW" upgrade --from-dir /tmp/bad 2>&1); then
		fail "$m: a tampered binary was installed"
	fi
	printf '%s\n' "$out" | grep -q 'checksum mismatch' || { printf '%s\n' "$out"; fail "$m: no checksum mismatch reported"; }
	podman exec "$m" sh -c "rm -rf /tmp/bad && cp -r /release/$NEW /tmp/bad && echo '0000  extra' >>/tmp/bad/SHA256SUMS"
	if out=$(lwinstall "$m" "$NEW" upgrade --from-dir /tmp/bad 2>&1); then
		fail "$m: a tampered SHA256SUMS was accepted"
	fi
	printf '%s\n' "$out" | grep -q 'signature on SHA256SUMS does not match' || { printf '%s\n' "$out"; fail "$m: no signature mismatch reported"; }
	[ "$(podman exec "$m" sha256sum /usr/local/bin/launchway)" = "$before" ] || fail "$m: the binary changed"
	pass "$m: refused a tampered binary (checksum) and a tampered SHA256SUMS (signature); nothing changed"

	# Upgrade.
	lwinstall "$m" "$NEW" upgrade --from-dir /release/$NEW || fail "$m: upgrade"
	[ "$(podman exec "$m" /usr/local/bin/launchway version)" = "launchway $NEW" ] || fail "$m: not upgraded"
	check_installed "$m" /usr/local/bin/launchway
	run_step "$name"
	[ "$(runner_version "$name")" = "$NEW" ] || fail "$m: the server sees version $(runner_version "$name") after the upgrade"
	pass "$m: upgraded $OLD -> $NEW, reconnected"
	if lwinstall "$m" "$OLD" upgrade --from-dir /release/$OLD >/dev/null 2>&1; then
		fail "$m: downgraded without --allow-downgrade"
	fi
	pass "$m: refused a downgrade without --allow-downgrade"

	# Uninstall.
	podman exec "$m" sh /release/$NEW/install.sh uninstall --purge || fail "$m: uninstall"
	for f in /usr/local/bin/launchway /etc/systemd/system/launchway-runner.service /etc/launchway /var/lib/launchway-runner; do
		podman exec "$m" test ! -e "$f" || fail "$m: $f is still there"
	done
	! podman exec "$m" getent passwd launchway-runner >/dev/null || fail "$m: the user is still there"
	! podman exec "$m" systemctl is-active --quiet launchway-runner || fail "$m: still running"
	pass "$m: uninstall --purge removed the binary, unit, settings, workspaces and user"
	podman rm -f "$m" >/dev/null
}

test_packages() { # DISTRO
	m=lw-it-$1-pkg
	name=$m
	log "$1: packages"
	start_machine "$m" "localhost/lw-it/$1"
	oldv=$(echo "${OLD#v}" | tr - '~')
	case $1 in
	debian | ubuntu)
		podman exec "$m" sh -ec '
			install -d /etc/apt/keyrings
			cp /release/pkgs/repo/launchway-repo.asc /etc/apt/keyrings/
			sed "s#^URIs: .*#URIs: file:/release/pkgs/repo/apt#" /release/pkgs/repo/launchway.sources >/etc/apt/sources.list.d/launchway.sources
			apt-get update -qq 2>&1 | grep -v "^W: Download is performed unsandboxed" || :' ||
			fail "$m: apt repository"
		podman exec "$m" sh -c "DEBIAN_FRONTEND=noninteractive apt-get install -qq -y launchway-runner=$oldv-1 >/tmp/apt.log 2>&1" ||
			{ podman exec "$m" cat /tmp/apt.log; fail "$m: apt-get install"; }
		upgrade="DEBIAN_FRONTEND=noninteractive apt-get install -qq -y --only-upgrade launchway-runner"
		remove="DEBIAN_FRONTEND=noninteractive apt-get purge -qq -y launchway-runner"
		;;
	rocky)
		podman exec "$m" sh -ec '
			sed -e "s#^baseurl=.*#baseurl=file:///release/pkgs/repo/yum#" \
				-e "s#^gpgkey=.*#gpgkey=file:///release/pkgs/repo/launchway-repo.asc#" \
				/release/pkgs/repo/launchway.repo >/etc/yum.repos.d/launchway.repo' || fail "$m: yum repository"
		podman exec "$m" sh -c "dnf install -q -y launchway-runner-$oldv >/tmp/dnf.log 2>&1" ||
			{ podman exec "$m" cat /tmp/dnf.log; fail "$m: dnf install"; }
		upgrade="dnf upgrade -q -y launchway-runner"
		remove="dnf remove -q -y launchway-runner"
		;;
	esac
	pass "$m: installed launchway-runner $oldv from the signed repository"
	! podman exec "$m" systemctl is-enabled --quiet launchway-runner 2>/dev/null || fail "$m: the service was enabled before it was configured"
	podman exec "$m" sh -ec "
		sed -i 's|^# server: .*|server: http://lw-server:8080|; s|^# name: .*|name: $name|' /etc/launchway/runner.yaml
		sed -i \"s|^LAUNCHWAY_RUNNER_TOKEN=.*|LAUNCHWAY_RUNNER_TOKEN=\$(cat /root/token)|\" /etc/launchway/runner.env
		systemctl enable --now launchway-runner"
	i=0
	until podman exec "$m" curl -fsS http://127.0.0.1:8081/readyz >/dev/null 2>&1; do
		i=$((i + 1))
		[ $i -lt 60 ] || { podman exec "$m" journalctl -u launchway-runner --no-pager | tail -20; fail "$m: not ready"; }
		sleep 1
	done
	check_installed "$m" /usr/bin/launchway
	run_step "$name"
	pid=$(podman exec "$m" systemctl show -p MainPID --value launchway-runner)
	podman exec "$m" sh -c "$upgrade >/tmp/up.log 2>&1" || { podman exec "$m" cat /tmp/up.log; fail "$m: upgrade"; }
	[ "$(podman exec "$m" /usr/bin/launchway version)" = "launchway $NEW" ] || fail "$m: not upgraded"
	[ "$(podman exec "$m" systemctl show -p MainPID --value launchway-runner)" != "$pid" ] || fail "$m: the upgrade did not restart the runner"
	podman exec "$m" grep -q "^server: http://lw-server:8080" /etc/launchway/runner.yaml || fail "$m: the upgrade replaced runner.yaml"
	i=0
	until [ "$(runner_version "$name")" = "$NEW" ]; do
		i=$((i + 1))
		[ $i -lt 60 ] || fail "$m: the server does not see $NEW"
		sleep 1
	done
	run_step "$name"
	pass "$m: upgraded to $NEW with the package manager; settings kept; restarted and reconnected"
	podman exec "$m" sh -c "$remove >/tmp/rm.log 2>&1" || { podman exec "$m" cat /tmp/rm.log; fail "$m: remove"; }
	! podman exec "$m" systemctl is-active --quiet launchway-runner || fail "$m: still running after removal"
	podman exec "$m" test ! -e /usr/bin/launchway || fail "$m: the binary is still there"
	pass "$m: removed; the service stopped"
	podman rm -f "$m" >/dev/null
}

for d in $DISTROS; do
	test_script "$d"
	test_packages "$d"
done
log "test-install passed on: $DISTROS (install.sh and packages). Alpine/OpenRC: not supported."
