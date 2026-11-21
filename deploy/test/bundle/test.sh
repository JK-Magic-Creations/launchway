#!/bin/sh
# make test-bundle: checks the air-gapped bundle and installs from it on a kind cluster without
# any registry for the Launchway images.
#
#   deploy/test/bundle/test.sh --context kind-NAME dist/launchway-bundle-VERSION.tar
#
# Verifies BUNDLE-SHA256SUMS and its signature (the key in the bundle; a real installation takes
# the key from elsewhere), the release's SHA256SUMS, and the images' signatures from their saved
# OCI layouts, offline. Then loads the linux/amd64 image archives into the cluster's node, installs
# the launchway chart (server, development PostgreSQL) and the launchway-runner chart from the
# bundle's chart archives with imagePullPolicy Never, and waits for the runner to reach the server.
# Namespaces lw-bundle-server and lw-bundle-runners, deleted afterwards unless KEEP=1.
set -eu

ctx=
while [ $# -gt 1 ]; do
	case $1 in
	--context) ctx=$2; shift 2 ;;
	*) break ;;
	esac
done
[ $# -eq 1 ] || { echo "usage: $0 --context kind-NAME BUNDLE.tar" >&2; exit 2; }
case $ctx in kind-*) ;; *) echo "test-bundle: refusing context '$ctx': only kind-* clusters" >&2; exit 2 ;; esac
bundle=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
[ -f "$bundle" ] || { echo "test-bundle: no $bundle; run make bundle" >&2; exit 1; }
root=$(cd "$(dirname "$0")/../../.." && pwd)
COSIGN=${COSIGN:-$root/bin/tools/cosign}
cluster=${ctx#kind-}
k() { kubectl --context "$ctx" "$@"; }
h() { helm --kube-context "$ctx" "$@"; }
fail() {
	echo "test-bundle: FAIL: $*" >&2
	exit 1
}
pass() { echo "  ok: $*"; }
SRV=lw-bundle-server
RUN=lw-bundle-runners

work=$(mktemp -d)
cleanup() {
	rm -rf "$work"
	[ "${KEEP:-}" = 1 ] && return
	h uninstall runners -n $RUN --wait >/dev/null 2>&1 || :
	h uninstall lw -n $SRV --wait >/dev/null 2>&1 || :
	k delete namespace $RUN $SRV --ignore-not-found --wait=false >/dev/null 2>&1 || :
}
trap cleanup EXIT

tar -xf "$bundle" -C "$work"
dir=$(find "$work" -mindepth 1 -maxdepth 1 -type d -name 'launchway-bundle-*')
[ -n "$dir" ] || fail "no launchway-bundle-* directory in the archive"
cd "$dir"
version=${dir##*/launchway-bundle-}
ver=${version#v}
echo "bundle $version: $(du -sh . | cut -f1)"

sha256sum --quiet -c BUNDLE-SHA256SUMS || fail "BUNDLE-SHA256SUMS"
"$COSIGN" verify-blob --key release/cosign.pub --insecure-ignore-tlog \
	--bundle BUNDLE-SHA256SUMS.sigstore.json BUNDLE-SHA256SUMS >/dev/null 2>&1 || fail "the bundle's signature"
(cd release && sha256sum --quiet -c SHA256SUMS) || fail "the release's SHA256SUMS"
"$COSIGN" verify-blob --key release/cosign.pub --insecure-ignore-tlog \
	--bundle release/SHA256SUMS.sigstore.json release/SHA256SUMS >/dev/null 2>&1 || fail "the release's signature"
pass "checksums and signatures of the bundle and of the release"
for img in launchway launchway-runner; do
	"$COSIGN" verify --key release/cosign.pub --insecure-ignore-tlog --local-image "images/$img" >/dev/null 2>&1 ||
		fail "the signature of images/$img"
done
pass "image signatures verified offline from the saved OCI layouts"

# ---- load and install, no registry ----
for img in launchway launchway-runner; do
	KIND_EXPERIMENTAL_PROVIDER=${KIND_EXPERIMENTAL_PROVIDER:-podman} kind load image-archive --name "$cluster" \
		"images/$img-linux-amd64.oci.tar" >/dev/null 2>&1 || fail "kind load $img"
done
node=$(k get nodes -o jsonpath='{.items[0].metadata.name}')
names=$(podman exec "$node" crictl images -o json 2>/dev/null | jq -r '.images[].repoTags[]' | grep -E "/launchway(-runner)?:$ver$" || :)
server_ref=$(printf '%s\n' "$names" | grep -E "/launchway:$ver$" | head -n 1)
runner_ref=$(printf '%s\n' "$names" | grep -E "/launchway-runner:$ver$" | head -n 1)
if [ -z "$server_ref" ] || [ -z "$runner_ref" ]; then fail "the images are not on the node: $names"; fi
pass "images loaded into the node: $server_ref, $runner_ref"

h uninstall runners -n $RUN --wait >/dev/null 2>&1 || :
h uninstall lw -n $SRV --wait >/dev/null 2>&1 || :
k delete namespace $RUN $SRV --ignore-not-found --wait >/dev/null
h install lw "release/launchway-$ver.tgz" -n $SRV --create-namespace --wait --timeout 5m \
	--set postgresql.enabled=true --set runner.enabled=false --set server.replicas=1 \
	--set image.repository="${server_ref%:*}" --set image.pullPolicy=Never \
	--set-json config.extraConfig='{"auth":{"allow_anonymous":true}}' >/dev/null || fail "helm install launchway"
pass "launchway chart from the bundle: server ready"
k create namespace $RUN >/dev/null
k -n $RUN create secret generic launchway-runner-token \
	--from-literal=token="$(k -n $SRV get secret lw-launchway-runner-token -o jsonpath='{.data.token}' | base64 -d)" >/dev/null
h install runners "release/launchway-runner-$ver.tgz" -n $RUN --wait --timeout 3m \
	--set server.url="http://lw-launchway.$SRV.svc:8080" --set token.existingSecret=launchway-runner-token \
	--set runner.replicas=1 --set runner.kubernetes.namespace=$RUN \
	--set image.repository="${runner_ref%:*}" --set image.pullPolicy=Never >/dev/null || fail "helm install launchway-runner"
pass "launchway-runner chart from the bundle: runner ready (it reached the server)"
echo "test-bundle passed"
