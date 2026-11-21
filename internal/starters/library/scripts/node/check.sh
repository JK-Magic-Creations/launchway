#!/bin/sh
# lint, format check and type check: each runs when package.json defines it
# (lint, format:check, typecheck), and TypeScript projects without a
# typecheck script are checked with tsc --noEmit.
set -e
# Style is advice by default (lint_mode: advise): findings are printed and do not fail the run.
if [ "${PARAM_LINT_MODE:-advise}" = advise ] && [ -z "${LW_ENFORCED:-}" ]; then
  if LW_ENFORCED=1 sh "$0"; then exit 0; fi
  echo "launchway: the style findings above are advice (lint_mode: advise); they do not fail the run. Set lint_mode: enforce to block on them." >&2
  exit 0
fi
. "$(dirname "$0")/../common.sh"
enter
here=$(cd "$(dirname "$0")" && pwd)
ran=
for n in lint format:check typecheck; do
  has=$(node -e 'const s=(require("./package.json").scripts)||{};process.stdout.write(s[process.argv[1]]?"1":"")' "$n")
  if [ -n "$has" ]; then LW_SCRIPT=$n sh "$here/script.sh"; ran=1; fi
done
has=$(node -e 'const s=(require("./package.json").scripts)||{};process.stdout.write(s.typecheck?"1":"")')
if [ -z "$has" ] && [ -f tsconfig.json ] && [ -x node_modules/.bin/tsc ]; then
  run node_modules/.bin/tsc --noEmit; ran=1
fi
[ -z "${PARAM_LINT_COMMAND:-}" ] || { run "$PARAM_LINT_COMMAND"; ran=1; }
[ -n "$ran" ] || say "no lint, format:check or typecheck script in package.json, and no TypeScript: nothing was checked (add a \"lint\" script)"
