#!/bin/sh
# Formatting and go vet for the module in the working directory, then the
# lint command if one is given.
set -e
# Style is advice by default (lint_mode: advise): findings are printed and do not fail the run.
if [ "${PARAM_LINT_MODE:-advise}" = advise ] && [ -z "${LW_ENFORCED:-}" ]; then
  if LW_ENFORCED=1 sh "$0"; then exit 0; fi
  echo "launchway: the style findings above are advice (lint_mode: advise); they do not fail the run. Set lint_mode: enforce to block on them." >&2
  exit 0
fi
. "$(dirname "$0")/../common.sh"
enter
want=$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' go.mod | head -1)
say "go.mod asks for Go ${want:-?}; this is $(go version | cut -d' ' -f3)"
bad=$(gofmt -l . 2>/dev/null | grep -v '^vendor/' || true)
if [ -n "$bad" ]; then
  echo "These files are not formatted the way gofmt writes them:" >&2
  echo "$bad" >&2
  echo "Fix: gofmt -w ." >&2
  exit 1
fi
run go vet ./...
[ -z "${PARAM_LINT_COMMAND:-}" ] || run "$PARAM_LINT_COMMAND"
