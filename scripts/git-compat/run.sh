#!/bin/sh
# make test-git-compat: runs the Git hosting tests (internal/forge/storage and
# internal/forge/storagespike) against the oldest git Launchway supports, which is
# not the git on this machine. The tests are compiled in the Go image the pipeline
# uses and run in an image with only that git. Skips, saying so, when neither podman
# nor docker is installed. Takes a few minutes the first time (it builds the image).
set -eu
cd "$(dirname "$0")/../.."
tool=${CONTAINER:-}
if [ -z "$tool" ]; then
	for t in podman docker; do command -v "$t" >/dev/null 2>&1 && { tool=$t; break; }; done
fi
if [ -z "$tool" ] || ! command -v "$tool" >/dev/null 2>&1; then
	echo "test-git-compat: skipped: neither podman nor docker is installed (set CONTAINER=<tool> to use another)"
	exit 0
fi
GO_IMAGE=${GO_IMAGE:-docker.io/library/golang:1.27}
GIT_VERSION=${GIT_VERSION:-$(sed -n 's/^ARG GIT_VERSION=//p' scripts/git-compat/Dockerfile)}
image=launchway-git-compat:$GIT_VERSION
if ! "$tool" image inspect "$image" >/dev/null 2>&1; then
	echo "test-git-compat: building $image (git $GIT_VERSION from source)"
	"$tool" build --build-arg GIT_VERSION="$GIT_VERSION" ${GIT_SHA256:+--build-arg GIT_SHA256="$GIT_SHA256"} -t "$image" scripts/git-compat
fi
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
chmod 755 "$out"
uid=$(id -u)
# :z relabels for SELinux and is harmless elsewhere. The module cache is mounted read-only.
mods=$(go env GOMODCACHE 2>/dev/null || echo "$HOME/go/pkg/mod")
userns=""
[ "$tool" = podman ] && userns="--userns=keep-id"
echo "test-git-compat: compiling the tests with $GO_IMAGE"
# shellcheck disable=SC2086
"$tool" run --rm --user "$uid:$(id -g)" $userns -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 \
	-v "$PWD":/src:ro,z -v "$mods":/go/pkg/mod:ro,z -v "$out":/out:z -w /src "$GO_IMAGE" \
	sh -c 'go test -c -o /out/storage.test ./internal/forge/storage/ && go test -c -o /out/spike.test ./internal/forge/storagespike/'
status=0
for t in storage spike; do
	echo "test-git-compat: running the $t tests"
	# shellcheck disable=SC2086
	"$tool" run --rm --user "$uid:$(id -g)" $userns -e HOME=/tmp -e GIT_TERMINAL_PROMPT=0 -v "$out":/t:ro,z -w /tmp "$image" \
		sh -c 'git --version; /t/'$t'.test -test.count=1 -test.timeout 9m' >"$out/$t.log" 2>&1 || status=$?
	if [ $status -ne 0 ]; then
		grep -v '^\(fatal: ref updates aborted by hook\|server rep[0-9]*: launchway: injected\)' "$out/$t.log" | tail -60
		echo "test-git-compat: FAILED on git $GIT_VERSION ($t tests)"
		exit $status
	fi
	sed -n '1p;$p' "$out/$t.log"
done
echo "test-git-compat: the hosting tests pass on git $GIT_VERSION"
