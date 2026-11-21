#!/bin/sh
# Checks this library the way a pull request to it should be checked:
#
#   1. every script parses (sh -n) and is executable;
#   2. every template's scripts exist;
#   3. every example pipeline in pipelines/ is valid (launchway validate);
#   4. with go on the path and the Launchway source around (../..), the go test
#      that expands every example pipeline against these templates, which also
#      checks the environment and application parameter types, the deploy,
#      verify, rollback and approval steps and the reports each template declares.
#
# Usage: examples/library/check-library.sh [path to the launchway binary]
set -e
here=$(cd "$(dirname "$0")" && pwd)
lw=${1:-$(command -v launchway || true)}
fail=0
for f in "$here"/scripts/*.sh "$here"/scripts/service/*.sh; do
  sh -n "$f" || { echo "does not parse: $f" >&2; fail=1; }
  [ -x "$f" ] || { echo "not executable: $f" >&2; fail=1; }
done
for t in "$here"/templates/*.yaml; do
  for s in $(grep -o 'LAUNCHWAY_LIB/scripts/[A-Za-z0-9_./-]*\.sh' "$t" | sed 's|LAUNCHWAY_LIB/||' | sort -u); do
    [ -f "$here/$s" ] || { echo "$(basename "$t") runs $s, which is not in the library" >&2; fail=1; }
  done
done
if [ -n "$lw" ]; then
  for p in "$here"/pipelines/*.yaml; do
    "$lw" validate "$p" >/dev/null || { echo "invalid: $p" >&2; fail=1; }
  done
else
  echo "no launchway binary: skipping validate (pass its path as the first argument)" >&2
fi
if [ -f "$here/../../go.mod" ] && command -v go >/dev/null 2>&1; then
  (cd "$here/../.." && go test -count=1 ./internal/pipeline -run 'ExampleLibrary|ServiceC|SmallService|Pipelines') || fail=1
fi
[ "$fail" -eq 0 ] && echo "library checks passed"
exit "$fail"
