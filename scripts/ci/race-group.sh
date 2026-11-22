#!/bin/sh
# Runs one group of the Go packages under the race detector, the way the hosted CI matrix does
# (.github/workflows/ci.yml, job `race`), with -short: the tests that are long end-to-end runs or
# scale tests skip themselves, and the full non-race run (`go test ./...`, as the project's pipeline
# runs it) is where they run.
#
#   scripts/ci/race-group.sh GROUP[:SHARD/SHARDS] [extra go test flags]
#
# Groups (disjoint; together every package except internal/forge/storagespike, which starts the test
# binary again as each Git hook, seconds per start when race-instrumented, and is raced for no
# benefit: the non-race run covers it):
#   api      internal/api            (its tests, split by name into SHARDS shards: shard i takes
#                                     every SHARDS-th test of the sorted list)
#   engine   internal/engine         (same)
#   storage  internal/forge/storage  (its tests, split by name like api)
#   forge    the other packages under internal/forge/
#   rest     every other package     (split by package: shard i takes every SHARDS-th package)
# DRY_RUN=1 prints what would run. TIMEOUT (default 20m) is go test's per-package timeout.
set -eu
spec=${1:?usage: race-group.sh GROUP[:SHARD/SHARDS] [go test flags]}
shift
group=${spec%%:*}
shard=1
shards=1
case $spec in
*:*/*)
	rest=${spec#*:}
	shard=${rest%/*}
	shards=${rest#*/}
	;;
esac
case $shard$shards in
*[!0-9]* | '') echo "race-group: bad shard in '$spec'" >&2; exit 2 ;;
esac
[ "$shard" -ge 1 ] && [ "$shard" -le "$shards" ] || { echo "race-group: shard $shard of $shards is out of range" >&2; exit 2; }
timeout=${TIMEOUT:-20m}

all=$(go list ./... | grep -v '/internal/forge/storagespike$')
case $group in
api) pkgs=$(printf '%s\n' "$all" | grep '/internal/api$') ;;
engine) pkgs=$(printf '%s\n' "$all" | grep '/internal/engine$') ;;
storage) pkgs=$(printf '%s\n' "$all" | grep '/internal/forge/storage$') ;;
forge) pkgs=$(printf '%s\n' "$all" | grep '/internal/forge/' | grep -v '/internal/forge/storage$') ;;
rest) pkgs=$(printf '%s\n' "$all" | grep -v -e '/internal/api$' -e '/internal/engine$' -e '/internal/forge/') ;;
*)
	echo "race-group: unknown group '$group' (api, engine, storage, forge or rest)" >&2
	exit 2
	;;
esac

run=
case $group in
api | engine | storage)
	# Split one package's tests by name. -list needs the package built, which the run needs anyway.
	names=$(go test -race -short -list '.' "$pkgs" | grep -E '^(Test|Fuzz|Example)' | LC_ALL=C sort | awk -v s="$shard" -v n="$shards" '(NR - 1) % n == s - 1')
	[ -n "$names" ] || { echo "race-group: no test in shard $spec" >&2; exit 2; }
	run="^($(printf '%s\n' "$names" | paste -sd'|' -))\$"
	echo "race-group: $spec: $(printf '%s\n' "$names" | wc -l | tr -d ' ') tests of $pkgs"
	;;
*)
	pkgs=$(printf '%s\n' "$pkgs" | awk -v s="$shard" -v n="$shards" '(NR - 1) % n == s - 1')
	[ -n "$pkgs" ] || { echo "race-group: no package in $spec" >&2; exit 2; }
	echo "race-group: $spec: $(printf '%s\n' "$pkgs" | wc -l | tr -d ' ') packages"
	;;
esac
echo "race-group: go test -race -short -timeout $timeout"
[ -z "${DRY_RUN:-}" ] || { printf '%s\n' "$pkgs"; [ -z "$run" ] || printf 'run %s\n' "$run" | cut -c1-200; exit 0; }
if [ -n "$run" ]; then
	# shellcheck disable=SC2086 # one package path per line, no spaces: word splitting is wanted
	exec go test -race -short -count=1 -timeout "$timeout" -run "$run" "$@" $pkgs
fi
# shellcheck disable=SC2086
exec go test -race -short -count=1 -timeout "$timeout" "$@" $pkgs
