#!/bin/sh
# Builds Launchway's release binaries reproducibly: the same commit and version give the same
# bytes on any machine. Used by `make release-local` and by the release workflow, which call it
# the same way.
#
#   packaging/release/build.sh VERSION OUTDIR [PLATFORM ...]
#
# VERSION is a semantic version with a leading v (v1.4.0, v1.4.0-rc.1). PLATFORMs are os/arch
# pairs; the default is every supported one. The binaries are written as
# OUTDIR/launchway-<version without v>-<os>-<arch>.
#
# What makes the build reproducible:
#   - the Go toolchain is the one go.mod names (GOTOOLCHAIN), whatever Go is installed;
#   - -trimpath, no cgo, an empty build id, fixed GOAMD64/GOARM64 levels, -mod=readonly;
#   - the version comes from the argument and the commit and its time from Git (-buildvcs), never
#     from the clock;
#   - the web UI compiled into the binary is built in a container image pinned by digest
#     (WEB_BUILD=container, the default) from the files Git tracks, with the lockfile.
# The working tree must be clean (ALLOW_DIRTY=1 skips the check, and the binaries then say
# vcs.modified=true).
#
# Environment:
#   WEB_BUILD    container (default), local (the npm on PATH) or skip (web/dist is already built)
#   CONTAINER    podman (default) or docker, for WEB_BUILD=container
#   ALLOW_DIRTY  1 to build from a tree with uncommitted changes
set -eu

usage() { echo "usage: $0 VERSION OUTDIR [os/arch ...]" >&2; exit 2; }
[ $# -ge 2 ] || usage
version=$1 out=$2
shift 2
platforms=${*:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}

# Pinned like the Dockerfile's NODE_IMAGE, by digest as well as by tag.
NODE_IMAGE=${NODE_IMAGE:-docker.io/library/node:24.22.3-alpine3.23@sha256:3946fb783fdce8a2812136ea694bc95c698c4f4096f3fd18568dc27a8ac389dc}

if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
	echo "build.sh: version $version is not a semantic version with a leading v, such as v1.2.3" >&2
	exit 2
fi

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"

if [ "${ALLOW_DIRTY:-}" != 1 ] && [ -n "$(git status --porcelain --untracked-files=no)" ]; then
	echo "build.sh: the working tree has uncommitted changes; commit them or set ALLOW_DIRTY=1" >&2
	exit 1
fi

gover=$(sed -n 's/^toolchain go//p' go.mod)
[ -n "$gover" ] || gover=$(sed -n 's/^go //p' go.mod)
GOTOOLCHAIN=go$gover
export GOTOOLCHAIN

# ---- the web UI ----
build_web_container() {
	ctr=${CONTAINER:-podman}
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	# Only what Git tracks, so that a stray file cannot change the bundle.
	git ls-files -z web | tar --null -T - -cf - | tar -xf - -C "$tmp"
	rm -rf "$tmp/web/dist"
	"$ctr" run --rm --network=host -v "$tmp/web:/src/web:Z" -w /src/web \
		-e npm_config_update_notifier=false -e npm_config_fund=false -e npm_config_audit=false \
		"$NODE_IMAGE" sh -c 'npm ci --no-audit --no-fund --loglevel=error && npm run build'
	find web/dist -mindepth 1 ! -name .keep -exec rm -rf {} +
	cp -R "$tmp/web/dist/." web/dist/
	rm -rf "$tmp"
	trap - EXIT
}

case ${WEB_BUILD:-container} in
container) build_web_container ;;
local) (cd web && npm ci --no-audit --no-fund && npm run build) ;;
skip) [ -f web/dist/index.html ] || { echo "build.sh: WEB_BUILD=skip but web/dist/index.html is missing" >&2; exit 1; } ;;
*) echo "build.sh: WEB_BUILD must be container, local or skip" >&2; exit 2 ;;
esac

# ---- the binaries ----
mkdir -p "$out"
ver=${version#v}
for p in $platforms; do
	os=${p%/*} arch=${p#*/}
	case $os in
	linux | darwin) ;;
	# The host executor uses process groups and signals that Windows lacks; see SUPPORT.md.
	*) echo "build.sh: unsupported platform $p" >&2; exit 2 ;;
	esac
	bin="$out/launchway-$ver-$os-$arch"
	echo "building $bin"
	env CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0 GOFLAGS=-mod=readonly \
		go build -trimpath -buildvcs=true \
		-ldflags "-s -w -buildid= -X main.version=$version" \
		-o "$bin" ./cmd/launchway
done
