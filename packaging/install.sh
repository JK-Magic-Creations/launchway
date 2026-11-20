#!/bin/sh
# Installs, upgrades or removes a Launchway runner on a Linux machine with systemd.
#
#   sudo sh install.sh --server https://ci.example.com --token-file /root/runner-token
#   sudo sh install.sh upgrade --version v1.5.0
#   sudo sh install.sh uninstall [--purge]
#
# It downloads the release's binary and checksums, checks the binary against SHA256SUMS, and
# checks the signature on SHA256SUMS when it can (see "Verification" below); any mismatch stops it
# before anything is installed. Then it creates the launchway-runner system user, writes
# /etc/launchway/runner.yaml and /etc/launchway/runner.env (the token, root only), installs a
# hardened systemd unit and starts it. Running it again with the same options changes nothing;
# with other options it updates the settings.
#
# Verification. The signature on SHA256SUMS is checked:
#   - with --public-key FILE: against that key, with cosign if it is installed, else with openssl
#     (for releases signed with a key, such as internal or air-gapped rebuilds);
#   - otherwise, if cosign is installed: as a keyless signature by the project's release workflow
#     (GitHub Actions, tag v*), checked against the public Sigstore instance;
#   - otherwise only the checksum is checked, and the script says so. --require-signature makes
#     that an error.
# Install cosign (https://docs.sigstore.dev) or pass --public-key to get the full check.
#
# Run `sh install.sh --help` for every option. POSIX sh; needs curl or wget, sha256sum (or
# shasum), and systemd. Alpine and other OpenRC systems are not supported yet.
set -eu

# Replaced with the release's version when the release is built.
DEFAULT_VERSION="@VERSION@"
RELEASES_URL=https://github.com/jk-magic-creations/launchway/releases/download
# The release workflow's identity, for keyless verification.
CERT_IDENTITY_REGEXP='^https://github\.com/(?i:jk-magic-creations/launchway)/\.github/workflows/release\.yml@refs/tags/v'
CERT_ISSUER=https://token.actions.githubusercontent.com

USER_NAME=launchway-runner
BIN=/usr/local/bin/launchway
CONF_DIR=/etc/launchway
DATA_DIR=/var/lib/launchway-runner
UNIT=/etc/systemd/system/launchway-runner.service
DROPIN_DIR=/etc/systemd/system/launchway-runner.service.d
HEALTH=http://127.0.0.1:8081/readyz

mode=install
version=
base_url=
from_dir=
server=
token_file=
join_token=
name=
executor=
concurrency=
proxy=
no_proxy=
ca_file=
public_key=
require_signature=no
start=yes
purge=no
allow_downgrade=no

usage() {
	cat <<'EOF'
Usage:
  install.sh [install] --server URL (--token-file FILE | --join TOKEN) [options]
  install.sh upgrade   [--version V] [download options]
  install.sh uninstall [--purge]

Install options:
  --server URL          the Launchway server the runner connects to
  --token-file FILE     a file holding the runner token (or a runner pool's token)
  --join TOKEN          a one-time join token; the runner enrols and keeps its own identity
  --name NAME           the runner's name (default: the host name)
  --executor E          host (default), podman or docker; podman relaxes the sandbox (see
                        container-executor.conf), docker needs the user in the docker group
  --concurrency N       steps to run at once (default 2)
  --proxy URL           HTTPS_PROXY and HTTP_PROXY for the runner and Git
  --no-proxy LIST       NO_PROXY
  --ca-file FILE        a PEM CA certificate to trust as well as the system's (a private CA, or a
                        proxy that inspects TLS)
  --no-start            install and configure, but do not enable or start the service

Download options:
  --version V           the release, such as v1.4.0 (default: this script's release)
  --base-url URL        where the release files are (default: GitHub releases)
  --from-dir DIR        take the release files from DIR (an offline bundle) instead
  --public-key FILE     verify the signature with this key (cosign or openssl)
  --require-signature   refuse to install unless the signature was verified
  --allow-downgrade     upgrade: allow a version older than the installed one

Uninstall options:
  --purge               also delete the settings, the workspaces and the user
EOF
}

say() { printf '%s\n' "launchway-install: $*"; }
die() {
	printf '%s\n' "launchway-install: error: $*" >&2
	exit 1
}

# ---- arguments ----
case ${1:-} in
install | upgrade | uninstall)
	mode=$1
	shift
	;;
esac
while [ $# -gt 0 ]; do
	opt=$1
	case $opt in
	-h | --help)
		usage
		exit 0
		;;
	--purge) purge=yes ;;
	--no-start) start=no ;;
	--require-signature) require_signature=yes ;;
	--allow-downgrade) allow_downgrade=yes ;;
	--*=*)
		# --opt=value
		set -- "${opt%%=*}" "${opt#*=}" "$@"
		shift
		continue
		;;
	--version | --base-url | --from-dir | --server | --token-file | --join | --name | --executor | \
		--concurrency | --proxy | --no-proxy | --ca-file | --public-key)
		[ $# -ge 2 ] || die "$opt needs a value"
		val=$2
		shift
		case $opt in
		--version) version=$val ;;
		--base-url) base_url=$val ;;
		--from-dir) from_dir=$val ;;
		--server) server=$val ;;
		--token-file) token_file=$val ;;
		--join) join_token=$val ;;
		--name) name=$val ;;
		--executor) executor=$val ;;
		--concurrency) concurrency=$val ;;
		--proxy) proxy=$val ;;
		--no-proxy) no_proxy=$val ;;
		--ca-file) ca_file=$val ;;
		--public-key) public_key=$val ;;
		esac
		;;
	*)
		usage >&2
		die "unknown argument $opt"
		;;
	esac
	shift
done

# ---- checks shared by every mode ----
[ "$(id -u)" = 0 ] || die "run as root (sudo sh install.sh ...)"
[ "$(uname -s)" = Linux ] || die "only Linux is supported"
[ -d /run/systemd/system ] || die "systemd is not running; only systemd systems are supported (OpenRC and others are not yet)"

if command -v dpkg-query >/dev/null 2>&1 && dpkg-query -W -f='${Status}' launchway-runner 2>/dev/null | grep -q 'ok installed'; then
	die "the launchway-runner package is installed; upgrade or remove it with apt instead of this script"
fi
if command -v rpm >/dev/null 2>&1 && rpm -q launchway-runner >/dev/null 2>&1; then
	die "the launchway-runner package is installed; upgrade or remove it with dnf or yum instead of this script"
fi

# ---- helpers ----
tmp=
cleanup() { [ -z "$tmp" ] || rm -rf "$tmp"; }
trap cleanup EXIT
trap 'exit 130' INT TERM

arch() {
	case $(uname -m) in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) die "unsupported architecture $(uname -m); releases exist for x86_64 and aarch64" ;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "neither sha256sum nor shasum is installed"
	fi
}

# same A B: true if both files exist with the same contents (cmp is not installed everywhere).
same() {
	[ -f "$1" ] && [ -f "$2" ] || return 1
	if command -v cmp >/dev/null 2>&1; then
		cmp -s "$1" "$2"
	else
		[ "$(sha256 "$1")" = "$(sha256 "$2")" ]
	fi
}

fetch() { # fetch NAME: the release file NAME into $tmp
	if [ -n "$from_dir" ]; then
		[ -f "$from_dir/$1" ] || die "$from_dir/$1 does not exist"
		cp "$from_dir/$1" "$tmp/$1"
		return
	fi
	url="$base_url/$1"
	if command -v curl >/dev/null 2>&1; then
		curl --fail --silent --show-error --location --proto '=https,http' --retry 3 -o "$tmp/$1" "$url" ||
			die "could not download $url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$tmp/$1" "$url" || die "could not download $url"
	else
		die "neither curl nor wget is installed"
	fi
}

# check_sum NAME: NAME's SHA-256 must be the one SHA256SUMS lists for it.
check_sum() {
	want=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
	[ -n "$want" ] || die "$1 is not listed in SHA256SUMS"
	got=$(sha256 "$tmp/$1")
	[ "$want" = "$got" ] || die "checksum mismatch for $1: SHA256SUMS says $want, the file is $got. Nothing was installed."
}

# The signature from a Sigstore bundle signed with a key, as DER, for openssl.
bundle_signature() {
	sig=$(tr -d '\n ' <"$1" | sed -n 's/.*"messageSignature":{[^}]*},"signature":"\([^"]*\)".*/\1/p')
	[ -n "$sig" ] || return 1
	printf '%s' "$sig" | openssl base64 -d -A >"$2"
}

verify_signature() {
	fetch SHA256SUMS.sigstore.json
	if [ -n "$public_key" ]; then
		[ -f "$public_key" ] || die "--public-key $public_key does not exist"
		if command -v cosign >/dev/null 2>&1; then
			# A key-signed bundle made offline has no transparency-log entry to check.
			cosign verify-blob --key "$public_key" --insecure-ignore-tlog \
				--bundle "$tmp/SHA256SUMS.sigstore.json" "$tmp/SHA256SUMS" >/dev/null 2>&1 ||
				die "the signature on SHA256SUMS does not match $public_key. Nothing was installed."
			say "signature verified with $public_key (cosign)"
		elif command -v openssl >/dev/null 2>&1; then
			bundle_signature "$tmp/SHA256SUMS.sigstore.json" "$tmp/SHA256SUMS.sig" ||
				die "SHA256SUMS.sigstore.json holds no key signature"
			openssl dgst -sha256 -verify "$public_key" -signature "$tmp/SHA256SUMS.sig" "$tmp/SHA256SUMS" >/dev/null 2>&1 ||
				die "the signature on SHA256SUMS does not match $public_key. Nothing was installed."
			say "signature verified with $public_key (openssl)"
		else
			die "--public-key needs cosign or openssl"
		fi
		return
	fi
	if command -v cosign >/dev/null 2>&1; then
		cosign verify-blob --bundle "$tmp/SHA256SUMS.sigstore.json" \
			--certificate-identity-regexp "$CERT_IDENTITY_REGEXP" --certificate-oidc-issuer "$CERT_ISSUER" \
			"$tmp/SHA256SUMS" >/dev/null 2>&1 ||
			die "the signature on SHA256SUMS is not the release workflow's. Nothing was installed."
		say "signature verified: signed by the release workflow (Sigstore)"
		return
	fi
	[ "$require_signature" = no ] ||
		die "cannot verify the signature: install cosign, or pass --public-key FILE"
	say "WARNING: the signature was not checked (no cosign, no --public-key); only the checksum was"
}

# download: fetch, verify and stage the release files; sets $staged_bin.
download() {
	[ -n "$version" ] || version=$DEFAULT_VERSION
	case $version in
	@*) die "this copy of install.sh does not name a release: pass --version (such as v1.4.0)" ;;
	v*) ;;
	*) version=v$version ;;
	esac
	[ -n "$base_url" ] || base_url=$RELEASES_URL/$version
	tmp=$(mktemp -d)
	a=$(arch)
	binname=launchway-${version#v}-linux-$a
	say "fetching Launchway $version for linux/$a from ${from_dir:-$base_url}"
	fetch SHA256SUMS
	verify_signature
	fetch "$binname"
	check_sum "$binname"
	fetch launchway-runner.service
	check_sum launchway-runner.service
	fetch launchway-runner-container.conf
	check_sum launchway-runner-container.conf
	chmod 0755 "$tmp/$binname"
	"$tmp/$binname" version >/dev/null || die "the downloaded binary does not run on this machine"
	staged_bin=$tmp/$binname
	say "checksum verified: $binname"
}

ensure_user() {
	nologin=/usr/sbin/nologin
	[ -x "$nologin" ] || nologin=/sbin/nologin
	if ! getent group "$USER_NAME" >/dev/null 2>&1; then
		groupadd --system "$USER_NAME"
	fi
	if ! getent passwd "$USER_NAME" >/dev/null 2>&1; then
		useradd --system --gid "$USER_NAME" --home-dir "$DATA_DIR" --no-create-home \
			--shell "$nologin" --comment "Launchway runner" "$USER_NAME"
		say "created system user $USER_NAME"
	fi
	install -d -m 0750 -o "$USER_NAME" -g "$USER_NAME" "$DATA_DIR"
}

install_binary() {
	# Atomically: a running service keeps its old binary until it is restarted.
	install -m 0755 "$staged_bin" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
}

install_unit() {
	changed=no
	if ! same "$tmp/launchway-runner.service" "$UNIT"; then
		install -m 0644 "$tmp/launchway-runner.service" "$UNIT"
		changed=yes
	fi
	dropin=$DROPIN_DIR/container-executor.conf
	if [ "${executor:-host}" = podman ]; then
		install -d -m 0755 "$DROPIN_DIR"
		if ! same "$tmp/launchway-runner-container.conf" "$dropin"; then
			install -m 0644 "$tmp/launchway-runner-container.conf" "$dropin"
			changed=yes
		fi
	elif [ -f "$dropin" ] && [ "$mode" = install ]; then
		rm -f "$dropin"
		changed=yes
	fi
	systemctl daemon-reload
}

# write_if_changed FILE MODE GROUP: stdin to FILE, only if different; returns 0 when changed.
write_if_changed() {
	new=$tmp/$(basename "$1").new
	cat >"$new"
	if same "$new" "$1"; then
		rm -f "$new"
		return 1
	fi
	install -m "$2" -o root -g "$3" "$new" "$1"
	rm -f "$new"
	return 0
}

yaml_quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"; }

# A one-time `lwj_` token is exchanged for a key of the runner's own through the runner's --join
# flag. It reaches the runner through LAUNCHWAY_JOIN in the root-only environment file, and the
# runner keeps its identity in /var/lib/launchway-runner. Refuse a binary too old to enrol.
join_hook() {
	if ! "$BIN" runner -h 2>&1 | grep -q -- '-join'; then
		die "--join needs a Launchway release whose runner supports join tokens; this one ($version) does not. Use --token-file."
	fi
	printf 'LAUNCHWAY_JOIN=%s\n' "$join_token"
}

write_settings() {
	install -d -m 0750 -o root -g "$USER_NAME" "$CONF_DIR"
	changed_settings=no
	{
		echo "# Written by install.sh; run it again to change these. See the README's Commands section."
		echo "server: $(yaml_quote "$server")"
		[ -z "$name" ] || echo "name: $(yaml_quote "$name")"
		echo "data: $DATA_DIR"
		echo "executor: ${executor:-host}"
		[ -z "$concurrency" ] || echo "concurrency: $concurrency"
	} | write_if_changed "$CONF_DIR/runner.yaml" 0640 "$USER_NAME" && changed_settings=yes

	ca_bundle=
	if [ -n "$ca_file" ]; then
		[ -f "$ca_file" ] || die "--ca-file $ca_file does not exist"
		grep -q 'BEGIN CERTIFICATE' "$ca_file" || die "--ca-file $ca_file is not a PEM certificate"
		system=
		for f in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/ca-bundle.pem /etc/ssl/cert.pem; do
			if [ -f "$f" ]; then
				system=$f
				break
			fi
		done
		[ -n "$system" ] || die "no system CA bundle found to add $ca_file to"
		# SSL_CERT_FILE replaces the system bundle for Go and Git, so it holds both.
		cat "$system" "$ca_file" | write_if_changed "$CONF_DIR/ca-bundle.pem" 0644 root && changed_settings=yes
		ca_bundle=$CONF_DIR/ca-bundle.pem
	fi

	token=
	if [ -n "$token_file" ]; then
		[ -f "$token_file" ] || die "--token-file $token_file does not exist"
		token=$(tr -d '\r\n' <"$token_file")
		[ -n "$token" ] || die "--token-file $token_file is empty"
		case $token in *[[:space:]]* | *\"* | *\'* | *\\*) die "the token in $token_file has spaces or quotes" ;; esac
	elif [ -z "$join_token" ] && [ -f "$CONF_DIR/runner.env" ]; then
		# Run again without credentials: keep those already installed.
		token=$(sed -n 's/^LAUNCHWAY_RUNNER_TOKEN=//p' "$CONF_DIR/runner.env")
		join_token=$(sed -n 's/^LAUNCHWAY_JOIN=//p' "$CONF_DIR/runner.env")
	fi
	{
		echo "# Written by install.sh. Readable by root only; steps never see these variables."
		[ -z "$token" ] || echo "LAUNCHWAY_RUNNER_TOKEN=$token"
		[ -z "$join_token" ] || join_hook
		if [ -n "$proxy" ]; then
			echo "HTTPS_PROXY=$proxy"
			echo "HTTP_PROXY=$proxy"
		fi
		[ -z "$no_proxy" ] || echo "NO_PROXY=$no_proxy"
		if [ -n "$ca_bundle" ]; then
			echo "SSL_CERT_FILE=$ca_bundle"
			echo "GIT_SSL_CAINFO=$ca_bundle"
		fi
	} | write_if_changed "$CONF_DIR/runner.env" 0600 root && changed_settings=yes
	[ -n "$ca_bundle" ] || rm -f "$CONF_DIR/ca-bundle.pem"
	return 0
}

setup_executor() {
	case ${executor:-host} in
	host) ;;
	podman)
		command -v podman >/dev/null 2>&1 || die "--executor podman: podman is not installed"
		if ! grep -q "^$USER_NAME:" /etc/subuid 2>/dev/null; then
			# 65536 ids, past the ranges usually given to people.
			usermod --add-subuids 1000000000-1000065535 --add-subgids 1000000000-1000065535 "$USER_NAME"
			say "gave $USER_NAME subordinate ids for rootless Podman"
		fi
		;;
	docker)
		command -v docker >/dev/null 2>&1 || die "--executor docker: docker is not installed"
		getent group docker >/dev/null 2>&1 || die "--executor docker: there is no docker group"
		say "WARNING: membership of the docker group is equivalent to root on this machine"
		usermod -a -G docker "$USER_NAME"
		;;
	*) die "--executor must be host, podman or docker" ;;
	esac
}

wait_ready() {
	i=0
	while [ $i -lt 60 ]; do
		if systemctl is-active --quiet launchway-runner; then
			if command -v curl >/dev/null 2>&1; then
				curl -fsS "$HEALTH" >/dev/null 2>&1 && return 0
			elif command -v wget >/dev/null 2>&1; then
				wget -q -O /dev/null "$HEALTH" 2>/dev/null && return 0
			else
				return 0
			fi
		fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

start_service() {
	[ "$start" = yes ] || {
		say "not started (--no-start): systemctl enable --now launchway-runner"
		return
	}
	systemctl enable --quiet launchway-runner
	if [ "$1" = restart ] || ! systemctl is-active --quiet launchway-runner; then
		systemctl restart launchway-runner
	fi
	if wait_ready; then
		say "the runner is running and has reached $server"
	else
		systemctl --no-pager status launchway-runner >&2 || :
		die "the runner did not become ready within 60 seconds: journalctl -u launchway-runner"
	fi
}

installed_version() {
	[ -x "$BIN" ] && "$BIN" version 2>/dev/null | awk '{ print $2 }'
}

# older A B: true if version A sorts before B.
older() {
	[ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "${1#v}" "${2#v}" | sort -V | head -n 1)" = "${1#v}" ]
}

# ---- modes ----
do_install() {
	[ -n "$server" ] || {
		if [ -f "$CONF_DIR/runner.yaml" ]; then
			server=$(sed -n "s/^server: '\(.*\)'$/\1/p" "$CONF_DIR/runner.yaml" | sed "s/''/'/g")
		fi
		[ -n "$server" ] || die "--server URL is required"
	}
	case $server in
	https://* | http://*) ;;
	*) die "--server must be an http:// or https:// URL" ;;
	esac
	[ -z "$token_file" ] || [ -z "$join_token" ] || die "give --token-file or --join, not both"
	if [ -n "$token_file" ]; then
		# Checked before anything is installed; read again in write_settings.
		[ -f "$token_file" ] || die "--token-file $token_file does not exist"
		[ -n "$(tr -d '\r\n' <"$token_file")" ] || die "--token-file $token_file is empty"
	fi
	[ -z "$ca_file" ] || [ -f "$ca_file" ] || die "--ca-file $ca_file does not exist"
	if [ -z "$token_file" ] && [ -z "$join_token" ] && \
		! grep -q '^LAUNCHWAY_RUNNER_TOKEN=.' "$CONF_DIR/runner.env" 2>/dev/null && \
		! grep -q '^LAUNCHWAY_JOIN=.' "$CONF_DIR/runner.env" 2>/dev/null; then
		die "--token-file FILE (or --join TOKEN) is required"
	fi
	if [ -z "$executor" ] && [ -f "$CONF_DIR/runner.yaml" ]; then
		executor=$(sed -n 's/^executor: //p' "$CONF_DIR/runner.yaml")
	fi
	case $concurrency in '' | *[!0-9]*) [ -z "$concurrency" ] || die "--concurrency must be a number" ;; esac

	download
	ensure_user
	setup_executor
	before=$(installed_version || :)
	restart=no
	if ! same "$staged_bin" "$BIN"; then
		install_binary
		restart=restart
		say "installed $BIN ($version)"
	fi
	install_unit
	[ "$changed" = no ] || restart=restart
	write_settings
	[ "$changed_settings" = no ] || restart=restart
	[ -z "$before" ] || [ "$restart" = no ] || say "replaced $before"
	start_service "$restart"
}

do_upgrade() {
	[ -x "$BIN" ] || die "Launchway is not installed here (no $BIN); install it first"
	[ -f "$CONF_DIR/runner.yaml" ] || die "no $CONF_DIR/runner.yaml; install first"
	server=$(sed -n "s/^server: '\(.*\)'$/\1/p" "$CONF_DIR/runner.yaml" | sed "s/''/'/g")
	executor=$(sed -n 's/^executor: //p' "$CONF_DIR/runner.yaml")
	current=$(installed_version)
	download
	if [ "$current" = "$version" ] && same "$staged_bin" "$BIN"; then
		say "already at $version"
		install_unit
		return
	fi
	if older "$version" "$current" && [ "$allow_downgrade" = no ]; then
		die "$version is older than the installed $current; pass --allow-downgrade to go back"
	fi
	install_binary
	install_unit
	say "upgraded $current -> $version"
	if systemctl is-enabled --quiet launchway-runner 2>/dev/null; then
		start_service restart
	fi
}

do_uninstall() {
	if [ -f "$UNIT" ]; then
		systemctl disable --now --quiet launchway-runner 2>/dev/null || :
	fi
	rm -f "$UNIT" "$BIN" "$BIN.new"
	rm -rf "$DROPIN_DIR"
	systemctl daemon-reload
	if [ "$purge" = yes ]; then
		rm -f "$CONF_DIR/runner.yaml" "$CONF_DIR/runner.env" "$CONF_DIR/ca-bundle.pem"
		rmdir "$CONF_DIR" 2>/dev/null || :
		rm -rf "$DATA_DIR"
		if getent passwd "$USER_NAME" >/dev/null 2>&1; then
			userdel "$USER_NAME" 2>/dev/null || :
		fi
		if getent group "$USER_NAME" >/dev/null 2>&1; then
			groupdel "$USER_NAME" 2>/dev/null || :
		fi
		say "removed the runner, its settings, workspaces and user"
	else
		say "removed the runner; settings in $CONF_DIR and workspaces in $DATA_DIR are kept (--purge removes them)"
	fi
}

case $mode in
install) do_install ;;
upgrade) do_upgrade ;;
uninstall) do_uninstall ;;
esac
