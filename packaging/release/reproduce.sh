#!/bin/sh
# Builds the release twice, each from a clean output directory (and, with the default
# WEB_BUILD=container, a fresh web UI build), and compares every artifact byte for byte.
#
#   packaging/release/reproduce.sh VERSION
#
# Exits non-zero if any file listed in SHA256SUMS differs. Signatures and provenance are not
# compared: a signature is new each time by design.
set -eu
[ $# -eq 1 ] || { echo "usage: $0 VERSION" >&2; exit 2; }
version=$1
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
a=dist/reproduce-a b=dist/reproduce-b
SIGN=none packaging/release/release.sh "$version" "$a"
SIGN=none packaging/release/release.sh "$version" "$b"
echo
echo "first build:"
cat "$a/SHA256SUMS"
if cmp -s "$a/SHA256SUMS" "$b/SHA256SUMS"; then
	echo
	echo "reproducible: both builds of $version are byte-identical ($(wc -l <"$a/SHA256SUMS") files)"
else
	echo
	echo "NOT reproducible; differences (first build <, second >):"
	diff "$a/SHA256SUMS" "$b/SHA256SUMS" || :
	exit 1
fi
