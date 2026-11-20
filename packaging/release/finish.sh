#!/bin/sh
# Completes a release directory made by build.sh and packages.sh: an SBOM per artifact, the
# checksums file, its signature, and SLSA build provenance.
#
#   packaging/release/finish.sh VERSION OUTDIR
#
# Writes into OUTDIR:
#   <artifact>.cdx.json         a CycloneDX SBOM of each binary and package (syft)
#   SHA256SUMS                  the SHA-256 of every file (binaries, packages, SBOMs, install.sh,
#                               the unit, charts)
#   SHA256SUMS.sigstore.json    a Sigstore bundle signing SHA256SUMS (see SIGN)
#   provenance.json             SIGN=key only: an in-toto statement with a SLSA v1 provenance
#                               predicate naming every file in SHA256SUMS
#   provenance.sigstore.json    SIGN=key only: that statement, signed (DSSE)
#   cosign.pub                  SIGN=key only: the public key to verify with
# With SIGN=keyless (the release workflow) provenance comes from GitHub's attestation instead.
#
# Environment:
#   SIGN         key (default), keyless or none
#   COSIGN_KEY   SIGN=key: the private key file (cosign generate-key-pair). If unset, a key for
#                this machine is made once in .release-key/ (not for publishing: say so to whoever
#                gets the files). COSIGN_PASSWORD is its password (empty for that key).
#   TLOG         SIGN=key: 1 to record the signature in the public transparency log (Rekor). The
#                default, 0, keeps everything offline, for internal and air-gapped builds.
set -eu

[ $# -eq 2 ] || { echo "usage: $0 VERSION OUTDIR" >&2; exit 2; }
version=$1
out=$(cd "$2" && pwd)
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
tools=$root/bin/tools
COSIGN=${COSIGN:-$tools/cosign}
SYFT=${SYFT:-$tools/syft}
sign=${SIGN:-key}

# ---- SBOMs ----
SYFT_CHECK_FOR_APP_UPDATE=false
export SYFT_CHECK_FOR_APP_UPDATE
# syft stamps each SBOM with the time and a random serial number; both are replaced (by the
# commit's time and a serial number derived from the artifact's digest) so that the SBOMs are as
# reproducible as the artifacts they describe.
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
stamp=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$epoch" +%Y-%m-%dT%H:%M:%SZ)
for f in "$out"/launchway-*; do
	case $f in *.cdx.json | *.service | *.conf) continue ;; esac
	[ -f "$f" ] || continue
	name=$(basename "$f")
	# syft derives references from the path it is given, so it is always given a relative one.
	scandir=$out
	src=file:./$name
	case $name in
	*.deb | *.rpm)
		# syft does not look inside a package's payload; the package holds the release binary
		# (and text files), so its SBOM is that of a directory holding the same binary.
		case $name in *amd64* | *x86_64*) a=amd64 ;; *) a=arm64 ;; esac
		scandir=$(mktemp -d)
		mkdir "$scandir/package"
		cp "$out/launchway-${version#v}-linux-$a" "$scandir/package/launchway"
		src=dir:./package
		;;
	esac
	(cd "$scandir" && "$SYFT" scan "$src" --quiet --source-name "$name" --source-version "$version" \
		-o "cyclonedx-json=$f.cdx.tmp")
	[ "$scandir" = "$out" ] || rm -rf "$scandir"
	d=$(sha256sum "$f" | cut -c1-32)
	uuid=$(printf '%s' "$d" | sed 's/^\(.\{8\}\)\(.\{4\}\)\(.\{4\}\)\(.\{4\}\)\(.\{12\}\)$/\1-\2-\3-\4-\5/')
	sed -e "s/\"serialNumber\":\"urn:uuid:[0-9a-f-]*\"/\"serialNumber\":\"urn:uuid:$uuid\"/" \
		-e "s/\"timestamp\":\"[^\"]*\"/\"timestamp\":\"$stamp\"/" "$f.cdx.tmp" >"$f.cdx.json"
	rm -f "$f.cdx.tmp"
done

# ---- checksums ----
(
	cd "$out"
	rm -f SHA256SUMS SHA256SUMS.sigstore.json provenance.json provenance.sigstore.json
	find . -maxdepth 1 -type f ! -name 'SHA256SUMS*' ! -name 'provenance*' ! -name cosign.pub \
		| sed 's#^\./##' | LC_ALL=C sort | xargs sha256sum >SHA256SUMS
)

# ---- signature and provenance ----
offline_config() {
	# A Sigstore signing configuration with no transparency log, timestamp authority or
	# certificate authority: the signature is made and checked with the key alone.
	printf '%s\n' '{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json","caUrls":[],"oidcUrls":[],"rekorTlogUrls":[],"tsaUrls":[],"rekorTlogConfig":{"selector":"ANY"},"tsaConfig":{"selector":"ANY"}}' >"$1"
}

provenance() {
	commit=$(git rev-parse HEAD)
	epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
	gover=$(sed -n 's/^toolchain go//p' go.mod)
	[ -n "$gover" ] || gover=$(sed -n 's/^go //p' go.mod)
	repo=${SOURCE_REPOSITORY:-https://github.com/jk-magic-creations/launchway}
	{
		printf '{"_type":"https://in-toto.io/Statement/v1","subject":['
		sep=
		while read -r sum file; do
			printf '%s{"name":"%s","digest":{"sha256":"%s"}}' "$sep" "$file" "$sum"
			sep=,
		done <"$out/SHA256SUMS"
		printf '],"predicateType":"https://slsa.dev/provenance/v1","predicate":{'
		printf '"buildDefinition":{"buildType":"%s/blob/%s/packaging/release/build.sh",' "$repo" "$commit"
		printf '"externalParameters":{"version":"%s","source":"git+%s@%s"},' "$version" "$repo" "$commit"
		printf '"internalParameters":{"goToolchain":"go%s","sourceDateEpoch":%s,"dirty":%s},' "$gover" "$epoch" \
			"$([ -n "$(git status --porcelain --untracked-files=no)" ] && echo true || echo false)"
		printf '"resolvedDependencies":[{"uri":"git+%s","digest":{"gitCommit":"%s"}}]},' "$repo" "$commit"
		printf '"runDetails":{"builder":{"id":"%s"},"metadata":{"invocationId":"%s"}}}}\n' \
			"${BUILDER_ID:-local:make release-local}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$out/provenance.json"
}

case $sign in
none)
	echo "finish.sh: SIGN=none: SHA256SUMS is not signed" >&2
	;;
keyless)
	# Sigstore keyless: a short-lived certificate for the workflow's OIDC identity, recorded in
	# the public transparency log. Runs only where an identity token is available (GitHub Actions
	# with id-token: write).
	"$COSIGN" sign-blob --yes --bundle "$out/SHA256SUMS.sigstore.json" "$out/SHA256SUMS"
	;;
key)
	key=${COSIGN_KEY:-}
	if [ -z "$key" ]; then
		key=$root/.release-key/cosign.key
		if [ ! -f "$key" ]; then
			echo "finish.sh: no COSIGN_KEY; making a signing key for this machine in .release-key/" >&2
			mkdir -p "$root/.release-key"
			(cd "$root/.release-key" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null)
		fi
		COSIGN_PASSWORD=${COSIGN_PASSWORD:-}
	fi
	export COSIGN_PASSWORD
	cfg=$(mktemp)
	trap 'rm -f "$cfg"' EXIT
	set --
	if [ "${TLOG:-0}" != 1 ]; then
		offline_config "$cfg"
		set -- --signing-config "$cfg"
	fi
	"$COSIGN" public-key --key "$key" >"$out/cosign.pub"
	"$COSIGN" sign-blob --yes --key "$key" "$@" --bundle "$out/SHA256SUMS.sigstore.json" "$out/SHA256SUMS"
	provenance
	"$COSIGN" attest-blob --yes --key "$key" "$@" --statement "$out/provenance.json" \
		--bundle "$out/provenance.sigstore.json"
	;;
*)
	echo "finish.sh: SIGN must be key, keyless or none" >&2
	exit 2
	;;
esac
chmod 0644 "$out"/*.json "$out"/SHA256SUMS* 2>/dev/null || :
echo "release $version in $out:"
(cd "$out" && ls -l)
