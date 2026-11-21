#!/bin/sh
# The repository's own lint command, in the environment the dependency step
# made. Without one there is nothing to check, and the step says so.
set -e
. "$(dirname "$0")/enter.sh"
if [ -z "${PARAM_LINT_COMMAND:-}" ]; then
  echo "no lint_command given: nothing to lint"
  exit 0
fi
[ -d "$VENV" ] && PATH="$VENV/bin:$PATH"
echo "+ $PARAM_LINT_COMMAND"
eval "$PARAM_LINT_COMMAND"
