#!/bin/sh
# Builds a complete release into OUTDIR without publishing anything: the binaries, the runner's
# deb and rpm packages, the install script, the Helm charts, an SBOM per artifact, SHA256SUMS,
# its signature and build provenance. `make release-local` and the release workflow both run it.
#
#   packaging/release/release.sh VERSION OUTDIR
#
# Each step is a script of its own (build.sh, packages.sh, finish.sh); see them for the
# environment they read. Container images are built separately (images.sh), as they need a
# registry to sign and attach to.
set -eu

[ $# -eq 2 ] || { echo "usage: $0 VERSION OUTDIR" >&2; exit 2; }
version=$1 out=$2
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"

rm -rf "$out"
mkdir -p "$out"
out=$(cd "$out" && pwd)

packaging/release/build.sh "$version" "$out"
packaging/release/packages.sh "$version" "$out"

# The install script names its release, so that `sh install.sh` without --version installs the
# release it came with.
sed "s/^DEFAULT_VERSION=\"@VERSION@\"$/DEFAULT_VERSION=\"$version\"/" packaging/install.sh >"$out/install.sh"
grep -q "^DEFAULT_VERSION=\"$version\"$" "$out/install.sh" || { echo "release.sh: could not stamp install.sh" >&2; exit 1; }
chmod 0755 "$out/install.sh"

# The charts, with the release's version as both the chart's and the app's.
packaging/release/charts.sh "$version" "$out"

packaging/release/finish.sh "$version" "$out"
