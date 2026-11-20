#!/bin/sh
# Builds the launchway-runner deb and rpm packages from release binaries already in OUTDIR
# (packaging/release/build.sh), and renders the systemd unit for install.sh.
#
#   packaging/release/packages.sh VERSION OUTDIR [ARCH ...]     (ARCH: amd64 arm64)
#
# Needs nfpm (make tools). File times are the commit's, so rebuilding gives the same contents.
set -eu

[ $# -ge 2 ] || { echo "usage: $0 VERSION OUTDIR [arch ...]" >&2; exit 2; }
version=$1 out=$2
shift 2
arches=${*:-amd64 arm64}

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
NFPM=${NFPM:-$root/bin/tools/nfpm}
command -v "$NFPM" >/dev/null 2>&1 || { echo "packages.sh: nfpm not found; run make tools" >&2; exit 1; }

epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}
ver=${version#v}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sed 's#@BIN@#/usr/bin/launchway#' packaging/systemd/launchway-runner.service.in >"$work/launchway-runner.service"
# install.sh puts the binary in /usr/local/bin and the unit in /etc/systemd/system.
sed 's#@BIN@#/usr/local/bin/launchway#' packaging/systemd/launchway-runner.service.in >"$out/launchway-runner.service"
cp packaging/systemd/container-executor.conf "$out/launchway-runner-container.conf"

PKG_VERSION=$ver
PKG_UNIT=$work/launchway-runner.service
SOURCE_DATE_EPOCH=$epoch
export PKG_VERSION PKG_UNIT SOURCE_DATE_EPOCH
for arch in $arches; do
	PKG_ARCH=$arch
	PKG_BINARY=$out/launchway-$ver-linux-$arch
	export PKG_ARCH PKG_BINARY
	[ -f "$PKG_BINARY" ] || { echo "packages.sh: $PKG_BINARY is missing; build it first" >&2; exit 1; }
	# nfpm expands variables in some fields only, so the file is rendered first.
	sed -e "s#\${PKG_VERSION}#$PKG_VERSION#g" -e "s#\${PKG_ARCH}#$PKG_ARCH#g" \
		-e "s#\${PKG_BINARY}#$PKG_BINARY#g" -e "s#\${PKG_UNIT}#$PKG_UNIT#g" \
		packaging/nfpm.yaml >"$work/nfpm-$arch.yaml"
	for packager in deb rpm; do
		"$NFPM" package --config "$work/nfpm-$arch.yaml" --packager "$packager" --target "$out/"
	done
done
