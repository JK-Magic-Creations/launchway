#!/bin/sh
# Runs every native Go fuzz target for a short time (FUZZTIME, default 15s each), one at a time.
# Each target feeds a parser with input an outsider can control: pipeline files and templates,
# cron schedules, webhook signatures, Git service paths, and the test, coverage, SBOM and security
# report parsers. A crash is saved under the package's testdata/fuzz and fails; commit it, fix the
# parser, and the saved input becomes a regression test that runs with every `go test`.
# A short run finds shallow bugs only; see docs/security/how-launchway-is-scanned.md for the
# continuous fuzzing a long-running service would add.
set -eu
. "$(dirname "$0")/lib.sh"
cd "$ROOT"
FUZZTIME="${FUZZTIME:-15s}"
# Instrumented builds are large; keep their scratch space off a small /tmp.
if [ -z "${GOTMPDIR:-}" ] && mkdir -p "$HOME/.cache/launchway-gotmp" 2>/dev/null; then
	GOTMPDIR="$HOME/.cache/launchway-gotmp"
	export GOTMPDIR
fi
targets="
./internal/pipeline FuzzParse
./internal/pipeline FuzzParseCron
./internal/pipeline FuzzDescribeTemplate
./internal/api FuzzVerifyGitHook
./internal/forge/githttp FuzzService
./internal/testreport FuzzParse
./internal/findings FuzzParse
./internal/findings/jsonscan FuzzValue
./internal/sbom FuzzParse
./internal/coverage FuzzParse
./internal/deploy/edit FuzzSet
"
avail="$(awk '/MemAvailable/{print int($2/1048576)}' /proc/meminfo 2>/dev/null || echo 99)"
[ "$avail" -ge 4 ] || { echo "fuzz: only ${avail} GB of memory is free; stop something first" >&2; exit 2; }
echo "$targets" | while read -r pkg name; do
	[ -n "$pkg" ] || continue
	echo "== $pkg $name ($FUZZTIME)"
	env -u SSH_ASKPASS GIT_ASKPASS=/bin/false GIT_TERMINAL_PROMPT=0 timeout -k 30s 10m \
		go test -count=1 -run '^$' -fuzz "^${name}\$" -fuzztime "$FUZZTIME" "$pkg"
done
echo "fuzz: no crashes"
