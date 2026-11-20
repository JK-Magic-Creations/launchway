#!/bin/sh
# Packages the Helm charts for a release: deploy/helm/launchway and deploy/helm/launchway-runner,
# each with the release's version as its version and appVersion (so the image tag defaults to
# the release). The archives are rewritten with sorted entries, fixed owners and the commit's
# time, so that packaging the same commit again gives the same bytes.
#
#   packaging/release/charts.sh VERSION OUTDIR
set -eu

[ $# -eq 2 ] || { echo "usage: $0 VERSION OUTDIR" >&2; exit 2; }
version=$1 out=$2
ver=${version#v}
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for chart in deploy/helm/launchway deploy/helm/launchway-runner; do
	[ -d "$chart" ] || continue
	name=$(basename "$chart")
	helm package "$chart" --version "$ver" --app-version "$ver" --destination "$work" >/dev/null
	mkdir -p "$work/x"
	tar -xzf "$work/$name-$ver.tgz" -C "$work/x"
	(
		cd "$work/x"
		find "$name" -print0 | LC_ALL=C sort -z |
			tar --no-recursion --mtime="@$epoch" --owner=0 --group=0 --numeric-owner \
				--format=ustar --null -T - -cf - | gzip -9n
	) >"$out/$name-$ver.tgz"
	rm -rf "$work/x" "$work/$name-$ver.tgz"
	echo "packaged $out/$name-$ver.tgz"
done
