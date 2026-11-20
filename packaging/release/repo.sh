#!/bin/sh
# Generates an apt and a yum repository from the deb and rpm packages in OUTDIR, into
# OUTDIR/repo, ready to be served by any static web server (nothing is published).
#
#   packaging/release/repo.sh OUTDIR
#
#   OUTDIR/repo/launchway-repo.asc          the repositories' public OpenPGP key
#   OUTDIR/repo/apt/                         dists/stable/{InRelease,Release,Release.gpg}, pool/
#   OUTDIR/repo/yum/                         x86_64/ aarch64/ repodata/ (repomd.xml.asc), packages
#                                            signed with the same key
#   OUTDIR/repo/launchway.repo               a yum/dnf .repo file (BASE_URL)
#   OUTDIR/repo/launchway.sources            an apt deb822 sources file (BASE_URL)
#
# The release assets themselves (OUTDIR/*.deb, *.rpm) stay unsigned so that they are
# reproducible; SHA256SUMS and its signature cover them. The repository copies are signed.
#
# Environment:
#   REPO_GPG_KEY  an armored OpenPGP private key (file) to sign with, without a passphrase. If
#                 unset, a key for this machine is made once in .release-key/gpg (not for
#                 publishing).
#   BASE_URL      where the repository will be served (default https://example.invalid/launchway),
#                 written into the .repo and .sources files only.
#   CONTAINER     podman (default) or docker: apt-ftparchive and createrepo_c run in containers.
set -eu

[ $# -eq 1 ] || { echo "usage: $0 OUTDIR" >&2; exit 2; }
out=$(cd "$1" && pwd)
root=$(cd "$(dirname "$0")/../.." && pwd)
ctr=${CONTAINER:-podman}
base_url=${BASE_URL:-https://example.invalid/launchway}
DEBIAN_IMAGE=${DEBIAN_IMAGE:-docker.io/library/debian:12@sha256:268fb24c39bca92d326dd83ea3abebe4a3f7751c804ffef23bb6d857dd343ff8}
ROCKY_IMAGE=${ROCKY_IMAGE:-quay.io/rockylinux/rockylinux:9@sha256:bb5755f96bec05ff26165e779ec843d7167544be9e50770508f307019a6d05c4}

ls "$out"/*.deb "$out"/*.rpm >/dev/null 2>&1 || { echo "repo.sh: no packages in $out" >&2; exit 1; }

# ---- the signing key ----
gnupghome=$(mktemp -d)
trap 'rm -rf "$gnupghome"' EXIT
chmod 0700 "$gnupghome"
key=${REPO_GPG_KEY:-}
if [ -z "$key" ]; then
	key=$root/.release-key/gpg/repo-signing.asc
	if [ ! -f "$key" ]; then
		echo "repo.sh: no REPO_GPG_KEY; making a repository key for this machine in .release-key/gpg" >&2
		mkdir -p "$root/.release-key/gpg"
		gpg --batch --quiet --homedir "$gnupghome" --passphrase '' \
			--quick-gen-key 'Launchway repository (local build) <repo@launchway.invalid>' ed25519 sign never
		gpg --batch --quiet --homedir "$gnupghome" --armor --export-secret-keys >"$key"
		chmod 0600 "$key"
	fi
fi
gpg --batch --quiet --homedir "$gnupghome" --import "$key"
fpr=$(gpg --batch --homedir "$gnupghome" --with-colons --list-secret-keys | awk -F: '$1 == "fpr" { print $10; exit }')
[ -n "$fpr" ] || { echo "repo.sh: $key holds no secret key" >&2; exit 1; }
sign() { gpg --batch --quiet --homedir "$gnupghome" --pinentry-mode loopback --passphrase '' --local-user "$fpr" "$@"; }

repo=$out/repo
rm -rf "$repo"
mkdir -p "$repo/apt/pool/main" "$repo/yum/x86_64" "$repo/yum/aarch64"
gpg --batch --homedir "$gnupghome" --armor --export "$fpr" >"$repo/launchway-repo.asc"

# ---- apt ----
cp "$out"/*.deb "$repo/apt/pool/main/"
for arch in amd64 arm64; do
	mkdir -p "$repo/apt/dists/stable/main/binary-$arch"
done
# shellcheck disable=SC2016 # expanded in the container
"$ctr" run --rm -v "$repo/apt:/repo:Z" -w /repo "$DEBIAN_IMAGE" sh -ec '
	apt-get update -qq >/dev/null && apt-get install -qq -y --no-install-recommends apt-utils >/dev/null
	for arch in amd64 arm64; do
		apt-ftparchive --arch "$arch" packages pool >"dists/stable/main/binary-$arch/Packages"
		gzip -9n -k "dists/stable/main/binary-$arch/Packages"
	done
	apt-ftparchive -o APT::FTPArchive::Release::Origin=Launchway -o APT::FTPArchive::Release::Label=Launchway \
		-o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
		-o "APT::FTPArchive::Release::Architectures=amd64 arm64" -o APT::FTPArchive::Release::Components=main \
		release dists/stable >dists/stable/Release.new
	mv dists/stable/Release.new dists/stable/Release'
sign --armor --detach-sign --output "$repo/apt/dists/stable/Release.gpg" "$repo/apt/dists/stable/Release"
sign --clearsign --output "$repo/apt/dists/stable/InRelease" "$repo/apt/dists/stable/Release"
cat >"$repo/launchway.sources" <<EOF
Types: deb
URIs: $base_url/apt
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/launchway-repo.asc
EOF

# ---- yum ----
for f in "$out"/*.rpm; do
	case $f in
	*.x86_64.rpm) cp "$f" "$repo/yum/x86_64/" ;;
	*.aarch64.rpm) cp "$f" "$repo/yum/aarch64/" ;;
	esac
done
# The packages are signed with rpmsign, in a container that has it, with a copy of the key.
cp "$key" "$repo/.signing.asc"
# shellcheck disable=SC2016 # expanded in the container
"$ctr" run --rm -v "$repo/yum:/repo:Z" -v "$repo/.signing.asc:/key.asc:ro,Z" -w /repo "$ROCKY_IMAGE" sh -ec '
	dnf install -q -y createrepo_c rpm-sign gnupg2 >/dev/null
	export GNUPGHOME=$(mktemp -d)
	gpg --batch --quiet --import /key.asc
	fpr=$(gpg --batch --with-colons --list-secret-keys | awk -F: "\$1 == \"fpr\" { print \$10; exit }")
	rpmsign --define "_gpg_name $fpr" --define "_gpg_sign_cmd_extra_args --pinentry-mode loopback --passphrase \"\"" \
		--addsign x86_64/*.rpm aarch64/*.rpm >/dev/null
	createrepo_c -q --simple-md-filenames --set-timestamp-to-revision --revision 1 .'
rm -f "$repo/.signing.asc"
sign --armor --detach-sign --output "$repo/yum/repodata/repomd.xml.asc" "$repo/yum/repodata/repomd.xml"
cat >"$repo/launchway.repo" <<EOF
[launchway]
name=Launchway
baseurl=$base_url/yum
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=$base_url/launchway-repo.asc
EOF

echo "repositories in $repo (key $fpr):"
(cd "$repo" && find . -type f | LC_ALL=C sort)
