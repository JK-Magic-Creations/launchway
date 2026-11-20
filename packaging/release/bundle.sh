#!/bin/sh
# Makes the air-gapped bundle: one archive with everything needed to install Launchway where
# there is no internet access.
#
#   packaging/release/bundle.sh VERSION RELEASE_DIR
#
# RELEASE_DIR must hold a finished release (release.sh, repo.sh) and its images (images.sh, which
# writes images.txt). Writes dist/launchway-bundle-VERSION.tar holding launchway-bundle-VERSION/:
#
#   OFFLINE-INSTALL.md          how to verify and install from the bundle
#   release/                    the release files as published: binaries, deb and rpm, install.sh,
#                               charts, SBOMs, SHA256SUMS, its signature, provenance, cosign.pub
#                               (key-signed builds), and repo/ (the apt and yum repositories)
#   images/<name>/              each image as an OCI layout with its signatures and attestations
#                               (cosign save): load into a registry with `cosign load`
#   images/<name>-linux-<arch>.oci.tar   each image for one platform as an OCI archive, named
#                               <name>:<version>: for `podman load`, `ctr images import`,
#                               `kind load image-archive`
#   BUNDLE-SHA256SUMS           the SHA-256 of every file in the bundle, and its signature
#                               BUNDLE-SHA256SUMS.sigstore.json (SIGN as in finish.sh)
set -eu

[ $# -eq 2 ] || { echo "usage: $0 VERSION RELEASE_DIR" >&2; exit 2; }
version=$1
rel=$(cd "$2" && pwd)
ver=${version#v}
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
COSIGN=${COSIGN:-$root/bin/tools/cosign}
sign=${SIGN:-key}

[ -f "$rel/SHA256SUMS" ] || { echo "bundle.sh: $rel has no SHA256SUMS; run make release-local first" >&2; exit 1; }
[ -f "$rel/images.txt" ] || { echo "bundle.sh: $rel has no images.txt; run make release-images first" >&2; exit 1; }

name=launchway-bundle-$version
out=$root/dist/$name
rm -rf "$out"
mkdir -p "$out/release" "$out/images"
# The release, without the image build's own working files.
(cd "$rel" && find . -mindepth 1 -maxdepth 1 ! -name images ! -name 'reproduce-*' -exec cp -R {} "$out/release/" \;)

# ---- images ----
while read -r line; do
	[ -n "$line" ] || continue
	ref=${line%@*}  # registry/repo/name:tag
	digest=${line#*@}
	repo=${ref%:*}
	img=${repo##*/}
	flags=
	case $repo in 127.0.0.1:* | localhost:*) flags=--allow-http-registry ;; esac
	echo "saving $repo@$digest"
	# shellcheck disable=SC2086 # empty or one flag
	"$COSIGN" save $flags --dir "$out/images/$img" "$repo@$digest" >/dev/null
	tls=
	[ -z "$flags" ] || tls=--src-tls-verify=false
	for arch in amd64 arm64; do
		# shellcheck disable=SC2086
		skopeo copy -q $tls --override-os linux --override-arch "$arch" \
			"docker://$repo@$digest" "oci-archive:$out/images/$img-linux-$arch.oci.tar:$img:$ver"
	done
done <"$rel/images.txt"
cp "$rel/images.txt" "$out/images/images.txt"

# ---- the guide ----
sed -e "s/@VERSION@/$version/g" -e "s/@VER@/$ver/g" packaging/release/OFFLINE-INSTALL.md >"$out/OFFLINE-INSTALL.md"

# ---- checksums of the whole bundle ----
(cd "$out" && find . -type f ! -name 'BUNDLE-SHA256SUMS*' | sed 's#^\./##' | LC_ALL=C sort | xargs sha256sum >BUNDLE-SHA256SUMS)
case $sign in
none) ;;
key)
	key=${COSIGN_KEY:-$root/.release-key/cosign.key}
	[ -n "${COSIGN_KEY:-}" ] || COSIGN_PASSWORD=${COSIGN_PASSWORD:-}
	export COSIGN_PASSWORD
	cfg=$(mktemp)
	printf '%s\n' '{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json","caUrls":[],"oidcUrls":[],"rekorTlogUrls":[],"tsaUrls":[],"rekorTlogConfig":{"selector":"ANY"},"tsaConfig":{"selector":"ANY"}}' >"$cfg"
	"$COSIGN" sign-blob -y --key "$key" --signing-config "$cfg" --bundle "$out/BUNDLE-SHA256SUMS.sigstore.json" \
		"$out/BUNDLE-SHA256SUMS" >/dev/null 2>&1
	rm -f "$cfg"
	;;
keyless)
	"$COSIGN" sign-blob -y --bundle "$out/BUNDLE-SHA256SUMS.sigstore.json" "$out/BUNDLE-SHA256SUMS" >/dev/null 2>&1
	;;
esac
chmod -R a+rX "$out"

epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
(cd "$root/dist" && find "$name" -print0 | LC_ALL=C sort -z |
	tar --null -T - --no-recursion --mtime="@$epoch" --owner=0 --group=0 --numeric-owner -cf "$name.tar")
rm -rf "$out"
ls -l "$root/dist/$name.tar"
