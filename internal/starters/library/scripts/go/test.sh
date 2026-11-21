#!/bin/sh
# go test with a JSON report for the Tests tab and a coverage profile for the
# Coverage tab. The exit status of go test is kept (it is not lost in a pipe).
set -e
. "$(dirname "$0")/../common.sh"
enter
rd=$(report_dir)
mkdir -p "$rd"
flags="-count=1 -json -coverprofile=$rd/cover.out"
[ "${PARAM_RACE:-false}" = true ] && flags="$flags -race"
cmd=${PARAM_TEST_COMMAND:-}
[ -n "$cmd" ] || cmd="go test $flags ./..."
echo "+ $cmd > $rd/go-test.json"
status=0
eval "$cmd" > "$rd/go-test.json" || status=$?
# One line per failing test, so the log says what failed without the Tests tab.
grep '"Action":"fail"' "$rd/go-test.json" | grep '"Test":' | head -50 || true
exit "$status"
