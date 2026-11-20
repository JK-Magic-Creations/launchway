#!/bin/sh
# Builds the release's container images from the binaries in OUTDIR, for linux/amd64 and
# linux/arm64, pushes them, signs them and attaches an SBOM and SLSA provenance to them; then
# runs finish.sh again so that SHA256SUMS (and its signature) covers images.txt, the list of
# image digests.
#
#   packaging/release/images.sh VERSION OUTDIR
#
# Images: $IMAGE_REPO/launchway (the server, and everything else) and $IMAGE_REPO/launchway-runner
# (the same binary, starting a runner), tagged with the version without its v.
#
# Environment:
#   IMAGE_REPO   where to push, such as ghcr.io/jk-magic-creations (the release workflow). Default:
#                127.0.0.1:5555/launchway, a throwaway registry this script starts in a container
#                (launchway-release-registry) when nothing listens there.
#   SIGN         key (default), keyless or none; COSIGN_KEY, COSIGN_PASSWORD and TLOG as in
#                finish.sh
#   PLATFORMS    default linux/amd64,linux/arm64
#   CONTAINER    podman (the only one supported: it builds the multi-arch manifest lists)
set -eu

[ $# -eq 2 ] || { echo "usage: $0 VERSION OUTDIR" >&2; exit 2; }
version=$1
out=$(cd "$2" && pwd)
ver=${version#v}
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
tools=$root/bin/tools
COSIGN=${COSIGN:-$tools/cosign}
SYFT=${SYFT:-$tools/syft}
REGISTRY_IMAGE=${REGISTRY_IMAGE:-docker.io/library/registry:3@sha256:82f6ff4a7f982960c8aa51617fea3aabc485df6e55cce80d22deb1de36f1bc3c}
platforms=${PLATFORMS:-linux/amd64,linux/arm64}
sign=${SIGN:-key}
repo=${IMAGE_REPO:-127.0.0.1:5555/launchway}
SYFT_CHECK_FOR_APP_UPDATE=false
export SYFT_CHECK_FOR_APP_UPDATE

for p in $(echo "$platforms" | tr , ' '); do
	[ -f "$out/launchway-$ver-${p%/*}-${p#*/}" ] || { echo "images.sh: $out/launchway-$ver-${p%/*}-${p#*/} is missing; run release.sh first" >&2; exit 1; }
done

# A registry on this machine is plain HTTP.
insecure=no
case $repo in
127.0.0.1:* | localhost:*)
	insecure=yes
	port=${repo#*:}
	port=${port%%/*}
	if ! curl -fsS "http://127.0.0.1:$port/v2/" >/dev/null 2>&1; then
		echo "images.sh: starting a throwaway registry on 127.0.0.1:$port"
		podman rm -f launchway-release-registry >/dev/null 2>&1 || :
		podman run -d --name launchway-release-registry -p "127.0.0.1:$port:5000" "$REGISTRY_IMAGE" >/dev/null
		i=0
		until curl -fsS "http://127.0.0.1:$port/v2/" >/dev/null 2>&1; do
			i=$((i + 1))
			[ $i -lt 30 ] || { echo "images.sh: the registry did not start" >&2; exit 1; }
			sleep 1
		done
	fi
	;;
esac
tlsflag=
cosignflags=
[ "$insecure" = no ] || {
	tlsflag=--tls-verify=false
	cosignflags=--allow-http-registry
	SYFT_REGISTRY_INSECURE_USE_HTTP=true
	export SYFT_REGISTRY_INSECURE_USE_HTTP
}

commit=$(git rev-parse HEAD)
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
created=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$epoch" +%Y-%m-%dT%H:%M:%SZ)
source_repo=${SOURCE_REPOSITORY:-https://github.com/JK-Magic-Creations/launchway}

work=$(mktemp -d)
cfg=$work/signing-config.json
trap 'rm -rf "$work"' EXIT

# cosign_sign ARGS...: cosign with the key or keyless settings of $sign.
key=
case $sign in
key)
	key=${COSIGN_KEY:-$root/.release-key/cosign.key}
	[ -f "$key" ] || { echo "images.sh: no signing key $key; run release.sh (which makes one) or set COSIGN_KEY" >&2; exit 1; }
	[ -n "${COSIGN_KEY:-}" ] || COSIGN_PASSWORD=${COSIGN_PASSWORD:-}
	export COSIGN_PASSWORD
	;;
keyless | none) ;;
*) echo "images.sh: SIGN must be key, keyless or none" >&2; exit 2 ;;
esac
signflags() {
	if [ "$sign" = key ]; then
		printf '%s\n' --key "$key"
		if [ "${TLOG:-0}" != 1 ]; then
			printf '%s\n' '{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json","caUrls":[],"oidcUrls":[],"rekorTlogUrls":[],"tsaUrls":[],"rekorTlogConfig":{"selector":"ANY"},"tsaConfig":{"selector":"ANY"}}' >"$cfg"
			printf '%s\n' --signing-config "$cfg"
		fi
	fi
	[ -z "$cosignflags" ] || printf '%s\n' "$cosignflags"
}

# cosign_q ARGS...: cosign, quiet unless it fails.
cosign_q() {
	"$COSIGN" "$@" >"$work/cosign.log" 2>&1 || {
		cat "$work/cosign.log" >&2
		echo "images.sh: cosign $1 failed" >&2
		exit 1
	}
}

mkdir -p "$out/images"
: >"$work/images.txt"
for target in server runner; do
	case $target in server) name=launchway ;; runner) name=launchway-runner ;; esac
	ref=$repo/$name
	local_ref=localhost/launchway-release/$name:$ver
	podman manifest rm "$local_ref" >/dev/null 2>&1 || :
	echo "building $ref:$ver ($platforms)"
	podman build --quiet --platform "$platforms" --manifest "$local_ref" \
		-f packaging/release/Containerfile --target "$target" \
		--build-arg VERSION="$ver" --build-arg REVISION="$commit" --build-arg CREATED="$created" \
		--build-arg SOURCE="$source_repo" \
		--timestamp "$epoch" "$out" >/dev/null
	# shellcheck disable=SC2086 # $tlsflag is empty or one flag
	podman manifest push --all $tlsflag --digestfile "$work/digest" "$local_ref" "docker://$ref:$ver" >/dev/null 2>&1
	digest=$(cat "$work/digest")
	echo "$ref:$ver@$digest" >>"$work/images.txt"
	echo "pushed $ref:$ver@$digest"

	# Each platform's image: its own SBOM, attached as a signed CycloneDX attestation.
	# shellcheck disable=SC2086
	skopeo inspect --raw $tlsflag "docker://$ref@$digest" >"$work/index.json"
	for p in $(echo "$platforms" | tr , ' '); do
		arch=${p#*/}
		pdigest=$(jq -r --arg a "$arch" '.manifests[] | select(.platform.architecture == $a) | .digest' "$work/index.json")
		[ -n "$pdigest" ] || { echo "images.sh: no $p image in $ref@$digest" >&2; exit 1; }
		sbom=$out/images/$name-$ver-linux-$arch.cdx.json
		"$SYFT" scan "registry:$ref@$pdigest" --quiet --source-name "$name" --source-version "$version" \
			-o "cyclonedx-json=$sbom"
		echo "  $p $pdigest (SBOM $(basename "$sbom"))"
		if [ "$sign" != none ]; then
			# shellcheck disable=SC2046 # one flag per line
			cosign_q sign -y $(signflags) "$ref@$pdigest"
			# shellcheck disable=SC2046
			cosign_q attest -y $(signflags) --type cyclonedx --predicate "$sbom" "$ref@$pdigest"
		fi
	done

	# The index: signed, with SLSA provenance naming the release binaries it was built from.
	{
		printf '{"buildDefinition":{"buildType":"%s/blob/%s/packaging/release/images.sh",' "$source_repo" "$commit"
		printf '"externalParameters":{"version":"%s","target":"%s","platforms":"%s","source":"git+%s@%s"},' \
			"$version" "$target" "$platforms" "$source_repo" "$commit"
		printf '"internalParameters":{"containerfile":"packaging/release/Containerfile","sourceDateEpoch":%s},' "$epoch"
		printf '"resolvedDependencies":[{"uri":"git+%s","digest":{"gitCommit":"%s"}}' "$source_repo" "$commit"
		for p in $(echo "$platforms" | tr , ' '); do
			f=launchway-$ver-${p%/*}-${p#*/}
			printf ',{"name":"%s","digest":{"sha256":"%s"}}' "$f" "$(sha256sum "$out/$f" | cut -d' ' -f1)"
		done
		printf ']},"runDetails":{"builder":{"id":"%s"},"metadata":{"invocationId":"%s"}}}\n' \
			"${BUILDER_ID:-local:make release-images}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} >"$out/images/$name-$ver.provenance.json"
	if [ "$sign" != none ]; then
		# shellcheck disable=SC2046
		cosign_q sign -y $(signflags) "$ref@$digest"
		# shellcheck disable=SC2046
		cosign_q attest -y $(signflags) --type slsaprovenance1 --predicate "$out/images/$name-$ver.provenance.json" \
			"$ref@$digest"
		echo "  signed, with SBOM and provenance attestations"
	fi
done
cp "$work/images.txt" "$out/images.txt"

# SHA256SUMS now lists images.txt too.
packaging/release/finish.sh "$version" "$out" >/dev/null
echo "images in $out/images.txt:"
cat "$out/images.txt"
